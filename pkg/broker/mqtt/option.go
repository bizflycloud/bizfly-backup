package mqtt

import "net/url"

type Option func(m *mqttBroker) error

// WithURL returns an Option which set the broker url.
func WithURL(u string) Option {
	return func(m *mqttBroker) error {
		uri, err := url.Parse(u)
		if err != nil {
			return err
		}
		m.uri = uri
		return nil
	}
}

// WitClientID returns an Option which set the broker client id.
func WithClientID(id string) Option {
	return func(m *mqttBroker) error {
		m.clientID = id
		return nil
	}
}
