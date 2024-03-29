package server

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.uber.org/zap"
	"golang.org/x/mod/semver"

	"github.com/go-chi/chi"
	"github.com/go-chi/valve"
	"github.com/inconshreveable/go-update"
	"github.com/jpillora/backoff"
	"github.com/panjf2000/ants/v2"
	"github.com/robfig/cron/v3"
	"github.com/spf13/viper"

	"github.com/bizflycloud/bizfly-backup/pkg/backupapi"
	"github.com/bizflycloud/bizfly-backup/pkg/broker"
	"github.com/bizflycloud/bizfly-backup/pkg/cache"
	"github.com/bizflycloud/bizfly-backup/pkg/progress"
	"github.com/bizflycloud/bizfly-backup/pkg/storage_vault"
	"github.com/bizflycloud/bizfly-backup/pkg/storage_vault/s3"
	"github.com/bizflycloud/bizfly-backup/pkg/support"
)

var Version = "dev"

const (
	statusPendingFile = "PENDING"
	statusUploadFile  = "UPLOADING"
	statusComplete    = "COMPLETED"
	statusDownloading = "DOWNLOADING"
	statusFailed      = "FAILED"
)

const (
	PERCENT_PROCESS = 0.2
)

const (
	BACKUP_FAILED_PATH = "backup_failed"
)

const (
	maxCacheAgeDefault = 24 * time.Hour * 30
)

const (
	intervalTimeCheckUpgrade     = 86400 * time.Second
	intervalTimeCheckTaskRunning = 50 * time.Second
	intervalPushProgress         = 20 * time.Second
)

type contextStruct struct {
	ctx    context.Context
	cancel context.CancelFunc
}

// Server defines parameters for running BizFly Backup HTTP server.
type Server struct {
	Addr            string
	router          *chi.Mux
	b               broker.Broker
	subscribeTopics []string
	publishTopics   []string
	useUnixSock     bool
	backupClient    *backupapi.Client

	// mu guards handle broker event.
	mu                   sync.Mutex
	cronManager          *cron.Cron
	mappingToCronEntryID map[string]cron.EntryID

	// signal chan use for testing.
	testSignalCh chan os.Signal

	// Goroutines pool
	poolDir   *ants.Pool
	pool      *ants.Pool
	chunkPool *ants.Pool

	// Num goroutine
	numGoroutine int

	logger *zap.Logger

	// map contains context of running worker
	mapActionContext map[string]contextStruct
}

// New creates new server instance.
func New(opts ...Option) (*Server, error) {
	s := &Server{}
	for _, opt := range opts {
		if err := opt(s); err != nil {
			return nil, err
		}
	}

	s.router = chi.NewRouter()
	s.cronManager = cron.New(cron.WithParser(cron.NewParser(
		cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)))
	s.cronManager.Start()
	s.mappingToCronEntryID = make(map[string]cron.EntryID)
	s.mapActionContext = make(map[string]contextStruct)

	if s.logger == nil {
		l, err := backupapi.WriteLog()
		if err != nil {
			return nil, err
		}
		s.logger = l
	}

	s.setupRoutes()

	if s.numGoroutine == 0 {
		s.numGoroutine = int(float64(runtime.NumCPU()) * PERCENT_PROCESS)
		if s.numGoroutine <= 1 {
			s.numGoroutine = 2
		}
	}

	s.useUnixSock = strings.HasPrefix(s.Addr, "unix://")
	trimPrefix := "unix://"
	if !s.useUnixSock {
		trimPrefix = "http://"
	}
	s.Addr = strings.TrimPrefix(s.Addr, trimPrefix)

	var err error
	s.poolDir, err = ants.NewPool(s.numGoroutine)
	if err != nil {
		s.logger.Error("err ", zap.Error(err))
		return nil, err
	}

	s.pool, err = ants.NewPool(s.numGoroutine)
	if err != nil {
		s.logger.Error("err ", zap.Error(err))
		return nil, err
	}
	s.chunkPool, err = ants.NewPool(s.numGoroutine)
	if err != nil {
		s.logger.Error("err ", zap.Error(err))
		return nil, err
	}
	return s, nil
}

func (s *Server) setupRoutes() {
	s.router.Route("/backups", func(r chi.Router) {
		r.Get("/", s.ListBackup)
		r.Post("/", s.RequestBackup)
		r.Get("/{backupID}/recovery-points", s.ListRecoveryPoints)
		r.Post("/sync", s.SyncConfig)
	})

	s.router.Route("/recovery-points", func(r chi.Router) {
		r.Delete("/{recoveryPointID}", s.DeleteRecoveryPoints)
		r.Post("/{recoveryPointID}/restore", s.RequestRestore)
	})

	s.router.Route("/upgrade", func(r chi.Router) {
		r.Post("/", s.UpgradeAgent)
	})
	s.router.Route("/version", func(r chi.Router) {
		r.Post("/", s.Version)
	})
	s.router.Route("/actions", func(r chi.Router) {
		r.Get("/", s.ListAction)
		r.Delete("/{actionID}", s.StopAction)
	})
}

func (s *Server) ListAction(w http.ResponseWriter, r *http.Request) {
	c, err := s.backupClient.ListActivity(r.Context(), s.backupClient.Id, []string{statusDownloading, statusUploadFile})
	if err != nil {
		s.logger.Error("err ", zap.Error(err))
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(err.Error()))
		return
	}
	_ = json.NewEncoder(w).Encode(c)
}

func (s *Server) StopAction(w http.ResponseWriter, r *http.Request) {
	actionID := chi.URLParam(r, "actionID")

	msg := map[string]string{"event_type": broker.StopAction, "action_id": actionID}
	payload, _ := json.Marshal(msg)
	err := s.b.Publish("agent/"+s.backupClient.Id, payload)
	if err != nil {
		s.logger.Error("err ", zap.Error(err))
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(err.Error()))
		return
	}
	_, _ = w.Write([]byte("Success"))
}

func (s *Server) handleBrokerEvent(e broker.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	limitUpload := viper.GetInt("limit_upload")
	limitDownload := viper.GetInt("limit_download")
	var msg broker.Message
	if err := json.Unmarshal(e.Payload, &msg); err != nil {
		return err
	}
	s.logger.Debug("Got broker event", zap.String("event_type", msg.EventType))
	switch msg.EventType {
	case broker.BackupManual:
		limitDownload = 0
		var err error
		go func() {
			err = s.backup(msg.BackupDirectoryID, msg.PolicyID, msg.Name, limitUpload, limitDownload, backupapi.RecoveryPointTypeInitialReplica, ioutil.Discard)
		}()
		return err
	case broker.RestoreManual:
		limitUpload = 0
		var err error
		go func() {
			err = s.restore(msg.MachineID, msg.ActionId, msg.CreatedAt, msg.RestoreSessionKey, msg.RecoveryPointID, msg.DestinationDirectory, msg.StorageVaultId, limitUpload, limitDownload, ioutil.Discard)
		}()
		return err
	case broker.ConfigUpdate:
		return s.handleConfigUpdate(msg)
	case broker.ConfigRefresh:
		return s.handleConfigRefresh(msg.BackupDirectories)
	case broker.AgentUpgrade:
	case broker.StatusNotify:
		s.logger.Info("Got agent status", zap.String("status", msg.Status))

		// schedule check old cache directory after 1 days
		s.schedule(24*time.Hour, 1)

		// schedule update size of directory on machine after 15 minutes
		s.schedule(15*time.Minute, 2)
	case broker.StopAction:
		// Done context of running action
		if actionContext, ok := s.mapActionContext[msg.ActionId]; ok {
			actionContext.cancel()
		}
		s.notifyStatusFailed(msg.ActionId, backupapi.ErrorGotCancelRequest.Error())
	default:
		s.logger.Debug("Got unknown event", zap.Any("message", msg))
	}
	return nil
}

