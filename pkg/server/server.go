package server

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi"
	"github.com/go-chi/valve"
	"github.com/jpillora/backoff"
	"github.com/robfig/cron/v3"
	"go.uber.org/zap"

	"github.com/bizflycloud/bizfly-backup/pkg/backupapi"
	"github.com/bizflycloud/bizfly-backup/pkg/broker"
)

const (
	statusZipFile     = "ZIP_FILE"
	statusUploadFile  = "UPLOADING"
	statusComplete    = "COMPLETED"
	statusDownloading = "DOWNLOADING"
	statusRestoring   = "RESTORING"
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
	s.cronManager = cron.New(cron.WithLocation(time.UTC))
	s.cronManager.Start()
	s.mappingToCronEntryID = make(map[string]cron.EntryID)

	if s.logger == nil {
		l, err := zap.NewDevelopment()
		if err != nil {
			return nil, err
		}
		s.logger = l
	}

	s.setupRoutes()
	s.useUnixSock = strings.HasPrefix(s.Addr, "unix://")
	s.Addr = strings.TrimPrefix(s.Addr, "unix://")

	return s, nil
}

func (s *Server) setupRoutes() {
	s.router.Route("/backups", func(r chi.Router) {
		r.Get("/", s.ListBackup)
		r.Post("/", s.Backup)
		r.Get("/{backupID}/recovery-points", s.ListRecoveryPoints)
		r.Post("/sync", s.SyncConfig)
	})

	s.router.Route("/recovery-points", func(r chi.Router) {
		r.Get("/{recoveryPointID}/download", s.DownloadRecoveryPoint)
		r.Post("/{recoveryPointID}/restore", s.Restore)
	})

	s.router.Route("/upgrade", func(r chi.Router) {
		r.Post("/", s.UpgradeAgent)
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
		return s.backup(msg.BackupDirectoryID, msg.PolicyID)
	case broker.RestoreManual:
		return s.restore(msg.RecoveryPointID, msg.DestinationDirectory)
	case broker.ConfigUpdate:
		return s.handleConfigUpdate(msg.Action, msg.BackupDirectories)
	case broker.ConfigRefresh:
		return s.handleConfigRefresh(msg.BackupDirectories)
	case broker.AgentUpgrade:
	default:
		return fmt.Errorf("Event %s: %w", msg.EventType, broker.ErrUnknownEventType)
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
	s.cronManager = cron.New(cron.WithLocation(time.UTC))
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
			entryID, err := s.cronManager.AddFunc(policy.SchedulePattern, func() {
				if err := s.backup(bd.ID, policy.ID); err != nil {
					zapFields := []zap.Field{
						zap.Error(err),
						zap.String("service", "cron"),
						zap.String("backup_directory_id", bd.ID),
						zap.String("policy_id", policy.ID),
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

func (s *Server) Backup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID       string `json:"id"`
		PolicyID string `json:"policy_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`malformed body`))
		return

	}
	if err := s.backup(body.ID, body.PolicyID); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(err.Error()))
	}
}

func (s *Server) ListBackup(w http.ResponseWriter, r *http.Request) {
	c, err := s.backupClient.GetConfig(r.Context())
	if err != nil {
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
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(err.Error()))
		return
	}
	_ = json.NewEncoder(w).Encode(rps)
}

func (s *Server) DownloadRecoveryPoint(w http.ResponseWriter, r *http.Request) {
	recoveryPointID := chi.URLParam(r, "recoveryPointID")
	if err := s.backupClient.DownloadFileContent(r.Context(), recoveryPointID, w); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(err.Error()))
		return
	}
}

func (s *Server) Restore(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Dest string `json:"destination"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`malformed body`))
		return
	}
	recoveryPointID := chi.URLParam(r, "recoveryPointID")
	if err := s.restore(recoveryPointID, body.Dest); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(err.Error()))
	}
}

func (s *Server) SyncConfig(w http.ResponseWriter, r *http.Request) {
	c, err := s.backupClient.GetConfig(r.Context())
	if err != nil {
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

func (s *Server) UpgradeAgent(w http.ResponseWriter, r *http.Request) {}

func (s *Server) subscribeBrokerLoop(ctx context.Context) {
	if len(s.subscribeTopics) == 0 {
		return
	}
	b := &backoff.Backoff{Jitter: true}
	for {
		if err := s.b.Connect(); err == nil {
			break
		}
		time.Sleep(b.Duration())
		continue
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
			return err
		}
		return srv.Serve(unixListener)
	}

	srv.Addr = s.Addr
	return srv.ListenAndServe()
}

// backup performs backup flow.
func (s *Server) backup(backupDirectoryID string, policyID string) error {
	ctx := context.Background()
	// Create recovery point
	rp, err := s.backupClient.CreateRecoveryPoint(ctx, backupDirectoryID, &backupapi.CreateRecoveryPointRequest{PolicyID: policyID})
	if err != nil {
		return err
	}

	// Get BackupDirectory
	bd, err := s.backupClient.GetBackupDirectory(backupDirectoryID)
	if err != nil {
		return err
	}

	msg := map[string]string{
		"action_id": rp.ID,
		"status":    statusZipFile,
	}
	payload, _ := json.Marshal(msg)
	if err := s.b.Publish(s.publishTopic, payload); err != nil {
		s.logger.Warn("failed to notify server before zip file", zap.Error(err))
	}

	wd := filepath.Dir(bd.Path)
	backupDir := filepath.Base(bd.Path)

	if err := os.Chdir(wd); err != nil {
		return err
	}

	// Compress directory
	fi, err := ioutil.TempFile("", "bizfly-backup-agent-backup-*")
	if err != nil {
		return err
	}
	defer os.Remove(fi.Name())
	if err := compressDir(backupDir, fi); err != nil {
		return err
	}
	if err := fi.Close(); err != nil {
		return err
	}

	fi, err = os.Open(fi.Name())
	if err != nil {
		return err
	}

	msg["status"] = statusUploadFile
	payload, _ = json.Marshal(msg)
	if err := s.b.Publish(s.publishTopic, payload); err != nil {
		s.logger.Warn("failed to notify server before upload file", zap.Error(err))
	}
	// Upload file to server
	if err := s.backupClient.UploadFile(rp.RecoveryPoint.ID, fi); err != nil {
		return nil
	}

	msg["status"] = statusComplete
	payload, _ = json.Marshal(msg)
	if err := s.b.Publish(s.publishTopic, payload); err != nil {
		s.logger.Warn("failed to notify server upload file completed", zap.Error(err))
	}

	return nil
}

func (s *Server) restore(recoveryPointID string, destDir string) error {
	ctx := context.Background()

	fi, err := ioutil.TempFile("", "bizfly-backup-agent-restore*")
	if err != nil {
		return err
	}
	defer os.Remove(fi.Name())

	msg := map[string]string{
		"action_id": recoveryPointID,
		"status":    statusDownloading,
	}
	payload, _ := json.Marshal(msg)
	if err := s.b.Publish(s.publishTopic, payload); err != nil {
		s.logger.Warn("failed to notify server before downloading file content", zap.Error(err))
	}

	if err := s.backupClient.DownloadFileContent(ctx, recoveryPointID, fi); err != nil {
		s.logger.Error("failed to download file content", zap.Error(err))
		return err
	}
	if err := fi.Close(); err != nil {
		s.logger.Error("failed to save to temporary file", zap.Error(err))
		return err
	}

	msg["status"] = statusRestoring
	payload, _ = json.Marshal(msg)
	if err := s.b.Publish(s.publishTopic, payload); err != nil {
		s.logger.Warn("failed to notify server before restoring", zap.Error(err))
	}
	if err := unzip(fi.Name(), destDir); err != nil {
		return err
	}

	msg["status"] = statusComplete
	payload, _ = json.Marshal(msg)
	if err := s.b.Publish(s.publishTopic, payload); err != nil {
		s.logger.Warn("failed to notify server restore progress completed", zap.Error(err))
	}

	return nil
}

func compressDir(src string, w io.Writer) error {
	// zip > buf
	zw := zip.NewWriter(w)
	defer zw.Close()

	walker := func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		fi, err := os.Open(path)
		if err != nil {
			return err
		}
		defer fi.Close()

		fw, err := zw.Create(path)
		if err != nil {
			return err
		}

		_, err = io.Copy(fw, fi)
		if err != nil {
			return err
		}

		return nil
	}

	// walk through every file in the folder and add to zip writer.
	if err := filepath.Walk(src, walker); err != nil {
		return err
	}

	if err := zw.Close(); err != nil {
		return err
	}

	return nil
}

func unzip(zipFile, dest string) error {
	r, err := zip.OpenReader(zipFile)
	if err != nil {
		return fmt.Errorf("zip.OpenReader: %w", err)
	}
	defer r.Close()

	if err := os.MkdirAll(dest, 0755); err != nil && !os.IsExist(err) {
		return err
	}

	extractAndWriteFile := func(f *zip.File) error {
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("extractAndWriteFile: f.Open: %w", err)
		}
		defer rc.Close()
		path := filepath.Join(dest, f.Name)

		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(path, f.Mode())
		} else {
			_ = os.MkdirAll(filepath.Dir(path), 0755)
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
			if err != nil {
				return fmt.Errorf("extractAndWriteFile: os.OpenFile: %w", err)
			}
			defer f.Close()

			if _, err := io.Copy(f, rc); err != nil {
				return fmt.Errorf("extractAndWriteFile: io.Copy: %w", err)
			}
			if err := f.Close(); err != nil {
				return fmt.Errorf("extractAndWriteFile: f.Close: %w", err)
			}
		}
		return nil
	}

	for _, f := range r.File {
		if err := extractAndWriteFile(f); err != nil {
			return err
		}
	}

	return nil
}
