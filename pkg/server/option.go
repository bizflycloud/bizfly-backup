package server

import (
	"go.uber.org/zap"

	"github.com/bizflycloud/bizfly-backup/pkg/backupapi"
	"github.com/bizflycloud/bizfly-backup/pkg/broker"
)

type Option func(s *Server) error

// WithAddr returns an Option which set the server listening address.
func WithAddr(addr string) Option {
	return func(s *Server) error {
		s.Addr = addr
		return nil
	}
}

// WithBroker returns an Option which set the server broker for async messaging.
func WithBroker(b broker.Broker) Option {
	return func(s *Server) error {
		s.b = b
		return nil
	}
}

// WithSubscribeTopics returns an Option which set the subscribeTopics that server broker will subscribe to.
func WithSubscribeTopics(topics ...string) Option {
	return func(s *Server) error {
		s.subscribeTopics = topics
		return nil
	}
}

// WithPublishTopics returns an Option which set the server topic that server broker will publish message.
func WithPublishTopics(topics ...string) Option {
	return func(s *Server) error {
		s.publishTopics = topics
		return nil
	}
}

// WithBackupClient returns an Option which set the backup client for Server.
func WithBackupClient(c *backupapi.Client) Option {
	return func(s *Server) error {
		s.backupClient = c
		return nil
	}
}

// WithLogger returns an Option which set the logger for Server.
func WithLogger(logger *zap.Logger) Option {
	return func(s *Server) error {
		s.logger = logger
		return nil
	}
}

// WithLogger returns an Option which set the logger for Server.
func WithNumGoroutine(num int) Option {
	return func(s *Server) error {
		s.numGoroutine = num
		return nil
	}
}
