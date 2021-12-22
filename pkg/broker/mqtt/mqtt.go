package mqtt

import (
	"errors"
	"fmt"
	"net/url"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"go.uber.org/zap"

	"github.com/bizflycloud/bizfly-backup/pkg/broker"
)

const (
	clientDisconnectWaitTimeout = 250
	lastWillTestatement         = `{"status": "OFFLINE"}`
)

var _ broker.Broker = (*MQTTBroker)(nil)

var ErrNoConnection = errors.New("no connection to broker server")

var tokenWaitTimeout = 3 * time.Second

// MQTTBroker implements broker.Broker interface.
type MQTTBroker struct {
	uri      *url.URL
	username string
	password string
	clientID string
	client   mqtt.Client
	qos      byte
	retained bool
	logger   *zap.Logger
}

// NewBroker creates new mqtt broker.
func NewBroker(opts ...Option) (*MQTTBroker, error) {
	m := &MQTTBroker{}
	for _, opt := range opts {
		if err := opt(m); err != nil {
			return nil, err
		}
	}
	if m.logger == nil {
		l, err := zap.NewDevelopment()
		if err != nil {
			return nil, err
		}
		m.logger = l
	}
	m.qos = 1
	return m, nil
}

func (m *MQTTBroker) opts() *mqtt.ClientOptions {
	opts := mqtt.NewClientOptions()
	opts.AddBroker("tcp://" + m.uri.Host)
	username := m.username
	if u := m.uri.User.Username(); u != "" {
		username = u
	}
	opts.SetUsername(username)
	password := m.password
	if p, isSet := m.uri.User.Password(); isSet {
		password = p
	}
	opts.SetPassword(password)
	opts.SetClientID(m.clientID)
	opts.SetCleanSession(false)
	opts.SetWill("agent/"+m.clientID, lastWillTestatement, 0, false)
	return opts
}

func (m *MQTTBroker) Connect() error {
	client := mqtt.NewClient(m.opts())
	token := client.Connect()
	for !token.WaitTimeout(tokenWaitTimeout) {
	}
	m.client = client
	return token.Error()
}

func (m *MQTTBroker) Disconnect() error {
	if m.client == nil {
		return ErrNoConnection
	}

	m.client.Disconnect(clientDisconnectWaitTimeout)

	return nil
}

func (m *MQTTBroker) Publish(topic string, payload interface{}) error {
	if m.client == nil {
		return ErrNoConnection
	}
	token := m.client.Publish(topic, m.qos, m.retained, payload)
	for !token.WaitTimeout(tokenWaitTimeout) {
	}

	return token.Error()
}

func (m *MQTTBroker) Subscribe(topics []string, h broker.Handler) error {
	if m.client == nil {
		return ErrNoConnection
	}
	if len(topics) == 0 {
		return errors.New("no topics provided")
	}
	filters := make(map[string]byte, len(topics))
	for _, topic := range topics {
		filters[topic] = m.qos
	}

	token := m.client.SubscribeMultiple(filters, func(client mqtt.Client, msg mqtt.Message) {
		if err := h(broker.Event{
			Topic:     msg.Topic(),
			Payload:   msg.Payload(),
			Duplicate: msg.Duplicate(),
			Qos:       msg.Qos(),
			Retained:  msg.Retained(),
			Ack:       msg.Ack,
		}); err != nil {
			m.logger.Error(err.Error())
		}
	})
	for !token.WaitTimeout(tokenWaitTimeout) {
	}

	return token.Error()
}

func (m *MQTTBroker) String() string {
	return fmt.Sprintf("Broker [%s]", m.clientID)
}
