package mqtt

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bizflycloud/bizfly-backup/pkg/broker"
	"github.com/bizflycloud/bizfly-backup/pkg/testlib"
)

func TestMQTT(t *testing.T) {
	topics := []string{"1", "2", "3"}
	done := make(chan struct{}, 1)
	mqttUrl := testlib.MqttUrl()
	sub, err := NewBroker(WithURL(mqttUrl), WithClientID("sub"))
	require.NoError(t, err)
	require.NotNil(t, sub)
	assert.NoError(t, sub.Connect())

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

	pub, err := NewBroker(WithURL(mqttUrl), WithClientID("pub"))
	require.NoError(t, err)
	require.NotNil(t, pub)
	assert.NoError(t, pub.Connect())

	for _, topic := range topics {
		assert.NoError(t, pub.Publish(topic, topic))
	}

	<-done
}
