package mqtt

import (
	"fmt"
	"os"
	"testing"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/ory/dockertest/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bizflycloud/bizfly-backup/pkg/broker"
)

var (
	sub     broker.Broker
	mqttURL string
)

func testMQTT(t *testing.T) {
	topics := []string{"1", "2", "3"}
	done := make(chan struct{}, 1)

	go func() {
		count := 0
		require.NoError(t, sub.Subscribe(topics, func(e broker.Event) error {
			t.Logf("%#v\n", e)
			count++
			if count == len(topics) {
				close(done)
			}
			return nil
		}))
	}()

	pub, err := NewBroker(WithURL(mqttURL), WithClientID("pub"))
	require.NoError(t, err)
	require.NotNil(t, pub)
	assert.NoError(t, pub.Connect())

	for _, topic := range topics {
		assert.NoError(t, pub.Publish(topic, topic))
	}

	<-done
}

func TestMQTT(t *testing.T) {
	if os.Getenv("EXCLUDE_MQTT") != "" {
		return
	}

	runWithVerneMQDockerImage(
		"vernemq/vernemq",
		"latest-alpine",
		[]string{"DOCKER_VERNEMQ_USER_foo=bar", "DOCKER_VERNEMQ_ACCEPT_EULA=yes"},
		testMQTT,
		t,
	)
}

func runWithVerneMQDockerImage(repo, tag string, env []string, testFunc func(t *testing.T), t *testing.T) {
	pool, err := dockertest.NewPool("")
	if err != nil {
		t.Fatalf("Could not connect to docker: %s", err)
	}
	resource, err := pool.Run(repo, tag, env)
	if err != nil {
		t.Fatalf("Could not start resource: %s", err)
	}

	defer func() {
		if err := pool.Purge(resource); err != nil {
			t.Fatalf("Could not purge resource: %s", err)
		}
	}()

	mqttURL = fmt.Sprintf("mqtt://foo:bar@%s", resource.GetHostPort("1883/tcp"))
	if err := pool.Retry(func() error {
		var err error
		sub, err = NewBroker(WithURL(mqttURL), WithClientID("sub"))
		if err != nil {
			return err
		}
		return sub.Connect()
	}); err != nil {
		t.Fatalf("Could not connect to docker: %s", err)
	}

	testFunc(t)
}

func Test_mqttBroker_opts(t *testing.T) {
	defaultBroker, _ := NewBroker(WithURL("mqtt://localhost:1883"))
	brokerWithAuthinURI, _ := NewBroker(
		WithURL("mqtt://foo:bar@localhost:1883"),
		WithUsername("username"),
		WithPassword("password"),
	)
	brokerWithAuth, _ := NewBroker(
		WithURL("mqtt://localhost:1883"),
		WithUsername("username"),
		WithPassword("password"),
	)

	tests := []struct {
		name       string
		m          *MQTTBroker
		assertFunc func(options *mqtt.ClientOptions) bool
	}{
		{
			"default",
			defaultBroker,
			func(opts *mqtt.ClientOptions) bool {
				return len(opts.Servers) == 1 && opts.Username == "" && opts.Password == ""
			},
		},
		{
			"auth info in url",
			brokerWithAuthinURI,
			func(opts *mqtt.ClientOptions) bool {
				return len(opts.Servers) == 1 && opts.Username == "foo" && opts.Password == "bar"
			},
		},
		{
			"auth info from MQTTBroker",
			brokerWithAuth,
			func(opts *mqtt.ClientOptions) bool {
				return len(opts.Servers) == 1 && opts.Username == "username" && opts.Password == "password"
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.True(t, tc.assertFunc(tc.m.opts()))
		})
	}
}
