package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
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
	"github.com/google/uuid"
	"github.com/inconshreveable/go-update"
	"github.com/jpillora/backoff"
	"github.com/panjf2000/ants/v2"
	"github.com/robfig/cron/v3"
	"go.uber.org/zap"
	"golang.org/x/mod/semver"

	"github.com/bizflycloud/bizfly-backup/pkg/backupapi"
	"github.com/bizflycloud/bizfly-backup/pkg/broker"
	"github.com/bizflycloud/bizfly-backup/pkg/progress"
	"github.com/bizflycloud/bizfly-backup/pkg/volume"
	"github.com/bizflycloud/bizfly-backup/pkg/volume/s3"
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

// Server defines parameters for running BizFly Backup HTTP server.
type Server struct {
	Addr            string
	router          *chi.Mux
	b               broker.Broker
	subscribeTopics []string
	publishTopic    string
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
		l := backupapi.WriteLog()
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
	var msg broker.Message
	if err := json.Unmarshal(e.Payload, &msg); err != nil {
		return err
	}
	s.logger.Debug("Got broker event", zap.String("event_type", msg.EventType))
	switch msg.EventType {
	case broker.BackupManual:
		go s.backup(msg.BackupDirectoryID, msg.PolicyID, msg.Name, backupapi.RecoveryPointTypeInitialReplica, ioutil.Discard)
		return nil
	case broker.RestoreManual:
		go s.restore(msg.ActionId, msg.CreatedAt, msg.RestoreSessionKey, msg.RecoveryPointID, msg.DestinationDirectory, msg.VolumeId, ioutil.Discard)
		return nil
	case broker.ConfigUpdate:
		return s.handleConfigUpdate(msg.Action, msg.BackupDirectories)
	case broker.ConfigRefresh:
		return s.handleConfigRefresh(msg.BackupDirectories)
	case broker.AgentUpgrade:
	case broker.StatusNotify:
		s.logger.Info("Got agent status", zap.String("status", msg.Status))
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
			entryID, err := s.cronManager.AddFunc(policy.SchedulePattern, func() {
				name := "auto-" + time.Now().Format(time.RFC3339)
				// improve when support incremental backup
				recoveryPointType := backupapi.RecoveryPointTypeInitialReplica
				if err := s.backup(directoryID, policyID, name, recoveryPointType, ioutil.Discard); err != nil {
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
	if err := s.b.Publish(s.publishTopic, payload); err != nil {
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

func (s *Server) notifyMsg(msg map[string]string) {
	payload, _ := json.Marshal(msg)
	if err := s.b.Publish(s.publishTopic, payload); err != nil {
		s.logger.Warn("failed to notify server", zap.Error(err), zap.Any("message", msg))
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
func (s *Server) backup(backupDirectoryID string, policyID string, name string, recoveryPointType string, progressOutput io.Writer) error {
	chErr := make(chan error, 1)
	_ = s.poolDir.Submit(s.backupWorker(backupDirectoryID, policyID, name, recoveryPointType, progressOutput, chErr))
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

func (s *Server) reportDownloadCompleted(w io.Writer) {
	_, _ = w.Write([]byte("Download completed."))
}

func (s *Server) reportStartRestore(w io.Writer) {
	_, _ = w.Write([]byte("Start restoring ..."))
}

func (s *Server) reportRestoreCompleted(w io.Writer) {
	_, _ = w.Write([]byte("Restore completed."))
}

func (s *Server) restore(actionID string, createdAt string, restoreSessionKey string, recoveryPointID string, destDir string, volumeId string, progressOutput io.Writer) error {
	// Get storage volume

	restoreKey := &backupapi.AuthRestore{
		RecoveryPointID:   recoveryPointID,
		ActionID:          actionID,
		CreatedAt:         createdAt,
		RestoreSessionKey: restoreSessionKey,
	}

	vol, err := s.backupClient.GetCredentialVolume(volumeId, actionID, restoreKey)
	if err != nil {
		s.logger.Error("Get credential volume error", zap.Error(err))
		return err
	}
	storageVolume, _ := NewStorageVolume(*vol, actionID)

	s.notifyMsg(map[string]string{
		"action_id": actionID,
		"status":    statusDownloading,
	})

	s.reportStartDownload(progressOutput)

	if err := s.backupClient.RestoreDirectory(recoveryPointID, destDir, storageVolume, restoreKey); err != nil {
		s.logger.Error("failed to download file", zap.Error(err))
		s.notifyStatusFailed(actionID, err.Error())
		return err
	}

	s.reportDownloadCompleted(progressOutput)
	s.reportStartRestore(progressOutput)
	s.reportRestoreCompleted(progressOutput)
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

func NewStorageVolume(vol backupapi.Volume, actionID string) (volume.StorageVolume, error) {
	switch vol.VolumeType {
	case "S3":
		return s3.NewS3Default(vol, actionID), nil
	default:
		return nil, fmt.Errorf(fmt.Sprintf("volume type not supported %s", vol.VolumeType))
	}
}

func WalkerDir(dir string, p *progress.Progress) (progress.Stat, *backupapi.FileInfoRequest, error) {
	//func WalkerDir(dir string) (uint64, *backupapi.FileInfoRequest, error) {
	p.Start()
	defer p.Done()

	var fileInfoRequest backupapi.FileInfoRequest
	//var total uint64
	var st progress.Stat
	err := filepath.Walk(dir, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		s := progress.Stat{
			Items: 1,
			Bytes: uint64(fi.Size()),
		}

		singleFile := backupapi.ItemInfo{
			ParentItemID:   "",
			ChunkReference: false,
			Attributes: &backupapi.Attribute{
				ID:          uuid.New().String(),
				ItemName:    path,
				ModifyTime:  fi.ModTime().UTC(),
				Mode:        fi.Mode().String(),
				AccessMode:  fi.Mode(),
				Size:        fi.Size(),
				SymlinkPath: "",
			},
		}

		atimeLocal, ctimeLocal, _, uid, gid := backupapi.ItemLocal(fi)
		singleFile.Attributes.AccessTime = atimeLocal
		singleFile.Attributes.ChangeTime = ctimeLocal
		singleFile.Attributes.UID = uid
		singleFile.Attributes.GID = gid

		if fi.IsDir() {
			singleFile.ItemType = "DIRECTORY"
			singleFile.Attributes.ItemType = "DIRECTORY"
			singleFile.Attributes.IsDir = true
			singleFile.ChunkReference = false
		} else if fi.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			singleFile.ItemType = "SYMLINK"
			singleFile.Attributes.ItemType = "SYMLINK"
			singleFile.Attributes.SymlinkPath = link
			singleFile.ChunkReference = false
		} else {
			singleFile.ItemType = "FILE"
			singleFile.Attributes.ItemType = "FILE"
			singleFile.Attributes.IsDir = false
			singleFile.ChunkReference = true
		}

		fileInfoRequest.Files = append(fileInfoRequest.Files, singleFile)
		p.Report(s)
		st.Add(s)
		return nil
	})
	if err != nil {
		return progress.Stat{}, nil, err
	}

	return st, &fileInfoRequest, err
}

type backupJob func()

func (s *Server) uploadFileWorker(ctx context.Context, recoveryPointID string, actionID string, latestRecoveryPointID string,
	itemInfo backupapi.ItemInfo, volume volume.StorageVolume, wg *sync.WaitGroup, size *uint64, errCh *error, p *progress.Progress) backupJob {
	return func() {
		defer wg.Done()
		select {
		case <-ctx.Done():
			return
		default:
			p.Start()
			ctx, cancel := context.WithCancel(ctx)
			s.logger.Info("Upload file worker: ", zap.String("recoveryPointID", recoveryPointID), zap.String("actionID", actionID), zap.String("item name", itemInfo.Attributes.ItemName))
			storageSize, err := s.backupClient.UploadFile(ctx, s.chunkPool, recoveryPointID, actionID, latestRecoveryPointID, itemInfo, volume, p)
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

func (s *Server) backupWorker(backupDirectoryID string, policyID string, name string, recoveryPointType string, progressOutput io.Writer, errCh chan<- error) backupJob {
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

		// Get storage volume
		storageVolume, err := NewStorageVolume(*rp.Volume, rp.ID)
		if err != nil {
			s.logger.Error("NewStorageVolume error", zap.Error(err))
			errCh <- err
			return
		}

		s.notifyMsg(map[string]string{
			"action_id": rp.ID,
			"status":    statusUploadFile,
		})
		progressScan := s.newProgressScanDir()
		itemTodo, itemsInfo, err := WalkerDir(bd.Path, progressScan)
		if err != nil {
			s.notifyStatusFailed(rp.ID, err.Error())
			s.logger.Error("WalkerDir error", zap.Error(err))
			errCh <- err
			return
		}

		var storageSize uint64
		var errFileWorker error
		progressUpload := s.newUploadProgress(itemTodo)
		ctx, cancel := context.WithCancel(ctx)
		var wg sync.WaitGroup
		for _, itemInfo := range itemsInfo.Files {
			if errFileWorker != nil {
				s.logger.Error("uploadFileWorker error", zap.Error(errFileWorker))
				cancel()
				break
			}
			wg.Add(1)
			_ = s.pool.Submit(s.uploadFileWorker(ctx, rp.RecoveryPoint.ID, rp.ID, lrp.ID, itemInfo, storageVolume, &wg, &storageSize, &errFileWorker, progressUpload))
		}
		wg.Wait()

		if errFileWorker != nil {
			s.notifyStatusFailed(rp.ID, errFileWorker.Error())
			s.logger.Error("Error uploadFileWorker error", zap.Error(errFileWorker))
			progressUpload.Done()
			errCh <- errFileWorker
			return
		}

		s.reportUploadCompleted(progressOutput)
		progressUpload.Done()
		s.notifyMsg(map[string]string{
			"action_id":    rp.ID,
			"status":       statusComplete,
			"storage_size": strconv.FormatUint(storageSize, 10),
			"total":        strconv.FormatUint(itemTodo.Bytes, 10),
		})

		errCh <- nil
		return
	}
}

func (s *Server) newProgressScanDir() *progress.Progress {
	p := progress.NewProgress(time.Second)
	p.OnUpdate = func(stat progress.Stat, d time.Duration, ticker bool) {
		s.notifyMsg(map[string]string{
			"STATISTIC": stat.String(),
		})
	}
	p.OnDone = func(stat progress.Stat, d time.Duration, ticker bool) {
		s.notifyMsg(map[string]string{
			"SCANNED": stat.String(),
		})
	}
	return p
}

func (s *Server) newUploadProgress(todo progress.Stat) *progress.Progress {
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

			status1 := fmt.Sprintf("[Duration %s] %s [speed:%s/s] [%s/%s (Total)] [%s put storage] [%d/%d items] %t erros ",
				formatDuration(d), formatPercent(stat.Bytes, todo.Bytes), formatBytes(bps), formatBytes(stat.Bytes), formatBytes(todo.Bytes),
				formatBytes(stat.Storage), itemsDone, itemsTodo, stat.Errors)
			status2 := fmt.Sprintf("ETA %s", formatSeconds(eta))

			message := fmt.Sprintf("%s %s", status1, status2)
			s.notifyMsg(map[string]string{
				"Uploading": message,
			})
		}
	}

	p.OnDone = func(stat progress.Stat, d time.Duration, ticker bool) {
		message := fmt.Sprintf("Duration: %s, %s", d, formatBytes(todo.Storage))
		s.notifyMsg(map[string]string{
			"COMPLETE UPLOAD": message,
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
