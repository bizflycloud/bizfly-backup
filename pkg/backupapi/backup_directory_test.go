package backupapi

import (
	"encoding/json"
	"net/http"
	"path"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestClient_GetBackupDirectory(t *testing.T) {
	setUp()
	defer tearDown()

	id := "id"
	bdp := client.backupDirectoryPath(id)

	mux.HandleFunc(path.Join("/api/v1", bdp), func(w http.ResponseWriter, r *http.Request) {
		resp := &BackupDirectory{
			ID:          "action-id",
			Name:        "name",
			Description: "description",
			Path:        "path",
			Quota:       1,
			Size:        1,
			MachineID:   "machine-id",
			TenantID:    "tenant-id",
		}

		assert.NoError(t, json.NewEncoder(w).Encode(resp))
	})

	rps, err := client.GetBackupDirectory(id)
	require.NoError(t, err)
	assert.NotEmpty(t, rps.ID)
}

func TestClient_RequestBackupDirectory(t *testing.T) {
	setUp()
	defer tearDown()

	id := "id"
	action := "action"
	storageType := "S3"
	name := "name"
	bdap := client.backupDirectoryActionPath(id)

	mux.HandleFunc(path.Join("/api/v1", bdap), func(w http.ResponseWriter, r *http.Request) {
		resp := &CreateManualBackupRequest{
			Action:      action,
			StorageType: storageType,
			Name:        name,
		}

		assert.NoError(t, json.NewEncoder(w).Encode(resp))
	})

	err := client.RequestBackupDirectory(id, &CreateManualBackupRequest{
		Action:      action,
		StorageType: storageType,
		Name:        name,
	})
	require.NoError(t, err)
}
