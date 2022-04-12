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

	// Option for resubscribe when OnConnect
	subscribeTopics  []string
	subscribeHandler broker.Handler
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

	var connectHandler mqtt.OnConnectHandler = func(client mqtt.Client) {
		m.logger.Info("Connected to broker")

		// resubscribe when connected or reconnected with broker
		if m.subscribeHandler != nil && m.subscribeTopics != nil {
			if err := m.Subscribe(m.subscribeTopics, m.subscribeHandler); err != nil {
				m.logger.Error("Subscribe to subscribeTopics return error", zap.Error(err), zap.Strings("subscribeTopics", m.subscribeTopics))
			}
			m.logger.Sugar().Debugf("Agent subscribe to topic %s successful", m.subscribeTopics)
		}
	}

	var connectLostHandler mqtt.ConnectionLostHandler = func(client mqtt.Client, err error) {
		m.logger.Error("Connection lost with broker: ", zap.Error(err))
	}

	var reconnectHandler mqtt.ReconnectHandler = func(client mqtt.Client, opts *mqtt.ClientOptions) {
		m.logger.Error("Trying reconnect with broker")
	}

	opts.OnConnectionLost = connectLostHandler
	opts.OnReconnecting = reconnectHandler
	opts.OnConnect = connectHandler

	opts.SetWill("agent/"+m.clientID, lastWillTestatement, 0, false)
	return opts
}

// connect and update option to auto resubscribe with option OnConnect
func (m *MQTTBroker) ConnectAndSubscribe(subHandler broker.Handler, subTopics []string) error {
	// update subscribe option
	m.subscribeHandler = subHandler
	m.subscribeTopics = subTopics

	return m.Connect()
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
