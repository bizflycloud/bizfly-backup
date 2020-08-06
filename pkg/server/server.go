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
	"syscall"
	"time"

	"github.com/go-chi/chi"
	"github.com/go-chi/valve"
	"github.com/jpillora/backoff"
	"go.uber.org/zap"

	"github.com/bizflycloud/bizfly-backup/pkg/backupapi"
	"github.com/bizflycloud/bizfly-backup/pkg/broker"
)

const (
	backupTimeLayout = "20060102150405"
	statusZipFile    = "ZIP_FILE"
	statusUploadFile = "UPLOADING"
	statusComplete   = "COMPLETED"
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
		r.Post("/restore", s.Restore)
	})

	s.router.Route("/cron", func(r chi.Router) {
		s.router.Patch("/{id}", s.UpdateCron)
	})

	s.router.Route("/upgrade", func(r chi.Router) {
		s.router.Post("/", s.UpgradeAgent)
	})
}

func (s *Server) handleBrokerEvent(e broker.Event) error {
	var msg broker.Message
	if err := json.Unmarshal(e.Payload, &msg); err != nil {
		return err
	}
	s.logger.Debug("Got broker event", zap.String("event_type", msg.EventType))
	switch msg.EventType {
	case broker.BackupManual:
		// TODO(cuonglm): fill in actual data later
		return s.backup(0, "")
	case broker.RestoreManual, broker.ConfigUpdate, broker.AgentUpgrade:
	default:
		return fmt.Errorf("Event %s: %w", msg.EventType, broker.ErrUnknownEventType)
	}
	return nil
}

func (s *Server) Backup(w http.ResponseWriter, r *http.Request)       {}
func (s *Server) ListBackup(w http.ResponseWriter, r *http.Request)   {}
func (s *Server) Restore(w http.ResponseWriter, r *http.Request)      {}
func (s *Server) UpdateCron(w http.ResponseWriter, r *http.Request)   {}
func (s *Server) UpgradeAgent(w http.ResponseWriter, r *http.Request) {}

func (s *Server) Run() error {
	// Graceful valve shut-off package to manage code preemption and shutdown signaling.
	valv := valve.New()
	baseCtx := valv.Context()

	go func(ctx context.Context) {
		if len(s.subscribeTopics) == 0 {
			return
		}
		b := &backoff.Backoff{Jitter: true}
		for {
			if err := s.b.Connect(); err != nil {
				time.Sleep(b.Duration())
				continue
			}
			if err := s.b.Subscribe(s.subscribeTopics, s.handleBrokerEvent); err != nil {
				s.logger.Error("Subscribe to subscribeTopics return error", zap.Error(err), zap.Strings("subscribeTopics", s.subscribeTopics))
			}
		}
	}(baseCtx)

	go func(ctx context.Context) {
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
	}(baseCtx)

	srv := http.Server{Handler: chi.ServerBaseContext(baseCtx, s.router)}

	c := make(chan os.Signal, 1)
	if s.testSignalCh != nil {
		c = s.testSignalCh
	}
	signal.Notify(c, syscall.SIGTERM, syscall.SIGINT)
	go func() {
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
	}()

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
func (s *Server) backup(backupDirectoryID int, policyID string) error {
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

	// Compress directory
	fi, err := ioutil.TempFile("", "bizfly-backup-*")
	if err != nil {
		return err
	}
	defer os.Remove(fi.Name())
	if err := compressDir(bd.Path, fi); err != nil {
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
	if err := s.backupClient.UploadFile(bd.Path+"-"+time.Now().UTC().Format(backupTimeLayout), fi); err != nil {
		return nil
	}

	msg["status"] = statusComplete
	payload, _ = json.Marshal(msg)
	if err := s.b.Publish(s.publishTopic, payload); err != nil {
		s.logger.Warn("failed to notify server upload file completed", zap.Error(err))
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
