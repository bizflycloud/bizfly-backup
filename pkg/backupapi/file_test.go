package backupapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClient_saveFileInfoPath(t *testing.T) {
	setUp()
	defer tearDown()

	recoveryPointID := "recovery-point-id"
	sfip := client.saveFileInfoPath(recoveryPointID)
	assert.Equal(t, "/agent/recovery-points/recovery-point-id/file", sfip)
}

func TestClient_getItemLatestPath(t *testing.T) {
	setUp()
	defer tearDown()

	latestRecoveryPointID := "latest-recovery-point-id"
	gilp := client.getItemLatestPath(latestRecoveryPointID)
	assert.Equal(t, "/agent/recovery-points/latest-recovery-point-id/path", gilp)
}
