package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi"
	"github.com/go-chi/valve"
	"github.com/jpillora/backoff"
	"go.uber.org/zap"

	"github.com/bizflycloud/bizfly-backup/pkg/broker"
)

// Server defines parameters for running BizFly Backup HTTP server.
type Server struct {
	Addr        string
	router      *chi.Mux
	b           broker.Broker
	topics      []string
	useUnixSock bool

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
	switch msg.EventType {
	case broker.BackupManual, broker.RestoreManual, broker.ConfigUpdate, broker.AgentUpgrade:
		s.logger.Debug("Got broker event", zap.String("event_type", msg.EventType))
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
		if len(s.topics) == 0 {
			return
		}
		b := &backoff.Backoff{Jitter: true}
		for {
			if err := s.b.Connect(); err != nil {
				time.Sleep(b.Duration())
				continue
			}
			if err := s.b.Subscribe(s.topics, s.handleBrokerEvent); err != nil {
				s.logger.Error("Subscribe to topics return error", zap.Error(err), zap.Strings("topics", s.topics))
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
