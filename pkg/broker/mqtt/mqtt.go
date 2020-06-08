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

const clientDisconnectWaitTimeout = 250

var _ broker.Broker = (*mqttBroker)(nil)

var ErrNoConnection = errors.New("no connection to broker server")

var tokenWaitTimeout = 3 * time.Second

type mqttBroker struct {
	uri      *url.URL
	clientID string
	client   mqtt.Client
	qos      byte
	retained bool
	logger   *zap.Logger
}

// NewBroker creates new mqtt broker.
func NewBroker(opts ...Option) (broker.Broker, error) {
	m := &mqttBroker{}
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
	return m, nil
}

func (m *mqttBroker) Connect() error {
	opts := mqtt.NewClientOptions()
	opts.AddBroker("tcp://" + m.uri.Host)
	opts.SetUsername(m.uri.User.Username())
	password, _ := m.uri.User.Password()
	opts.SetPassword(password)
	opts.SetClientID(m.clientID)
	opts.SetCleanSession(false)

	client := mqtt.NewClient(opts)
	token := client.Connect()
	for !token.WaitTimeout(tokenWaitTimeout) {
	}
	m.client = client
	return token.Error()
}

func (m *mqttBroker) Disconnect() error {
	if m.client == nil {
		return ErrNoConnection
	}

	m.client.Disconnect(clientDisconnectWaitTimeout)

	return nil
}

func (m *mqttBroker) Publish(topic string, payload interface{}) error {
	if m.client == nil {
		return ErrNoConnection
	}
	token := m.client.Publish(topic, m.qos, m.retained, payload)
	for !token.WaitTimeout(tokenWaitTimeout) {
	}

	return token.Error()
}

func (m *mqttBroker) Subscribe(topics []string, h broker.Handler) error {
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

func (m *mqttBroker) String() string {
	return fmt.Sprintf("Broker [%s]", m.clientID)
}
