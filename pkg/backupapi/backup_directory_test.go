package backupapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClient_backupDirectoryPath(t *testing.T) {
	setUp()
	defer tearDown()

	id := "id"
	bdp := client.backupDirectoryPath(id)
	assert.Equal(t, "/agent/backup-directories/id", bdp)
}

func TestClient_backupDirectoryActionPath(t *testing.T) {
	setUp()
	defer tearDown()

	id := "id"
	bdap := client.backupDirectoryActionPath(id)
	assert.Equal(t, "/agent/backup-directories/id/action", bdap)
}