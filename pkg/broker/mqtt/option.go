package mqtt

import (
	"errors"
	"net/url"

	"go.uber.org/zap"
)

type Option func(m *MQTTBroker) error

// WithURL returns an Option which set the broker url.
func WithURL(u string) Option {
	return func(m *MQTTBroker) error {
		if u == "" {
			return errors.New("empty broker url")
		}
		uri, err := url.Parse(u)
		if err != nil {
			return err
		}
		m.uri = uri
		return nil
	}
}

// WithClientID returns an Option which set the broker client id.
func WithClientID(id string) Option {
	return func(m *MQTTBroker) error {
		m.clientID = id
		return nil
	}
}

// WithUsername returns an Option which set the username use to connect to server.
func WithUsername(username string) Option {
	return func(m *MQTTBroker) error {
		m.username = username
		return nil
	}
}

// WithPassword returns an Option which set the password use to connect to server.
func WithPassword(password string) Option {
	return func(m *MQTTBroker) error {
		m.password = password
		return nil
	}
}

// WithLogger returns an Option which set the logger to use when connect to server.
func WithLogger(logger *zap.Logger) Option {
	return func(m *MQTTBroker) error {
		m.logger = logger
		return nil
	}
}
