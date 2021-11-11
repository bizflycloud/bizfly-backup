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
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi"
	"github.com/go-chi/valve"
	"github.com/inconshreveable/go-update"
	"github.com/jpillora/backoff"
	"github.com/panjf2000/ants/v2"
	"github.com/robfig/cron/v3"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"golang.org/x/mod/semver"

	"github.com/bizflycloud/bizfly-backup/pkg/backupapi"
	"github.com/bizflycloud/bizfly-backup/pkg/broker"
	"github.com/bizflycloud/bizfly-backup/pkg/cache"
	"github.com/bizflycloud/bizfly-backup/pkg/progress"
	"github.com/bizflycloud/bizfly-backup/pkg/storage_vault"
	"github.com/bizflycloud/bizfly-backup/pkg/storage_vault/s3"
)

var Version = "dev"

const (
	statusUploadFile  = "UPLOADING"
	statusComplete    = "COMPLETED"
	statusDownloading = "DOWNLOADING"
	statusFailed      = "FAILED"
)

const (
	PERCENT_PROCESS = 0.2
)

const (
	CACHE_PATH         = ".cache"
	BACKUP_FAILED_PATH = "backup_failed"
)

const (
	maxCacheAgeDefault = 24 * time.Hour * 30
)

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

	logger *zap.Logger
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

	if s.logger == nil {
		l, err := backupapi.WriteLog()
		if err != nil {
			return nil, err
		}
		s.logger = l
	}

	s.setupRoutes()
	s.useUnixSock = strings.HasPrefix(s.Addr, "unix://")
	s.Addr = strings.TrimPrefix(s.Addr, "unix://")

	var err error
	numGoroutine := int(float64(runtime.NumCPU()) * PERCENT_PROCESS)
	if numGoroutine <= 1 {
		numGoroutine = 2
	}
	s.poolDir, err = ants.NewPool(numGoroutine)
	if err != nil {
		s.logger.Error("err ", zap.Error(err))
		return nil, err
	}

	s.pool, err = ants.NewPool(numGoroutine)
	if err != nil {
		s.logger.Error("err ", zap.Error(err))
		return nil, err
	}
	s.chunkPool, err = ants.NewPool(numGoroutine)
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
		r.Post("/{recoveryPointID}/restore", s.RequestRestore)
	})

	s.router.Route("/upgrade", func(r chi.Router) {
		r.Post("/", s.UpgradeAgent)
	})
	s.router.Route("/version", func(r chi.Router) {
		r.Post("/", s.Version)
	})
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
			err = s.restore(msg.ActionId, msg.CreatedAt, msg.RestoreSessionKey, msg.RecoveryPointID, msg.DestinationDirectory, msg.StorageVaultId, limitUpload, limitDownload, ioutil.Discard)
		}()
		return err
	case broker.ConfigUpdate:
		return s.handleConfigUpdate(msg.Action, msg.BackupDirectories)
	case broker.ConfigRefresh:
		return s.handleConfigRefresh(msg.BackupDirectories)
	case broker.AgentUpgrade:
	case broker.StatusNotify:
		s.logger.Info("Got agent status", zap.String("status", msg.Status))

		// schedule check old cache directory after 1 days
		s.schedule(24*time.Hour, 1)

		// schedule update size of directory on machine after 15 minutes
		s.schedule(15*time.Minute, 2)
	default:
		s.logger.Debug("Got unknown event", zap.Any("message", msg))
	}
	return nil
}