func (s *Server) handleConfigUpdate(config broker.Message) error {
	switch config.Action {
	case broker.ConfigUpdateActionAddPolicy,
		broker.ConfigUpdateActionUpdatePolicy,
		broker.ConfigUpdateActionActiveDirectory,
		broker.ConfigUpdateActionAddDirectory:
		s.removeFromCronManager(config.BackupDirectories)
		s.addToCronManager(config.BackupDirectories)
	case broker.ConfigUpdateActionDelPolicy,
		broker.ConfigUpdateActionDeactiveDirectory,
		broker.ConfigUpdateActionDelDirectory:
		s.removeFromCronManager(config.BackupDirectories)

	case broker.UpdateNumGoroutine:
		if config.NumGoroutine == 0 {
			return nil
		}
		s.logger.Sugar().Debugf("handleConfigUpdate: updating num_goroutine to %d", config.NumGoroutine)
		viper.Set("num_goroutine", config.NumGoroutine)
		s.chunkPool.Tune(config.NumGoroutine)
		s.pool.Tune(config.NumGoroutine)
		s.poolDir.Tune(config.NumGoroutine)

	default:
		return fmt.Errorf("unhandled action: %s", config.Action)
	}
	return nil
}

func (s *Server) handleConfigRefresh(backupDirectories []backupapi.BackupDirectoryConfig) error {
	ctx := s.cronManager.Stop()
	<-ctx.Done()
	s.cronManager = cron.New()
	s.cronManager.Start()
	s.mappingToCronEntryID = make(map[string]cron.EntryID)
	s.addToCronManager(backupDirectories)
	return nil
}

func mappingID(backupDirectoryID, policyID string) string {
	return backupDirectoryID + "|" + policyID
}

func (s *Server) removeFromCronManager(bdc []backupapi.BackupDirectoryConfig) {
	for _, bd := range bdc {
		for _, policy := range bd.Policies {
			mappingID := mappingID(bd.ID, policy.ID)
			if entryID, ok := s.mappingToCronEntryID[mappingID]; ok {
				s.cronManager.Remove(entryID)
				delete(s.mappingToCronEntryID, mappingID)
			}
		}
	}
}

func (s *Server) addToCronManager(bdc []backupapi.BackupDirectoryConfig) {
	for _, bd := range bdc {
		if !bd.Activated {
			continue
		}
		for _, policy := range bd.Policies {
			directoryID := bd.ID
			policyID := policy.ID
			limitUpload := policy.LimitUpload
			if limitUpload == 0 {
				limitUpload = viper.GetInt("limit_upload")
			}
			limitDownload := 0
			entryID, err := s.cronManager.AddFunc(policy.SchedulePattern, func() {
				name := "auto-" + time.Now().Format(time.RFC3339)
				// improve when support incremental backup
				recoveryPointType := backupapi.RecoveryPointTypeInitialReplica
				if err := s.backup(directoryID, policyID, name, limitUpload, limitDownload, recoveryPointType, ioutil.Discard); err != nil {
					zapFields := []zap.Field{
						zap.Error(err),
						zap.String("service", "cron"),
						zap.String("backup_directory_id", directoryID),
						zap.String("policy_id", policyID),
					}
					s.logger.Error("failed to run backup", zapFields...)
				}
			})
			if err != nil {
				s.logger.Error("failed to add cron entry", zap.Error(err))
				continue
			}
			s.mappingToCronEntryID[mappingID(bd.ID, policy.ID)] = entryID
		}
	}
}

func (s *Server) RequestBackup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID          string `json:"id"`
		StorageType string `json:"storage_type"`
		Name        string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`malformed body`))
		return

	}
	if err := s.requestBackup(body.ID, body.Name, body.StorageType); err != nil {
		return
	}
}

func (s *Server) ListBackup(w http.ResponseWriter, r *http.Request) {
	c, err := s.backupClient.GetConfig(r.Context())
	if err != nil {
		s.logger.Error("err ", zap.Error(err))
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(err.Error()))
		return
	}
	_ = json.NewEncoder(w).Encode(c)
}

func (s *Server) ListRecoveryPoints(w http.ResponseWriter, r *http.Request) {
	backupID := chi.URLParam(r, "backupID")
	rps, err := s.backupClient.ListRecoveryPoints(r.Context(), backupID)
	if err != nil {
		s.logger.Error("err ", zap.Error(err))
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(err.Error()))
		return
	}
	_ = json.NewEncoder(w).Encode(rps)
}

func (s *Server) DeleteRecoveryPoints(w http.ResponseWriter, r *http.Request) {
	recoveryPointID := chi.URLParam(r, "recoveryPointID")
	err := s.backupClient.DeleteRecoveryPoints(r.Context(), recoveryPointID)
	if err != nil {
		s.logger.Error("err ", zap.Error(err))
		_, _ = w.Write([]byte(err.Error()))
		return
	}
	_, _ = w.Write([]byte("Delete recovery point successfully"))
}

