package backupapi

import (
	"context"
	"encoding/json"
	"net/http"
	"path"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_recoveryPointPath(t *testing.T) {
	setUp()
	defer tearDown()

	backupDirectoryID := "backup-directory-id"
	rpp := client.recoveryPointPath(backupDirectoryID)
	assert.Equal(t, "/agent/backup-directories/backup-directory-id/recovery-points", rpp)
}

func TestClient_recoveryPointItemPath(t *testing.T) {
	setUp()
	defer tearDown()

	backupDirectoryID := "backup-directory-id"
	recoveryPointID := "recovery-point-id"

	rpip := client.recoveryPointItemPath(backupDirectoryID, recoveryPointID)
	assert.Equal(t, "/agent/backup-directories/backup-directory-id/recovery-points/recovery-point-id", rpip)
}

func TestClient_recoveryPointActionPath(t *testing.T) {
	setUp()
	defer tearDown()

	recoveryPointID := "recovery-point-id"

	rpap := client.recoveryPointActionPath(recoveryPointID)
	assert.Equal(t, "/agent/recovery-points/recovery-point-id/action", rpap)
}

func TestClient_saveChunkPath(t *testing.T) {
	setUp()
	defer tearDown()

	recoveryPointID := "recovery-point-id"
	fileID := "file-id"

	scp := client.saveChunkPath(recoveryPointID, fileID)
	assert.Equal(t, "/agent/recovery-points/recovery-point-id/file/file-id/chunks", scp)
}

func TestClient_getListFilePath(t *testing.T) {
	setUp()
	defer tearDown()

	recoveryPointID := "recovery-point-id"

	glfp := client.getListFilePath(recoveryPointID)
	assert.Equal(t, "/agent/recovery-points/recovery-point-id/list-files", glfp)
}

func TestClient_CreateRecoveryPoint(t *testing.T) {
	setUp()
	defer tearDown()

	backupDirectoryID := "1"
	policyID := "policy-id"
	recoveryPointPath := client.recoveryPointPath(backupDirectoryID)

	mux.HandleFunc(path.Join("/api/v1/", recoveryPointPath), func(w http.ResponseWriter, r *http.Request) {
		crp := &CreateRecoveryPointResponse{
			ID: "ActionID",
			RecoveryPoint: &RecoveryPoint{
				ID:                "recovery-point-id",
				RecoveryPointType: RecoveryPointTypePoint,
				Status:            RecoveryPointStatusCreated,
				PolicyID:          "recovery-point-policy-id",
				CreatedAt:         time.Now().UTC().Format(http.TimeFormat),
			},
		}

		require.NoError(t, json.NewEncoder(w).Encode(crp))
	})

	crp, err := client.CreateRecoveryPoint(context.Background(), backupDirectoryID, &CreateRecoveryPointRequest{PolicyID: policyID})
	require.NoError(t, err)
	assert.NotEmpty(t, crp.ID)
	assert.NotEmpty(t, crp.RecoveryPoint.ID)
	assert.Equal(t, RecoveryPointStatusCreated, crp.RecoveryPoint.Status)
	assert.Equal(t, RecoveryPointTypePoint, crp.RecoveryPoint.RecoveryPointType)
}

func TestClient_UpdateRecoveryPoint(t *testing.T) {
	setUp()
	defer tearDown()

	backupDirectoryID := "1"
	recoveryPointID := "recovery-point-id"
	recoveryPointPath := client.recoveryPointItemPath(backupDirectoryID, recoveryPointID)
	status := RecoveryPointStatusFAILED

	mux.HandleFunc(path.Join("/api/v1/", recoveryPointPath), func(w http.ResponseWriter, r *http.Request) {
		var urpr UpdateRecoveryPointRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&urpr))
		assert.Equal(t, status, urpr.Status)
	})

	err := client.UpdateRecoveryPoint(context.Background(), backupDirectoryID, recoveryPointID, &UpdateRecoveryPointRequest{Status: RecoveryPointStatusFAILED})
	require.NoError(t, err)
}

func TestClient_ListRecoveryPoints(t *testing.T) {
	setUp()
	defer tearDown()

	backupDirectoryID := "1"
	recoveryPointPath := client.recoveryPointPath(backupDirectoryID)

	mux.HandleFunc(path.Join("/api/v1/", recoveryPointPath), func(w http.ResponseWriter, r *http.Request) {
		resp := []RecoveryPoint{
			{ID: "1", Status: RecoveryPointStatusCompleted, RecoveryPointType: RecoveryPointTypePoint},
			{ID: "2", Status: RecoveryPointStatusCreated, RecoveryPointType: RecoveryPointTypePoint},
		}
		assert.NoError(t, json.NewEncoder(w).Encode(resp))
	})

	rps, err := client.ListRecoveryPoints(context.Background(), backupDirectoryID)
	require.NoError(t, err)
	assert.Len(t, rps, 2)
}

func TestClient_RequestRestore(t *testing.T) {
	setUp()
	defer tearDown()

	recoveryPointID := "recovery-point-id"
	recoveryPointActionPath := client.recoveryPointActionPath(recoveryPointID)
	machine_id := "machine-id"
	path_restore := "path"

	mux.HandleFunc(path.Join("/api/v1/", recoveryPointActionPath), func(w http.ResponseWriter, r *http.Request) {
		var crr CreateRestoreRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&crr))
		assert.Equal(t, machine_id, crr.MachineID)
		assert.Equal(t, path_restore, crr.Path)
	})

	err := client.RequestRestore(recoveryPointID, &CreateRestoreRequest{
		MachineID: machine_id,
		Path:      path_restore,
	})
	require.NoError(t, err)
}