func (s *Server) handleConfigUpdate(action string, backupDirectories []backupapi.BackupDirectoryConfig) error {
	switch action {
	case broker.ConfigUpdateActionAddPolicy,
		broker.ConfigUpdateActionUpdatePolicy,
		broker.ConfigUpdateActionActiveDirectory,
		broker.ConfigUpdateActionAddDirectory:
		s.removeFromCronManager(backupDirectories)
		s.addToCronManager(backupDirectories)
	case broker.ConfigUpdateActionDelPolicy,
		broker.ConfigUpdateActionDeactiveDirectory,
		broker.ConfigUpdateActionDelDirectory:
		s.removeFromCronManager(backupDirectories)
	default:
		return fmt.Errorf("unhandled action: %s", action)
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
	//TODO: do not upgrade when there are running jobs
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
		s.logger.Warn("Current version is not less than latest version, do nothing", fields...)
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
	s.logger.Info("Upgrading done! TODO: self restart? For now, call os.Exit so service manager will restart us!")
	if s.useUnixSock {
		//	Remove socket and exit
		os.Remove(s.Addr)
	}
	os.Exit(0)
	return err
}

func (s *Server) upgradeLoop(ctx context.Context) {
	ticker := time.NewTicker(86400 * time.Second)
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
		if err := s.b.Connect(); err == nil {
			break
		}
		time.Sleep(b.Duration())
		continue
	}
	msg := map[string]string{"status": "ONLINE", "event_type": broker.StatusNotify}
	payload, _ := json.Marshal(msg)
	if err := s.b.Publish(s.publishTopics[0], payload); err != nil {
		s.logger.Error("failed to notify server status online", zap.Error(err))
	}
	if err := s.b.Subscribe(s.subscribeTopics, s.handleBrokerEvent); err != nil {
		s.logger.Error("Subscribe to subscribeTopics return error", zap.Error(err), zap.Strings("subscribeTopics", s.subscribeTopics))
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
	percent := int(math.Ceil(floatPercent))

	if percent%5 == 0 {
		if err := s.b.Publish(s.publishTopics[1]+"/"+recoverypointID, payload); err != nil {
			s.logger.Warn("failed to notify server", zap.Error(err), zap.Any("message", msg))
		}
	}
}

func (s *Server) notifyStatusFailed(recoveryPointID, reason string) {
	s.notifyMsg(map[string]string{
		"action_id": recoveryPointID,
		"status":    statusFailed,
		"reason":    reason,
	})
}

// backup performs backup flow.
func (s *Server) backup(backupDirectoryID string, policyID string, name string, limitUpload, limitDownload int, recoveryPointType string, progressOutput io.Writer) error {
	chErr := make(chan error, 1)
	_ = s.poolDir.Submit(s.backupWorker(backupDirectoryID, policyID, name, limitUpload, limitDownload, recoveryPointType, progressOutput, chErr))
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

func (s *Server) restore(actionID string, createdAt string, restoreSessionKey string, recoveryPointID string, destDir string, storageVaultID string, limitUpload, limitDownload int, progressOutput io.Writer) error {
	// Get storage volume
	restoreKey := &backupapi.AuthRestore{
		RecoveryPointID:   recoveryPointID,
		ActionID:          actionID,
		CreatedAt:         createdAt,
		RestoreSessionKey: restoreSessionKey,
	}

	vault, err := s.backupClient.GetCredentialStorageVault(storageVaultID, actionID, restoreKey)
	if err != nil {
		s.logger.Error("Get credential storage vault error", zap.Error(err))
		return err
	}

	storageVault, _ := NewStorageVault(*vault, actionID, limitUpload, limitDownload)

	rp, err := s.backupClient.GetRecoveryPointInfo(recoveryPointID)
	if err != nil {
		s.logger.Error("Error get recoveryPointInfo", zap.Error(err))
		return err
	}

	_, err = os.Stat(filepath.Join(CACHE_PATH, recoveryPointID, "index.json"))
	if err != nil {
		if os.IsNotExist(err) {
			buf, err := storageVault.GetObject(filepath.Join(recoveryPointID, "index.json"))
			if err == nil {
				_ = os.MkdirAll(filepath.Join(CACHE_PATH, recoveryPointID), 0700)
				if err := ioutil.WriteFile(filepath.Join(CACHE_PATH, recoveryPointID, "index.json"), buf, 0644); err != nil {
					s.logger.Error("Error writing index file", zap.Error(err))
					return err
				}
			} else {
				s.logger.Error("Error downloading index from storage", zap.Error(err))
				return err
			}
		} else {
			s.logger.Error("Error stat index file", zap.Error(err))
			return err
		}
	}

	index := cache.Index{}

	buf, err := ioutil.ReadFile(filepath.Join(CACHE_PATH, recoveryPointID, "index.json"))
	if err != nil {
		s.logger.Error("Error read index file", zap.Error(err))
		return err
	} else {
		_ = json.Unmarshal([]byte(buf), &index)
	}

	hash := sha256.Sum256(buf)
	if hex.EncodeToString(hash[:]) != rp.IndexHash {
		s.logger.Error("index.json is corrupted", zap.Error(err))
		return err
	}

	s.notifyMsg(map[string]string{
		"action_id": actionID,
		"status":    statusDownloading,
	})

	s.reportStartDownload(progressOutput)

	progressScan := s.newProgressScanDir(recoveryPointID)
	itemTodo, err := WalkerItem(&index, progressScan)
	if err != nil {
		s.notifyStatusFailed(rp.ID, err.Error())
		return err
	}
	progressRestore := s.newDownloadProgress(recoveryPointID, itemTodo)

	if err := s.backupClient.RestoreDirectory(index, filepath.Clean(destDir), storageVault, restoreKey, progressRestore); err != nil {
		s.logger.Error("failed to download file", zap.Error(err))
		s.notifyStatusFailed(actionID, err.Error())
		progressRestore.Done()
		return err
	}

	s.reportRestoreCompleted(progressOutput)
	progressRestore.Done()
	s.notifyMsg(map[string]string{
		"action_id": actionID,
		"status":    statusComplete,
	})
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

func NewStorageVault(storageVault backupapi.StorageVault, actionID string, limitUpload, limitDownload int) (storage_vault.StorageVault, error) {
	switch storageVault.StorageVaultType {
	case "S3":
		return s3.NewS3Default(storageVault, actionID, limitUpload, limitDownload), nil
	default:
		return nil, fmt.Errorf(fmt.Sprintf("storage vault type not supported %s", storageVault.StorageVaultType))
	}
}

func WalkerItem(index *cache.Index, p *progress.Progress) (progress.Stat, error) {
	p.Start()
	defer p.Done()

	var st progress.Stat
	for _, itemInfo := range index.Items {
		s := progress.Stat{
			Items: 1,
			Bytes: uint64(itemInfo.Size),
		}
		p.Report(s)
		st.Add(s)
	}
	return st, nil
}

func WalkerDir(dir string, index *cache.Index, p *progress.Progress) (progress.Stat, int64, error) {
	p.Start()
	defer p.Done()

	var st progress.Stat
	err := filepath.Walk(dir, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
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

func (s *Server) uploadFileWorker(ctx context.Context, itemInfo *cache.Node, latestInfo *cache.Node, cacheWriter *cache.Repository, chunks *cache.Chunk, storageVault storage_vault.StorageVault,
	wg *sync.WaitGroup, size *uint64, errCh *error, p *progress.Progress) backupJob {
	return func() {
		defer wg.Done()
		select {
		case <-ctx.Done():
			return
		default:
			p.Start()
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			storageSize, err := s.backupClient.UploadFile(ctx, s.chunkPool, latestInfo, itemInfo, cacheWriter, chunks, storageVault, p)
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

func (s *Server) backupWorker(backupDirectoryID string, policyID string, name string, limitUpload, limitDownload int, recoveryPointType string, progressOutput io.Writer, errCh chan<- error) backupJob {
	return func() {
		s.logger.Info("Backup directory ID: ", zap.String("backupDirectoryID", backupDirectoryID), zap.String("policyID", policyID), zap.String("name", name), zap.String("recoveryPointType", recoveryPointType))

		ctx := context.Background()
		// Get BackupDirectory
		bd, err := s.backupClient.GetBackupDirectory(backupDirectoryID)
		if err != nil {
			s.logger.Error("GetBackupDirectory error", zap.Error(err))
			errCh <- err
			return
		}

		// Create recovery point
		rp, err := s.backupClient.CreateRecoveryPoint(ctx, backupDirectoryID, &backupapi.CreateRecoveryPointRequest{
			PolicyID:          policyID,
			Name:              name,
			RecoveryPointType: recoveryPointType,
		})
		if err != nil {
			s.logger.Error("CreateRecoveryPoint error", zap.Error(err))
			errCh <- err
			return
		}

		// Get latest recovery point
		lrp, err := s.backupClient.GetLatestRecoveryPointID(backupDirectoryID)
		if err != nil {
			s.notifyStatusFailed(rp.ID, err.Error())
			s.logger.Error("GetLatestRecoveryPointID error", zap.Error(err))
			errCh <- err
			return
		}

		// Get storage vault
		storageVault, err := NewStorageVault(*rp.StorageVault, rp.ID, limitUpload, limitDownload)
		if err != nil {
			s.logger.Error("NewStorageVault error", zap.Error(err))
			errCh <- err
			return
		}

		// Scan list backup failed
		s.logger.Sugar().Info("Scan list backup failed")
		listBackupFailed, errScanListBackupFailed := scanListBackupFailed()
		if errScanListBackupFailed != nil {
			s.logger.Error("Err scan list backup failed", zap.Error(errScanListBackupFailed))
			errCh <- errScanListBackupFailed
			return
		}

		if listBackupFailed != nil {
			// Upload list backup failed to storage
			s.logger.Sugar().Info("Upload list backup failed to storage")
			errUploadListBackupFailed := s.uploadListBackupFailed(listBackupFailed, storageVault)
			if errUploadListBackupFailed != nil {
				errCh <- errUploadListBackupFailed
				return
			}
		}

		s.notifyMsg(map[string]string{
			"action_id": rp.ID,
			"status":    statusUploadFile,
		})
		rpID := rp.RecoveryPoint.ID
		progressScan := s.newProgressScanDir(rpID)

		index := cache.NewIndex(bd.ID, rp.RecoveryPoint.ID)
		chunks := cache.NewChunk(bd.ID, rp.RecoveryPoint.ID)
		itemTodo, totalFiles, err := WalkerDir(bd.Path, index, progressScan)
		if err != nil {
			s.notifyStatusFailed(rp.ID, err.Error())
			s.logger.Error("WalkerDir error", zap.Error(err))
			errCh <- err
			return
		}
		cacheWriter, err := cache.NewRepository(".cache", rp.RecoveryPoint.ID)
		if err != nil {
			errCh <- err
			return
		}

		if lrp != nil {
			// Store index
			errStoreIndexs := s.storeIndexs(lrp, storageVault)
			if errStoreIndexs != nil {
				s.notifyStatusFailed(rp.ID, errStoreIndexs.Error())
				errCh <- errStoreIndexs
				return
			}
		}

		latestIndex := cache.Index{}
		if lrp != nil {
			buf, err := ioutil.ReadFile(filepath.Join(CACHE_PATH, lrp.ID, "index.json"))
			if err != nil {
				lrp = nil
			} else {
				_ = json.Unmarshal([]byte(buf), &latestIndex)
			}
		}

		var storageSize uint64
		var errFileWorker error
		progressUpload := s.newUploadProgress(rpID, itemTodo)
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		var wg sync.WaitGroup
		for _, itemInfo := range index.Items {
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
				_ = s.pool.Submit(s.uploadFileWorker(ctx, itemInfo, lastInfo, cacheWriter, chunks, storageVault, &wg, &storageSize, &errFileWorker, progressUpload))
			}
		}
		wg.Wait()

		// Save chunks
		errSaveChunks := s.backupClient.SaveChunks(cacheWriter, chunks)
		if errSaveChunks != nil {
			s.notifyStatusFailed(rp.ID, errSaveChunks.Error())
			errCh <- errSaveChunks
			return
		}

		// Store files
		errWriterCSV := s.storeFiles(rp.RecoveryPoint.ID, index, storageVault)
		if errWriterCSV != nil {
			s.notifyStatusFailed(rp.ID, errWriterCSV.Error())
			errCh <- errWriterCSV
			return
		}

		var chunkFailedPath, fileFailedPath string
		var errCopyChunk, errCopyFile error
		if errFileWorker != nil {
			// Copy chunk.json backup failed to /backup_failed/<rp_id>/chunk.json
			s.logger.Sugar().Info("Copy chunk.json backup failed to /backup_failed/<rp_id>/chunk.json")
			chunkFailedPath, errCopyChunk = copyCache(rp.RecoveryPoint.ID, "chunk.json")
			if errCopyChunk != nil {
				errCh <- errCopyChunk
				return
			}

			// Copy file.csv backup failed to /backup_failed/<rp_id>/file.csv
			s.logger.Sugar().Info("Copy file.csv backup failed to /backup_failed/<rp_id>/file.csv")
			fileFailedPath, errCopyFile = copyCache(rp.RecoveryPoint.ID, "file.csv")
			if errCopyFile != nil {
				errCh <- errCopyFile
				return
			}
		}

		// Put chunks
		errPutChunks := s.putChunks(rp.RecoveryPoint.ID, chunkFailedPath, storageVault)
		if errPutChunks != nil {
			s.notifyStatusFailed(rp.ID, errPutChunks.Error())
			errCh <- errPutChunks
			return
		}

		// Put file.csv
		errPutFiles := s.putFiles(rp.RecoveryPoint.ID, fileFailedPath, storageVault)
		if errPutFiles != nil {
			s.notifyStatusFailed(rp.ID, errPutFiles.Error())
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
				s.notifyStatusFailed(rp.ID, err.Error())
			} else {
				s.notifyStatusFailed(rp.ID, errFileWorker.Error())
			}
			s.logger.Error("Error uploadFileWorker error", zap.Error(errFileWorker))
			progressUpload.Done()
			errCh <- errFileWorker
			return
		}

		// Save Indexs
		err = cacheWriter.SaveIndex(index)
		if err != nil {
			s.notifyStatusFailed(rp.ID, err.Error())
			errCh <- err
			return
		}

		// Put indexs
		indexHash, errPutIndexs := s.putIndexs(storageVault, latestIndex, rp.RecoveryPoint.ID)
		if errPutIndexs != nil {
			s.notifyStatusFailed(rp.ID, errPutIndexs.Error())
			errCh <- errPutIndexs
			return
		}
		if lrp != nil {
			err := os.RemoveAll(filepath.Join(CACHE_PATH, lrp.ID))
			if err != nil {
				errCh <- err
				return
			}
		}

		s.reportUploadCompleted(progressOutput)
		progressUpload.Done()
		s.notifyMsg(map[string]string{
			"action_id":    rp.ID,
			"status":       statusComplete,
			"index_hash":   indexHash,
			"storage_size": strconv.FormatUint(storageSize, 10),
			"total":        strconv.FormatUint(itemTodo.Bytes, 10),
			"total_files":  strconv.Itoa(int(totalFiles)),
		})

		errCh <- nil
	}
}

func (s *Server) storeIndexs(lrp *backupapi.RecoveryPointResponse, storageVault storage_vault.StorageVault) error {
	_, err := os.Stat(filepath.Join(CACHE_PATH, lrp.ID, "index.json"))
	if err != nil {
		if os.IsNotExist(err) {
			buf, err := storageVault.GetObject(filepath.Join(lrp.ID, "index.json"))
			if err == nil {
				_ = os.MkdirAll(filepath.Join(CACHE_PATH, lrp.ID), 0700)
				if err := ioutil.WriteFile(filepath.Join(CACHE_PATH, lrp.ID, "index.json"), buf, 0644); err != nil {
					return err
				}
			} else {
				lrp = nil
			}
		} else {
			return err
		}
	} else {
		buf, err := ioutil.ReadFile(filepath.Join(CACHE_PATH, lrp.ID, "index.json"))
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

func (s *Server) putIndexs(storageVault storage_vault.StorageVault, latestIndex cache.Index, rpID string) (string, error) {
	buf, err := ioutil.ReadFile(filepath.Join(".cache", rpID, "index.json"))
	if err != nil {
		s.logger.Error("Read indexs error", zap.Error(err))
		return "", err
	}
	err = storageVault.PutObject(filepath.Join(rpID, "index.json"), buf)
	if err != nil {
		s.logger.Error("Put indexs to storage error", zap.Error(err))
		os.RemoveAll(filepath.Join(CACHE_PATH, rpID))
		return "", err
	}
	hash := sha256.Sum256(buf)
	indexHash := hex.EncodeToString(hash[:])

	return indexHash, nil
}

func (s *Server) putChunks(rpID, chunkPath string, storageVault storage_vault.StorageVault) error {
	if chunkPath == "" {
		chunkPath = filepath.Join(CACHE_PATH, rpID, "chunk.json")
	} else {
		chunkPath = filepath.Join(BACKUP_FAILED_PATH, rpID, "chunk.json")
	}
	buf, err := ioutil.ReadFile(chunkPath)
	if err != nil {
		s.logger.Error("Read chunk.json error", zap.Error(err))
		return err
	}
	err = storageVault.PutObject(filepath.Join(rpID, "chunk.json"), buf)
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

func (s *Server) storeFiles(rpID string, index *cache.Index, storageVault storage_vault.StorageVault) error {
	if _, err := os.Stat(filepath.Dir(filepath.Join(CACHE_PATH, rpID, "file.csv"))); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(CACHE_PATH, rpID, "file.csv")), 0700); err != nil {
			s.logger.Error("Err make dir dir file.csv", zap.Error(err))
			return err
		}
	}
	file, err := os.Create(filepath.Join(CACHE_PATH, rpID, "file.csv"))
	if err != nil {
		s.logger.Error("Err Create file.csv", zap.Error(err))
		return err
	}
	defer file.Close()
	writerCSV := csv.NewWriter(file)
	defer writerCSV.Flush()
	for _, itemInfo := range index.Items {
		if itemInfo.Type == "file" {
			err := writerCSV.Write([]string{itemInfo.Name, itemInfo.Sha256Hash.String(), itemInfo.AbsolutePath})
			if err != nil {
				s.logger.Error("Err writer file.csv", zap.Error(err))
				return err
			}
		}
	}
	return nil
}

func (s *Server) putFiles(rpID string, filePath string, storageVault storage_vault.StorageVault) error {
	if filePath == "" {
		filePath = filepath.Join(CACHE_PATH, rpID, "file.csv")
	} else {
		filePath = filepath.Join(BACKUP_FAILED_PATH, rpID, "file.csv")
	}
	buf, err := ioutil.ReadFile(filePath)
	if err != nil {
		s.logger.Error("Read file.csv error", zap.Error(err))
		return err
	}
	err = storageVault.PutObject(filepath.Join(rpID, "file.csv"), buf)
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
	p := progress.NewProgress(time.Second * 2)

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
				"duration":     formatDuration(d),
				"percent":      formatPercent(stat.Bytes, todo.Bytes),
				"speed":        formatBytes(bps),
				"total":        fmt.Sprintf("%s/%s", formatBytes(stat.Bytes), formatBytes(todo.Bytes)),
				"push_storage": formatBytes(stat.Storage),
				"items":        fmt.Sprintf("%s/%s", strItemsDone, strItemsTodo),
				"erros":        strconv.FormatBool(stat.Errors),
				"eta":          formatSeconds(eta),
			})
		}
	}

	p.OnDone = func(stat progress.Stat, d time.Duration, ticker bool) {
		message := fmt.Sprintf("Duration: %s, %s", d, formatBytes(todo.Storage))
		s.notifyMsgProgress(recoveryPointID, map[string]string{
			"COMPLETE UPLOAD": message,
		})
	}
	return p
}

func (s *Server) newDownloadProgress(recoveryPointID string, todo progress.Stat) *progress.Progress {
	p := progress.NewProgress(time.Second * 2)

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
				"duration":     formatDuration(d),
				"percent":      formatPercent(stat.Bytes, todo.Bytes),
				"speed":        formatBytes(bps),
				"total":        fmt.Sprintf("%s/%s", formatBytes(stat.Bytes), formatBytes(todo.Bytes)),
				"pull_storage": formatBytes(stat.Storage),
				"items":        fmt.Sprintf("%s/%s", strItemsDone, strItemsTodo),
				"erros":        strconv.FormatBool(stat.Errors),
				"eta":          formatSeconds(eta),
			})
		}
	}

	p.OnDone = func(stat progress.Stat, d time.Duration, ticker bool) {
		message := fmt.Sprintf("Duration: %s, %s", d, formatBytes(todo.Storage))
		s.notifyMsgProgress(recoveryPointID, map[string]string{
			"COMPLETE DOWNLOAD": message,
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
func copyCache(rpID, fileName string) (string, error) {
	src := filepath.Join(CACHE_PATH, rpID, fileName)
	dst := filepath.Join(BACKUP_FAILED_PATH, rpID, fileName)
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
	for _, file := range dirEntries {
		dirRP := filepath.Join(BACKUP_FAILED_PATH, file.Name())
		fi, err := os.ReadDir(dirRP)
		if err != nil {
			return nil, err
		}
		for _, f := range fi {
			nameFile := filepath.Join(file.Name(), f.Name())
			listBackupFailed = append(listBackupFailed, nameFile)
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