func (s *Server) RequestRestore(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MachineID string `json:"machine_id"`
		Path      string `json:"path"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`malformed body`))
		return
	}

	body.MachineID = s.backupClient.Id

	recoveryPointID := chi.URLParam(r, "recoveryPointID")
	if err := s.requestRestore(recoveryPointID, body.MachineID, body.Path); err != nil {
		return
	}
}

func (s *Server) SyncConfig(w http.ResponseWriter, r *http.Request) {
	c, err := s.backupClient.GetConfig(r.Context())
	if err != nil {
		s.logger.Error("err ", zap.Error(err))
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(err.Error()))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.handleConfigRefresh(c.BackupDirectories); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(err.Error()))
	}
}

func (s *Server) Version(w http.ResponseWriter, r *http.Request) {
	_, _ = w.Write([]byte(Version))
}

func (s *Server) UpgradeAgent(w http.ResponseWriter, r *http.Request) {
	if err := s.doUpgrade(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(err.Error()))
	}
}

func (s *Server) doUpgrade() error {
	if Version == "dev" {
		// Do not upgrade dev version
		return nil
	}

	lv, err := s.backupClient.LatestVersion()
	if err != nil {
		s.logger.Error("err ", zap.Error(err))
		return err
	}
	latestVer := "v" + lv.Ver
	currentVer := "v" + Version
	fields := []zap.Field{zap.String("current_version", currentVer), zap.String("latest_version", latestVer)}
	if semver.Compare(latestVer, currentVer) != 1 {
		s.logger.Warn("Current version is latest version.", fields...)
		return nil
	}

	var binURL string
	switch runtime.GOOS {
	case "linux":
		binURL = lv.Linux[runtime.GOARCH]
	case "macos":
		binURL = lv.Macos[runtime.GOARCH]
	case "windows":
		binURL = lv.Windows[runtime.GOARCH]
	default:
		return errors.New("unsupported OS")
	}
	if binURL == "" {
		return errors.New("failed to get download url")
	}

	s.logger.Info("Detect new version, downloading...", fields...)

	resp, err := http.Get(binURL)
	if err != nil {
		s.logger.Error("err ", zap.Error(err))
		return err
	}
	defer resp.Body.Close()

	s.logger.Info("Finish downloading, perform upgrading...")
	_ = update.Apply(resp.Body, update.Options{})

	// check running backup task to do not auto upgrade
	totalWait := 0 * time.Second
	for s.chunkPool.Running() > 0 || s.pool.Running() > 0 || s.poolDir.Running() > 0 {
		s.logger.Debug("Waiting all task done to auto restart")
		totalWait += intervalTimeCheckTaskRunning
		if totalWait >= intervalTimeCheckUpgrade {
			return nil
		}
		time.Sleep(intervalTimeCheckTaskRunning)
	}

	s.logger.Info("Cleaning...")
	if s.useUnixSock {
		//	Remove socket
		err := os.Remove(s.Addr)
		if err != nil {
			s.logger.Error("err ", zap.Error(err))
		}
	}

	// do action restart application
	s.logger.Info("Restarting...")
	err = restartByExec()
	if err != nil {
		return err
	}

	return nil
}

// restartByExec calls `syscall.Exec()` to restart app
func restartByExec() error {
	executableArgs := os.Args
	executableEnvs := os.Environ()

	// searches for an executable path
	executablePath, err := filepath.Abs(os.Args[0])
	if err != nil {
		return err
	}

	// searches for an executable file in path
	binary, err := exec.LookPath(executablePath)
	if err != nil {
		return err
	}

	time.Sleep(1 * time.Second)

	// calls `syscall.Exec()` to restart app
	err = syscall.Exec(binary, executableArgs, executableEnvs)
	if err != nil {
		return err
	}
	return nil
}

func (s *Server) upgradeLoop(ctx context.Context) {
	ticker := time.NewTicker(intervalTimeCheckUpgrade)
	defer ticker.Stop()

	s.logger.Debug("Start auto upgrade loop.")
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			if err := s.doUpgrade(); err != nil {
				fields := []zap.Field{
					zap.Error(err),
					zap.Time("at", t),
				}
				s.logger.Error("Auto upgrade run", fields...)
			}
		}
	}
}

func (s *Server) subscribeBrokerLoop(ctx context.Context) {
	if len(s.subscribeTopics) == 0 {
		return
	}
	b := &backoff.Backoff{Jitter: true}
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := s.b.ConnectAndSubscribe(s.handleBrokerEvent, s.subscribeTopics); err == nil {
			break
		} else {
			s.logger.Error("connect to broker failed", zap.Error(err))
			time.Sleep(b.Duration())
			continue
		}
	}

	// publish message to notify online status
	msg := map[string]string{"status": "ONLINE", "event_type": broker.StatusNotify}
	payload, _ := json.Marshal(msg)
	if err := s.b.Publish(s.publishTopics[0], payload); err != nil {
		s.logger.Error("failed to notify server status online", zap.Error(err))
	}
}

func (s *Server) shutdownSignalLoop(ctx context.Context, valv *valve.Valve) {
	for {
		<-time.After(1 * time.Second)

		func() {
			if err := valve.Lever(ctx).Open(); err != nil {
				s.logger.Error("failed to open valve")
				return
			}
			defer valve.Lever(ctx).Close()

			// signal control.
			select {
			case <-valve.Lever(ctx).Stop():
				s.logger.Debug("valve is closed")
				return

			case <-ctx.Done():
				s.logger.Debug("context is cancelled")
				return
			default:
			}
		}()
	}
}

func (s *Server) signalHandler(c chan os.Signal, valv *valve.Valve, srv *http.Server) {
	<-c
	// signal is a ^C, handle it
	s.logger.Info("shutting down...")

	// first valv
	if err := valv.Shutdown(20 * time.Second); err != nil {
		s.logger.Error("failed to shutdown valv")
	}

	// create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// start http shutdown
	if err := srv.Shutdown(ctx); err != nil {
		s.logger.Error("failed to shutdown http server")
	}

	// verify, in worst case call cancel via defer
	select {
	case <-time.After(21 * time.Second):
		s.logger.Error("not all connections done")
	case <-ctx.Done():
	}
}

func (s *Server) Run() error {
	// Graceful valve shut-off package to manage code preemption and shutdown signaling.
	valv := valve.New()
	baseCtx := valv.Context()

	go s.subscribeBrokerLoop(baseCtx)
	go s.shutdownSignalLoop(baseCtx, valv)
	go s.upgradeLoop(baseCtx)

	srv := http.Server{Handler: chi.ServerBaseContext(baseCtx, s.router)}

	c := make(chan os.Signal, 1)
	if s.testSignalCh != nil {
		c = s.testSignalCh
	}
	signal.Notify(c, syscall.SIGTERM, syscall.SIGINT)
	go s.signalHandler(c, valv, &srv)

	if s.useUnixSock {
		unixListener, err := net.Listen("unix", s.Addr)
		if err != nil {
			s.logger.Error("err ", zap.Error(err))
			return err
		}
		return srv.Serve(unixListener)
	}

	srv.Addr = s.Addr
	return srv.ListenAndServe()
}

func (s *Server) reportUploadCompleted(w io.Writer) {
	_, _ = w.Write([]byte("Upload completed ..."))
}

func (s *Server) notifyMsg(msg interface{}) {
	payload, _ := json.Marshal(msg)
	if err := s.b.Publish(s.publishTopics[0], payload); err != nil {
		s.logger.Warn("failed to notify server", zap.Error(err), zap.Any("message", msg))
	}
}

func (s *Server) notifyMsgProgress(recoverypointID string, msg map[string]string) {
	payload, _ := json.Marshal(msg)
	floatPercent, _ := strconv.ParseFloat(strings.ReplaceAll(msg["percent"], "%", ""), 64)

	if floatPercent > 0 {
		s.logger.Sugar().Infof("notifyMsgProgress: %s", msg)
		if err := s.b.Publish(s.publishTopics[1]+"/"+recoverypointID, payload); err != nil {
			s.logger.Warn("failed to notify server", zap.Error(err), zap.Any("message", msg))
		}
	}
}

func (s *Server) notifyStatusFailed(actionID, reason string) {
	s.notifyMsg(map[string]string{
		"action_id": actionID,
		"status":    statusFailed,
		"reason":    reason,
	})
}

// backup performs backup flow.
func (s *Server) backup(backupDirectoryID string, policyID string, name string, limitUpload, limitDownload int, recoveryPointType string, progressOutput io.Writer) error {
	chErr := make(chan error, 1)

	s.logger.Info("Backup directory ID: ", zap.String("backupDirectoryID", backupDirectoryID), zap.String("policyID", policyID), zap.String("name", name), zap.String("recoveryPointType", recoveryPointType))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create recovery point
	s.logger.Sugar().Infof("Creating recovery point %s", backupDirectoryID)
	actionCreateRP, err := s.backupClient.CreateRecoveryPoint(ctx, backupDirectoryID, &backupapi.CreateRecoveryPointRequest{
		PolicyID:          policyID,
		Name:              name,
		RecoveryPointType: recoveryPointType,
	})
	if err != nil {
		s.logger.Error("CreateRecoveryPoint error", zap.Error(err))
		chErr <- err
		return <-chErr
	}

	// Save context of worker to map for manage
	s.mapActionContext[actionCreateRP.ID] = contextStruct{ctx: ctx, cancel: cancel}

	// Notify status pending to backend
	s.notifyMsg(map[string]string{
		"action_id": actionCreateRP.ID,
		"status":    statusPendingFile,
	})

	_ = s.poolDir.Submit(s.backupWorker(ctx, actionCreateRP, backupDirectoryID, limitUpload, limitDownload, progressOutput, chErr))
	return <-chErr
}

// requestBackup performs a request backup flow.
func (s *Server) requestBackup(backupDirectoryID string, name string, storageType string) error {
	if err := s.backupClient.RequestBackupDirectory(backupDirectoryID, &backupapi.CreateManualBackupRequest{
		Action:      "backup_manual",
		StorageType: storageType,
		Name:        name,
	}); err != nil {
		return err
	}
	return nil
}

func (s *Server) reportStartDownload(w io.Writer) {
	_, _ = w.Write([]byte("Start downloading ..."))
}

func (s *Server) reportRestoreCompleted(w io.Writer) {
	_, _ = w.Write([]byte("Restore completed."))
}

func (s *Server) restore(machineID, actionID string, createdAt string, restoreSessionKey string, recoveryPointID string, destDir string, storageVaultID string, limitUpload, limitDownload int, progressOutput io.Writer) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Save context of worker to map for manage
	s.mapActionContext[actionID] = contextStruct{ctx: ctx, cancel: cancel}

	_, cachePath, err := support.CheckPath()
	if err != nil {
		s.notifyStatusFailed(actionID, err.Error())
		return err
	}

	// Get storage volume
	restoreKey := &backupapi.AuthRestore{
		RecoveryPointID:   recoveryPointID,
		ActionID:          actionID,
		CreatedAt:         createdAt,
		RestoreSessionKey: restoreSessionKey,
	}

	s.logger.Sugar().Info("Get credential storage vault", storageVaultID)
	vault, err := s.backupClient.GetCredentialStorageVault(storageVaultID, actionID, restoreKey)
	if err != nil {
		s.logger.Error("Get credential storage vault error", zap.Error(err))
		s.notifyStatusFailed(actionID, err.Error())
		return err
	}
	storageVault, _ := s.NewStorageVault(*vault, actionID, limitUpload, limitDownload)

	s.logger.Sugar().Info("Get recovery point info", recoveryPointID)
	rp, err := s.backupClient.GetRecoveryPointInfo(recoveryPointID)
	if err != nil {
		s.logger.Error("Error get recoveryPointInfo", zap.Error(err))
		s.notifyStatusFailed(actionID, err.Error())
		return err
	}

	_, err = os.Stat(filepath.Join(cachePath, machineID, recoveryPointID, "index.json"))
	if err != nil {
		if os.IsNotExist(err) {
			s.logger.Sugar().Info("Get index.json from storage", zap.String("key", filepath.Join(machineID, recoveryPointID, "index.json")))
			buf, err := storageVault.GetObject(filepath.Join(machineID, recoveryPointID, "index.json"))
			if err == nil {
				_ = os.MkdirAll(filepath.Join(cachePath, machineID, recoveryPointID), 0700)
				if err := ioutil.WriteFile(filepath.Join(cachePath, machineID, recoveryPointID, "index.json"), buf, 0700); err != nil {
					s.logger.Error("Error writing index.json file", zap.Error(err), zap.String("key", filepath.Join(machineID, recoveryPointID, "index.json")))
					s.notifyStatusFailed(actionID, err.Error())
					return err
				}
			} else {
				s.logger.Error("Error get index.json from storage", zap.Error(err), zap.String("key", filepath.Join(machineID, recoveryPointID, "index.json")))
				s.notifyStatusFailed(actionID, err.Error())
				return err
			}
		} else {
			s.logger.Error("Error stat index.json file", zap.Error(err))
			s.notifyStatusFailed(actionID, err.Error())
			return err
		}
	}

	index := cache.Index{}

	buf, err := ioutil.ReadFile(filepath.Join(cachePath, machineID, recoveryPointID, "index.json"))
	if err != nil {
		s.logger.Error("Error read index.json file", zap.Error(err), zap.String("key", filepath.Join(machineID, recoveryPointID, "index.json")))
		s.notifyStatusFailed(actionID, err.Error())
		return err
	} else {
		_ = json.Unmarshal([]byte(buf), &index)
	}

	hash := sha256.Sum256(buf)
	if hex.EncodeToString(hash[:]) != rp.IndexHash {
		s.logger.Error("index.json is corrupted", zap.Error(err))
		s.notifyStatusFailed(actionID, err.Error())
		return err
	}

	s.notifyMsg(map[string]string{
		"action_id": actionID,
		"status":    statusDownloading,
	})

	s.reportStartDownload(progressOutput)

	progressScan := s.newProgressScanDir(recoveryPointID)
	itemTodo, err := WalkerItem(&index, progressScan, s.logger)
	if err != nil {
		s.notifyStatusFailed(actionID, err.Error())
		return err
	}
	progressRestore := s.newDownloadProgress(recoveryPointID, itemTodo)
	progressRestore.Start()
	defer progressRestore.Done()

	s.logger.Sugar().Info("Restore directory", filepath.Clean(destDir))
	if err := s.backupClient.RestoreDirectory(ctx, index, filepath.Clean(destDir), storageVault, restoreKey, progressRestore); err != nil {
		s.logger.Error("failed to download file", zap.Error(err))
		cancel()
		s.notifyStatusFailed(actionID, err.Error())
		progressRestore.Done()
		return err
	}

	// remove worker out of manage context mapping
	delete(s.mapActionContext, actionID)

	select {
	case <-ctx.Done():
		return backupapi.ErrorGotCancelRequest
	default:
		s.reportRestoreCompleted(progressOutput)
		progressRestore.Done()
		s.notifyMsg(map[string]string{
			"action_id": actionID,
			"status":    statusComplete,
		})
	}

	return nil
}

// requestRestore performs a request restore flow.
func (s *Server) requestRestore(recoveryPointID string, machineID string, path string) error {
	if err := s.backupClient.RequestRestore(recoveryPointID, &backupapi.CreateRestoreRequest{
		MachineID: machineID,
		Path:      path,
	}); err != nil {
		return err
	}
	return nil
}

func (s *Server) NewStorageVault(storageVault backupapi.StorageVault, actionID string, limitUpload, limitDownload int) (storage_vault.StorageVault, error) {
	switch storageVault.StorageVaultType {
	case "S3":
		newS3Default, err := s3.NewS3Default(storageVault, actionID, limitUpload, limitDownload, s.backupClient)
		if err != nil {
			return nil, err
		}
		return newS3Default, nil
	default:
		return nil, fmt.Errorf(fmt.Sprintf("storage vault type not supported %s", storageVault.StorageVaultType))
	}
}

func WalkerItem(index *cache.Index, p *progress.Progress, logger *zap.Logger) (progress.Stat, error) {
	p.Start()
	defer p.Done()
	var lastDir string

	var st progress.Stat
	for _, itemInfo := range index.Items {
		if filepath.Dir(itemInfo.AbsolutePath) != lastDir {
			lastDir = filepath.Dir(itemInfo.AbsolutePath)
			logger.Sugar().Infof("WalkerItem scanning: %s", lastDir)
		}

		s := progress.Stat{
			Items: 1,
			Bytes: uint64(itemInfo.Size),
		}
		p.Report(s)
		st.Add(s)
	}
	return st, nil
}

func WalkerDir(dir string, index *cache.Index, p *progress.Progress, logger *zap.Logger) (progress.Stat, int64, error) {
	p.Start()
	defer p.Done()

	var lastDir string

	var st progress.Stat
	err := filepath.Walk(dir, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if filepath.Dir(path) != lastDir {
			lastDir = filepath.Dir(path)
			logger.Sugar().Infof("WalkerDir scanning: %s", lastDir)
		}

		s := progress.Stat{
			Items: 1,
			Bytes: uint64(fi.Size()),
		}

		node, err := cache.NodeFromFileInfo(dir, path, fi)
		if err != nil {
			return err
		}
		index.Items[path] = node

		if !fi.IsDir() {
			index.TotalFiles++
		}

		p.Report(s)
		st.Add(s)
		return nil
	})
	if err != nil {
		return progress.Stat{}, 0, err
	}
	return st, index.TotalFiles, nil
}

type backupJob func()

func (s *Server) uploadFileWorker(ctx context.Context, itemInfo *cache.Node, latestInfo *cache.Node, cacheWriter *cache.Repository, storageVault storage_vault.StorageVault,
	wg *sync.WaitGroup, size *uint64, errCh *error, p *progress.Progress, pipe chan<- *cache.Chunk, rpID, bdID string) backupJob {
	return func() {
		defer wg.Done()
		select {
		case <-ctx.Done():
			return
		default:
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			storageSize, err := s.backupClient.UploadFile(ctx, s.chunkPool, latestInfo, itemInfo, cacheWriter, storageVault, p, pipe, rpID, bdID)
			if err != nil {
				s.logger.Error("uploadFileWorker error", zap.Error(err))
				*errCh = err
				cancel()
				return
			}

			*size += storageSize
		}
	}
}

func (s *Server) backupWorker(ctx context.Context, actionCreateRP *backupapi.CreateRecoveryPointResponse, backupDirectoryID string, limitUpload, limitDownload int, progressOutput io.Writer, errCh chan<- error) backupJob {
	return func() {
		s.notifyMsg(map[string]string{
			"action_id": actionCreateRP.ID,
			"status":    statusUploadFile,
		})

		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		// Get BackupDirectory
		s.logger.Sugar().Info("Get backup directory", zap.String("backupDirectoryID", backupDirectoryID))
		bd, err := s.backupClient.GetBackupDirectory(backupDirectoryID)
		if err != nil {
			s.logger.Error("GetBackupDirectory error", zap.Error(err))
			errCh <- err
			return
		}

		// Get latest recovery point
		s.logger.Sugar().Info("Get latest recovery point", zap.String("backupDirectoryID", backupDirectoryID))
		lrp, err := s.backupClient.GetLatestRecoveryPointID(backupDirectoryID)
		if err != nil {
			s.notifyStatusFailed(actionCreateRP.ID, err.Error())
			s.logger.Error("GetLatestRecoveryPointID error", zap.Error(err))
			errCh <- err
			return
		}

		// Get storage vault
		storageVault, err := s.NewStorageVault(*actionCreateRP.StorageVault, actionCreateRP.ID, limitUpload, limitDownload)
		if err != nil {
			s.logger.Error("NewStorageVault error", zap.Error(err))
			errCh <- err
			return
		}

		// Scaning failed backup list
		s.logger.Sugar().Info("Scanning failed backup list")
		listBackupFailed, errScanListBackupFailed := scanListBackupFailed()
		if errScanListBackupFailed != nil {
			s.logger.Error("Err scan failed backup list", zap.Error(errScanListBackupFailed))
			errCh <- errScanListBackupFailed
			return
		}

		if listBackupFailed != nil {
			// Uploading failed backup list to storage
			s.logger.Sugar().Info("Uploading failed backup list to storage")
			errUploadListBackupFailed := s.uploadListBackupFailed(listBackupFailed, storageVault)
			if errUploadListBackupFailed != nil {
				errCh <- errUploadListBackupFailed
				return
			}
		}

		mcID := s.backupClient.Id
		rpID := actionCreateRP.RecoveryPoint.ID
		bdID := bd.ID
		progressScan := s.newProgressScanDir(rpID)

		index := cache.NewIndex(bd.ID, rpID)
		chunks := cache.NewChunk(bdID, rpID)

		s.logger.Sugar().Infof("Scanning directory %s", backupDirectoryID)
		itemTodo, totalFiles, err := WalkerDir(bd.Path, index, progressScan, s.logger)
		if err != nil {
			s.notifyStatusFailed(actionCreateRP.ID, err.Error())
			s.logger.Error("WalkerDir error", zap.Error(err))
			errCh <- err
			return
		}

		_, cachePath, err := support.CheckPath()
		if err != nil {
			errCh <- err
			return
		}

		cacheWriter, err := cache.NewRepository(cachePath, mcID, rpID)
		if err != nil {
			errCh <- err
			return
		}

		if lrp != nil {
			// Store index
			errStoreIndexs := s.storeIndexs(cachePath, mcID, lrp, storageVault)
			if errStoreIndexs != nil {
				s.notifyStatusFailed(actionCreateRP.ID, errStoreIndexs.Error())
				errCh <- errStoreIndexs
				return
			}
		}

		latestIndex := cache.Index{}
		if lrp != nil {
			buf, err := ioutil.ReadFile(filepath.Join(cachePath, mcID, lrp.ID, "index.json"))
			if err != nil {
				lrp = nil
			} else {
				_ = json.Unmarshal([]byte(buf), &latestIndex)
			}
		}

		pipe := make(chan *cache.Chunk)
		done := make(chan bool)
		go func() {
			for {
				receiver, more := <-pipe
				if more {
					key := reflect.ValueOf(receiver.Chunks).MapKeys()[0].Interface().(string)
					if value, ok := chunks.Chunks[key]; ok {
						count, errParseInt := strconv.Atoi(strings.Split(value[0], "-")[0])
						if errParseInt != nil {
							errCh <- errParseInt
						}
						chunks.Chunks[key] = []string{fmt.Sprintf("%s-%s", strconv.Itoa(count+1), receiver.Chunks[key][1])}
					} else {
						chunks.Chunks[key] = []string{fmt.Sprintf("%s-%s", strconv.Itoa(1), receiver.Chunks[key][1])}
					}

					if time.Now().Minute()%5 == 0 && time.Now().Second() == 0 {
						// Save chunks to chunk.json
						errSaveChunks := cacheWriter.SaveChunk(chunks)
						if errSaveChunks != nil {
							s.notifyStatusFailed(actionCreateRP.ID, errSaveChunks.Error())
							errCh <- errSaveChunks
							return
						}
					}
				} else {
					s.logger.Sugar().Info("Received all chunks")
					done <- true
					return
				}
			}
		}()

		var storageSize uint64
		var errFileWorker error
		progressUpload := s.newUploadProgress(rpID, itemTodo)

		var wg sync.WaitGroup

		progressUpload.Start()
		defer progressUpload.Cancel()

		for _, itemInfo := range index.Items {
			select {
			case <-ctx.Done():
				progressUpload.Cancel()
				break
			default:
				if errFileWorker != nil {
					s.logger.Error("uploadFileWorker error", zap.Error(errFileWorker))
					err = errFileWorker
					cancel()
					break
				}
				progressUpload.Start()
				st := progress.Stat{}
				st.Items = 1
				progressUpload.Report(st)

				if itemInfo.Type == "file" {
					lastInfo := latestIndex.Items[itemInfo.AbsolutePath]
					wg.Add(1)
					_ = s.pool.Submit(s.uploadFileWorker(ctx, itemInfo, lastInfo, cacheWriter, storageVault, &wg, &storageSize, &errFileWorker, progressUpload, pipe, rpID, bdID))
				}
			}
		}
		go func() {
			wg.Wait()
			close(pipe)
		}()
		<-done

		s.logger.Sugar().Info("Save all chunks to chunk.json")
		errSaveChunks := cacheWriter.SaveChunk(chunks)
		if errSaveChunks != nil {
			s.notifyStatusFailed(actionCreateRP.ID, errSaveChunks.Error())
			errCh <- errSaveChunks
			return
		}

		// Store files
		errWriterCSV := s.storeFiles(cachePath, mcID, rpID, index, storageVault)
		if errWriterCSV != nil {
			s.notifyStatusFailed(actionCreateRP.ID, errWriterCSV.Error())
			errCh <- errWriterCSV
			return
		}

		var chunkFailedPath, fileFailedPath string
		var errCopyChunk, errCopyFile error
		if errFileWorker != nil {
			// Copy chunk.json backup failed to /backup_failed/<machine_id>/<rp_id>/chunk.json
			s.logger.Sugar().Info("Copy chunk.json backup failed to /backup_failed/<machine_id>/<rp_id>/chunk.json")
			chunkFailedPath, errCopyChunk = copyCache(cachePath, mcID, rpID, "chunk.json")
			if errCopyChunk != nil {
				errCh <- errCopyChunk
				return
			}

			// Copy file.csv backup failed to /backup_failed/<machine_id>/<rp_id>/file.csv
			s.logger.Sugar().Info("Copy file.csv backup failed to /backup_failed/<machine_id>/<rp_id>/file.csv")
			fileFailedPath, errCopyFile = copyCache(cachePath, mcID, rpID, "file.csv")
			if errCopyFile != nil {
				errCh <- errCopyFile
				return
			}
		}

		// Put chunks
		s.logger.Sugar().Info("Put chunk.json to storage", zap.String("key", filepath.Join(mcID, rpID, "chunk.json")))
		errPutChunks := s.putChunks(cachePath, mcID, rpID, chunkFailedPath, storageVault)
		if errPutChunks != nil {
			s.notifyStatusFailed(actionCreateRP.ID, errPutChunks.Error())
			errCh <- errPutChunks
			return
		}

		// Put file.csv
		s.logger.Sugar().Info("Put file.csv to storage", zap.String("key", filepath.Join(mcID, rpID, "file.csv")))
		errPutFiles := s.putFiles(cachePath, mcID, rpID, fileFailedPath, storageVault)
		if errPutFiles != nil {
			s.notifyStatusFailed(actionCreateRP.ID, errPutFiles.Error())
			errCh <- errPutFiles
			return
		}

		if chunkFailedPath != "" || fileFailedPath != "" {
			errRemove := os.RemoveAll(BACKUP_FAILED_PATH)
			if errRemove != nil {
				errCh <- errRemove
				return
			}
		}

		if errFileWorker != nil {
			if err != nil {
				s.notifyStatusFailed(actionCreateRP.ID, err.Error())
			} else {
				s.notifyStatusFailed(actionCreateRP.ID, errFileWorker.Error())
			}
			s.logger.Error("Error uploadFileWorker error", zap.Error(errFileWorker))
			progressUpload.Done()
			errCh <- errFileWorker
			return
		}

		// Save Indexs
		err = cacheWriter.SaveIndex(index)
		if err != nil {
			s.notifyStatusFailed(actionCreateRP.ID, err.Error())
			errCh <- err
			return
		}

		// Put indexs
		s.logger.Sugar().Info("Put index.json to storage", zap.String("key", filepath.Join(mcID, rpID, "index.json")))
		indexHash, errPutIndexs := s.putIndexs(storageVault, latestIndex, cachePath, mcID, rpID)
		if errPutIndexs != nil {
			s.notifyStatusFailed(actionCreateRP.ID, errPutIndexs.Error())
			errCh <- errPutIndexs
			return
		}
		if lrp != nil {
			err := os.RemoveAll(filepath.Join(cachePath, mcID, lrp.ID))
			if err != nil {
				errCh <- err
				return
			}
		}

		// remove worker out of manage context mapping
		delete(s.mapActionContext, actionCreateRP.ID)

		// check if context done before return --> got cancel request
		// else report done
		select {
		case <-ctx.Done():
			errCh <- backupapi.ErrorGotCancelRequest
		default:
			s.reportUploadCompleted(progressOutput)
			progressUpload.Done()
			s.notifyMsg(map[string]string{
				"action_id":    actionCreateRP.ID,
				"status":       statusComplete,
				"index_hash":   indexHash,
				"storage_size": strconv.FormatUint(storageSize, 10),
				"total":        strconv.FormatUint(itemTodo.Bytes, 10),
				"total_files":  strconv.Itoa(int(totalFiles)),
			})
		}

		errCh <- nil
	}
}

func (s *Server) storeIndexs(cachePath, mcID string, lrp *backupapi.RecoveryPointResponse, storageVault storage_vault.StorageVault) error {
	_, err := os.Stat(filepath.Join(cachePath, mcID, lrp.ID, "index.json"))
	if err != nil {
		if os.IsNotExist(err) {
			buf, err := storageVault.GetObject(filepath.Join(mcID, lrp.ID, "index.json"))
			if err == nil {
				_ = os.MkdirAll(filepath.Join(cachePath, mcID, lrp.ID), 0700)
				if err := ioutil.WriteFile(filepath.Join(cachePath, mcID, lrp.ID, "index.json"), buf, 0700); err != nil {
					return err
				}
			} else {
				lrp = nil
			}
		} else {
			return err
		}
	} else {
		buf, err := ioutil.ReadFile(filepath.Join(cachePath, mcID, lrp.ID, "index.json"))
		if err != nil {
			return err
		}
		hash := sha256.Sum256(buf)
		if hex.EncodeToString(hash[:]) != lrp.IndexHash {
			lrp = nil
		}
	}
	return nil
}

func (s *Server) putIndexs(storageVault storage_vault.StorageVault, latestIndex cache.Index, cachePath, mcID, rpID string) (string, error) {
	buf, err := ioutil.ReadFile(filepath.Join(cachePath, mcID, rpID, "index.json"))
	if err != nil {
		s.logger.Error("Read indexs error", zap.Error(err))
		return "", err
	}
	err = storageVault.PutObject(filepath.Join(mcID, rpID, "index.json"), buf)
	if err != nil {
		s.logger.Error("Put indexs to storage error", zap.Error(err))
		os.RemoveAll(filepath.Join(cachePath, mcID, rpID))
		return "", err
	}
	hash := sha256.Sum256(buf)
	indexHash := hex.EncodeToString(hash[:])

	return indexHash, nil
}

func (s *Server) putChunks(cachePath, mcID, rpID, chunkPath string, storageVault storage_vault.StorageVault) error {
	if chunkPath == "" {
		chunkPath = filepath.Join(cachePath, mcID, rpID, "chunk.json")
	} else {
		chunkPath = filepath.Join(BACKUP_FAILED_PATH, mcID, rpID, "chunk.json")
	}
	buf, err := ioutil.ReadFile(chunkPath)
	if err != nil {
		s.logger.Error("Read chunk.json error", zap.Error(err))
		return err
	}
	err = storageVault.PutObject(filepath.Join(mcID, rpID, "chunk.json"), buf)
	if err != nil {
		s.logger.Error("Put chunk.json to storage error", zap.Error(err))
		return err
	}
	return nil
}

// Upload list backup failed to storage
func (s *Server) uploadListBackupFailed(listBackupFailed []string, storageVault storage_vault.StorageVault) error {
	for _, fileFailed := range listBackupFailed {
		buf, err := ioutil.ReadFile(filepath.Join(BACKUP_FAILED_PATH, fileFailed))
		if err != nil {
			s.logger.Error("Read file error ", zap.Error(err))
			return err
		}
		err = storageVault.PutObject(fileFailed, buf)
		if err != nil {
			s.logger.Error("Put file to storage error ", zap.Error(err))
			return err
		}
	}
	errRemove := os.RemoveAll(BACKUP_FAILED_PATH)
	if errRemove != nil {
		return errRemove
	}
	return nil
}

func (s *Server) storeFiles(cachePath, mcID string, rpID string, index *cache.Index, storageVault storage_vault.StorageVault) error {
	if _, err := os.Stat(filepath.Dir(filepath.Join(cachePath, mcID, rpID, "file.csv"))); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(cachePath, mcID, rpID, "file.csv")), 0700); err != nil {
			s.logger.Error("Err make dir dir file.csv", zap.Error(err))
			return err
		}
	}
	file, err := os.Create(filepath.Join(cachePath, mcID, rpID, "file.csv"))
	if err != nil {
		s.logger.Error("Err Create file.csv", zap.Error(err))
		return err
	}
	defer file.Close()
	writerCSV := csv.NewWriter(file)
	defer writerCSV.Flush()
	errWriteCSV := writerCSV.Write([]string{"name", "hash", "path", "size", "type", "modify_time"})
	if errWriteCSV != nil {
		return errWriteCSV
	}
	for _, itemInfo := range index.Items {
		itemHash := ""
		var itemSize uint64
		itemModifiedTime := itemInfo.ModTime.String()
		if itemInfo.Type == "file" {
			itemHash = itemInfo.Sha256Hash.String()
			itemSize = itemInfo.Size
		}
		err := writerCSV.Write([]string{itemInfo.Name, itemHash, itemInfo.AbsolutePath, strconv.FormatUint(itemSize, 10), itemInfo.Type, itemModifiedTime})
		if err != nil {
			s.logger.Error("Err writer file.csv", zap.Error(err))
			return err
		}
	}
	return nil
}

func (s *Server) putFiles(cachePath, mcID, rpID string, filePath string, storageVault storage_vault.StorageVault) error {
	if filePath == "" {
		filePath = filepath.Join(cachePath, mcID, rpID, "file.csv")
	} else {
		filePath = filepath.Join(BACKUP_FAILED_PATH, mcID, rpID, "file.csv")
	}
	buf, err := ioutil.ReadFile(filePath)
	if err != nil {
		s.logger.Error("Read file.csv error", zap.Error(err))
		return err
	}
	err = storageVault.PutObject(filepath.Join(mcID, rpID, "file.csv"), buf)
	if err != nil {
		s.logger.Error("Put file.csv error", zap.Error(err))
		return err
	}
	return nil
}

func (s *Server) newProgressScanDir(recoverypointID string) *progress.Progress {
	p := progress.NewProgress(time.Second)
	p.OnUpdate = func(stat progress.Stat, d time.Duration, ticker bool) {
		s.notifyMsgProgress(recoverypointID, map[string]string{
			"STATISTIC": stat.String(),
		})
	}
	p.OnDone = func(stat progress.Stat, d time.Duration, ticker bool) {
		s.notifyMsgProgress(recoverypointID, map[string]string{
			"SCANNED": stat.String(),
		})
	}
	return p
}

func (s *Server) newUploadProgress(recoveryPointID string, todo progress.Stat) *progress.Progress {
	p := progress.NewProgress(intervalPushProgress)

	var bps, eta uint64
	itemsTodo := todo.Items

	p.OnUpdate = func(stat progress.Stat, d time.Duration, ticker bool) {
		sec := uint64(d / time.Second)

		if todo.Bytes > 0 && sec > 0 && ticker {
			bps = stat.Bytes / sec
			if stat.Bytes >= todo.Bytes {
				eta = 0
			} else if bps > 0 {
				eta = (todo.Bytes - stat.Bytes) / bps
			}
		}

		if ticker {
			itemsDone := stat.Items
			strItemsDone := strconv.FormatUint(itemsDone, 10)
			strItemsTodo := strconv.FormatUint(itemsTodo, 10)

			s.notifyMsgProgress(recoveryPointID, map[string]string{
				"duration":          formatDuration(d),
				"percent":           formatPercent(stat.Bytes, todo.Bytes),
				"speed":             formatBytes(bps),
				"total":             fmt.Sprintf("%s/%s", formatBytes(stat.Bytes), formatBytes(todo.Bytes)),
				"push_storage":      formatBytes(stat.Storage),
				"items":             fmt.Sprintf("%s/%s", strItemsDone, strItemsTodo),
				"erros":             strconv.FormatBool(stat.Errors),
				"eta":               formatSeconds(eta),
				"recovery_point_id": recoveryPointID,
			})
		}
	}

	p.OnDone = func(stat progress.Stat, d time.Duration, ticker bool) {
		message := fmt.Sprintf("Duration: %s, %s", d, formatBytes(todo.Storage))
		s.notifyMsgProgress(recoveryPointID, map[string]string{
			"COMPLETE UPLOAD": message,
		})
	}

	p.OnCancel = func(stat progress.Stat, d time.Duration, ticker bool) {
		message := fmt.Sprintf("Duration: %s, %s", d, formatBytes(todo.Storage))
		s.notifyMsgProgress(recoveryPointID, map[string]string{
			"CANCELED UPLOAD": message,
		})
	}
	return p
}

func (s *Server) newDownloadProgress(recoveryPointID string, todo progress.Stat) *progress.Progress {
	p := progress.NewProgress(intervalPushProgress)

	var bps, eta uint64
	itemsTodo := todo.Items

	p.OnUpdate = func(stat progress.Stat, d time.Duration, ticker bool) {
		sec := uint64(d / time.Second)

		if todo.Bytes > 0 && sec > 0 && ticker {
			bps = stat.Bytes / sec
			if stat.Bytes >= todo.Bytes {
				eta = 0
			} else if bps > 0 {
				eta = (todo.Bytes - stat.Bytes) / bps
			}
		}

		if ticker {
			itemsDone := stat.Items
			strItemsDone := strconv.FormatUint(itemsDone, 10)
			strItemsTodo := strconv.FormatUint(itemsTodo, 10)

			s.notifyMsgProgress(recoveryPointID, map[string]string{
				"duration":          formatDuration(d),
				"percent":           formatPercent(stat.Bytes, todo.Bytes),
				"speed":             formatBytes(bps),
				"total":             fmt.Sprintf("%s/%s", formatBytes(stat.Bytes), formatBytes(todo.Bytes)),
				"pull_storage":      formatBytes(stat.Storage),
				"items":             fmt.Sprintf("%s/%s", strItemsDone, strItemsTodo),
				"erros":             strconv.FormatBool(stat.Errors),
				"eta":               formatSeconds(eta),
				"recovery_point_id": recoveryPointID,
			})
		}
	}

	p.OnDone = func(stat progress.Stat, d time.Duration, ticker bool) {
		message := fmt.Sprintf("Duration: %s, %s", d, formatBytes(todo.Storage))
		s.notifyMsgProgress(recoveryPointID, map[string]string{
			"COMPLETE DOWNLOAD": message,
		})
	}

	p.OnCancel = func(stat progress.Stat, d time.Duration, ticker bool) {
		message := fmt.Sprintf("Duration: %s, %s", d, formatBytes(todo.Storage))
		s.notifyMsgProgress(recoveryPointID, map[string]string{
			"CANCELED DOWNLOAD": message,
		})
	}
	return p
}

func formatBytes(c uint64) string {
	b := float64(c)

	switch {
	case c > 1<<40:
		return fmt.Sprintf("%.3f TiB", b/(1<<40))
	case c > 1<<30:
		return fmt.Sprintf("%.3f GiB", b/(1<<30))
	case c > 1<<20:
		return fmt.Sprintf("%.3f MiB", b/(1<<20))
	case c > 1<<10:
		return fmt.Sprintf("%.3f KiB", b/(1<<10))
	default:
		return fmt.Sprintf("%dB", c)
	}
}

func formatPercent(numerator uint64, denominator uint64) string {
	if denominator == 0 {
		return ""
	}
	percent := 100.0 * float64(numerator) / float64(denominator)
	if percent > 100 {
		percent = 100
	}
	return fmt.Sprintf("%3.2f%%", percent)
}

func formatSeconds(sec uint64) string {
	hours := sec / 3600
	sec -= hours * 3600
	min := sec / 60
	sec -= min * 60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, min, sec)
	}
	return fmt.Sprintf("%d:%02d", min, sec)
}

func formatDuration(d time.Duration) string {
	sec := uint64(d / time.Second)
	return formatSeconds(sec)
}

// Copy file (file.csv or chunk.json) backup failed to /backup_failed/<rp_id>
func copyCache(cachePath, mcID, rpID, fileName string) (string, error) {
	src := filepath.Join(cachePath, mcID, rpID, fileName)
	dst := filepath.Join(BACKUP_FAILED_PATH, mcID, rpID, fileName)
	if _, err := os.Stat(filepath.Dir(dst)); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
			return "", err
		}
	}

	fin, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer fin.Close()

	fout, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	defer fout.Close()

	_, err = io.Copy(fout, fin)

	if err != nil {
		return "", err
	}

	return dst, nil
}

// Scan list backup failed
func scanListBackupFailed() ([]string, error) {
	if _, err := os.Stat(BACKUP_FAILED_PATH); os.IsNotExist(err) {
		if err := os.MkdirAll(BACKUP_FAILED_PATH, 0700); err != nil {
			return nil, err
		}
	}

	var listBackupFailed []string

	dirEntries, err := ioutil.ReadDir(BACKUP_FAILED_PATH)
	if err != nil {
		return nil, err
	}

	for _, dir := range dirEntries {
		dirMc := filepath.Join(BACKUP_FAILED_PATH, dir.Name())
		dirMcRead, err := os.ReadDir(dirMc)
		if err != nil {
			return nil, err
		}

		for _, dirChild := range dirMcRead {
			dirRp := filepath.Join(dirMc, dirChild.Name())
			dirRpRead, err := os.ReadDir(dirRp)
			if err != nil {
				return nil, err
			}

			for _, f := range dirRpRead {
				nameFile := filepath.Join(dir.Name(), dirChild.Name(), f.Name())
				listBackupFailed = append(listBackupFailed, nameFile)
			}
		}
	}
	return listBackupFailed, nil
}

// Get size of directory on machine and send server via mqtt
func (s *Server) getDirectorySize() error {
	var size int64
	var state backupapi.UpdateState

	// Get list backup directory
	lbd, err := s.backupClient.ListBackupDirectory()
	if err != nil {
		s.logger.Error("ListBackupDirectory error", zap.Error(err))
		return err
	}

	if len(lbd.Directories) != 0 {
		for _, item := range lbd.Directories {
			err := filepath.Walk(item.Path, func(_ string, fi os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if !fi.IsDir() {
					size += fi.Size()
				}
				return nil
			})
			if err != nil {
				return err
			}

			dir := backupapi.Directories{
				ID:   item.ID,
				Size: int(size),
			}
			state.Directories = append(state.Directories, dir)
		}
		state.EventType = "agent_update_state"

		// Send msg to server via mqtt
		s.notifyMsg(state)
	}
	return nil
}

func (s *Server) schedule(timeSchedule time.Duration, index int) {
	ticker := time.NewTicker(timeSchedule)
	go func() {
		for {
			switch index {
			case 1:
				<-ticker.C
				s.logger.Sugar().Info("Check old cache directory")
				if err := cache.RemoveOldCache(maxCacheAgeDefault); err != nil {
					s.logger.Error(err.Error())
				}
			case 2:
				<-ticker.C
				s.logger.Sugar().Info("Update size of directory")
				if err := s.getDirectorySize(); err != nil {
					s.logger.Error(err.Error())
				}
			}
		}
	}()
}
