package server

import (
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bizflycloud/bizfly-backup/pkg/broker"
	"github.com/bizflycloud/bizfly-backup/pkg/broker/mqtt"
	"github.com/bizflycloud/bizfly-backup/pkg/testlib"
)

var (
	b     broker.Broker
	topic = "agent/agent1"
)

func TestMain(m *testing.M) {
	mqttUrl := testlib.MqttUrl()
	var err error
	b, err = mqtt.NewBroker(mqtt.WithURL(mqttUrl), mqtt.WithClientID("sub"))
	if err != nil {
		log.Fatal(err)
	}
	if err := b.Connect(); err != nil {
		log.Fatal(err)
	}
	os.Exit(m.Run())
}

func TestServerRun(t *testing.T) {
	tests := []struct {
		addr string
	}{
		{"unix://" + filepath.Join(os.TempDir(), "bizfly-backup-test-server.sock")},
		{":1810"},
	}
	for _, tc := range tests {
		s, err := New(WithAddr(tc.addr), WithBroker(b))
		require.NoError(t, err)
		s.testSignalCh = make(chan os.Signal, 1)
		var serverError error
		done := make(chan struct{})
		go func() {
			serverError = s.Run()
			close(done)
		}()
		time.Sleep(time.Duration(rand.Intn(1000)) * time.Millisecond)
		s.testSignalCh <- syscall.SIGTERM
		<-done
		assert.IsType(t, http.ErrServerClosed, serverError)
	}
}

func TestServerEventHandler(t *testing.T) {
	addr := "unix://" + filepath.Join(os.TempDir(), "bizfly-backup-test-server.sock")
	s, err := New(WithAddr(addr), WithBroker(b))
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		require.NoError(t, s.b.Subscribe([]string{topic}, s.handleBrokerEvent))
		close(done)
	}()

	<-done
}
