package server

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/ory/dockertest/v3"
	"github.com/robfig/cron/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bizflycloud/bizfly-backup/pkg/backupapi"
	"github.com/bizflycloud/bizfly-backup/pkg/broker"
	"github.com/bizflycloud/bizfly-backup/pkg/broker/mqtt"
)

var (
	b       broker.Broker
	topic   = "agent/agent1"
	mqttURL string
)

func TestMain(m *testing.M) {
	if os.Getenv("EXCLUDE_MQTT") != "" {
		os.Exit(0)
	}

	pool, err := dockertest.NewPool("")
	if err != nil {
		log.Fatalf("Could not connect to docker: %s", err)
	}

	resource, err := pool.Run("vernemq/vernemq", "latest-alpine", []string{"DOCKER_VERNEMQ_USER_foo=bar", "DOCKER_VERNEMQ_ACCEPT_EULA=yes"})
	if err != nil {
		log.Fatalf("Could not start resource: %s", err)
	}

	mqttURL = fmt.Sprintf("mqtt://foo:bar@%s", resource.GetHostPort("1883/tcp"))
	if err := pool.Retry(func() error {
		var err error
		b, err = mqtt.NewBroker(mqtt.WithURL(mqttURL), mqtt.WithClientID("sub"))
		if err != nil {
			return err
		}
		return b.Connect()
	}); err != nil {
		log.Fatalf("Could not connect to docker: %s", err)
	}

	code := m.Run()

	if err := pool.Purge(resource); err != nil {
		log.Fatalf("Could not purge resource: %s", err)
	}
	os.Exit(code)
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
	stop := make(chan struct{})
	count := 0

	go func() {
		require.NoError(t, s.b.Subscribe([]string{topic}, func(e broker.Event) error {
			count++
			if count == 2 {
				close(stop)
			}
			return errors.New("unknown event)")
		}))
		close(done)
	}()

	<-done
	pub, err := mqtt.NewBroker(mqtt.WithURL(mqttURL), mqtt.WithClientID("pub"))
	require.NoError(t, err)
	require.NotNil(t, pub)
	assert.NoError(t, pub.Connect())
	assert.NoError(t, pub.Publish(topic, `{"event_type": "test"`))
	assert.NoError(t, pub.Publish(topic, `{"event_type": ""`))
	<-stop
	assert.Equal(t, 2, count)
}

func Test_compressDir(t *testing.T) {
	fi, err := ioutil.TempFile("", "bizfly-backup-agent-test-compress-*")
	require.NoError(t, err)
	defer os.Remove(fi.Name())

	var buf bytes.Buffer
	assert.NoError(t, compressDir("./testdata/test_compress_dir", &buf))

	zipReader, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)

	count := 0
	for _, zipFile := range zipReader.File {
		t.Log(zipFile.Name)
		count++
	}
	assert.Equal(t, 4, count)
}

func Test_unzip(t *testing.T) {
	fi, err := ioutil.TempFile("", "bizfly-backup-agent-test-unzip-*")
	require.NoError(t, err)
	defer os.Remove(fi.Name())

	assert.NoError(t, compressDir("./testdata/test_compress_dir", fi))
	require.NoError(t, fi.Close())

	tempDir, err := ioutil.TempDir("", "bizfly-backup-agent-test-unzip-dir-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	assert.NoError(t, unzip(fi.Name(), tempDir))

	count := 0
	walker := func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || (info.Mode()&os.ModeSymlink != 0) {
			return nil
		}
		count++
		return nil
	}

	assert.NoError(t, filepath.Walk(filepath.Join(tempDir, ""), walker))
	assert.Equal(t, 4, count)
}

func TestServerCron(t *testing.T) {
	tests := []struct {
		name               string
		bdc                []backupapi.BackupDirectoryConfig
		expectedNumEntries int
	}{
		{
			"empty",
			[]backupapi.BackupDirectoryConfig{},
			0,
		},
		{
			"good",
			[]backupapi.BackupDirectoryConfig{
				{
					ID:   "dir1",
					Name: "dir1",
					Path: "/dev/null",
					Policies: []backupapi.BackupDirectoryConfigPolicy{
						{
							ID:              "policy_1",
							Name:            "policy_1",
							SchedulePattern: "* * * * *",
						},
					},
					Activated: true,
				},
				{
					ID:   "dir2",
					Name: "dir2",
					Path: "/dev/zero",
					Policies: []backupapi.BackupDirectoryConfigPolicy{
						{
							ID:              "policy_2",
							Name:            "policy_2",
							SchedulePattern: "* * * * *",
						},
					},
					Activated: true,
				},
			},
			2,
		},
		{
			"activated false",
			[]backupapi.BackupDirectoryConfig{
				{
					ID:   "dir1",
					Name: "dir1",
					Path: "/dev/null",
					Policies: []backupapi.BackupDirectoryConfigPolicy{
						{
							ID:              "policy_1",
							Name:            "policy_1",
							SchedulePattern: "* * * * *",
						},
					},
					Activated: true,
				},
				{
					ID:   "dir2",
					Name: "dir2",
					Path: "/dev/zero",
					Policies: []backupapi.BackupDirectoryConfigPolicy{
						{
							ID:              "policy_2",
							Name:            "policy_2",
							SchedulePattern: "* * * * *",
						},
					},
					Activated: false,
				},
			},
			1,
		},
		{
			"invalid cron pattern",
			[]backupapi.BackupDirectoryConfig{
				{
					ID:   "dir1",
					Name: "dir1",
					Path: "/dev/null",
					Policies: []backupapi.BackupDirectoryConfigPolicy{
						{
							ID:              "policy_1",
							Name:            "policy_1",
							SchedulePattern: "* * * *",
						},
					},
					Activated: true,
				},
			},
			0,
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, err := New()
			require.NoError(t, err)
			s.addToCronManager(tc.bdc)
			assert.Len(t, s.mappingToCronEntryID, tc.expectedNumEntries)
			s.removeFromCronManager(tc.bdc)
			assert.Equal(t, map[string]cron.EntryID{}, s.mappingToCronEntryID)
		})
	}
}
