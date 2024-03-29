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

func TestClient_recoveryPointActionPath(t *testing.T) {
	setUp()
	defer tearDown()

	recoveryPointID := "recovery-point-id"

	rpap := client.recoveryPointActionPath(recoveryPointID)
	assert.Equal(t, "/agent/recovery-points/recovery-point-id/action", rpap)
}

func TestCLient_latestRecoveryPointID(t *testing.T) {
	setUp()
	defer tearDown()

	backupDirectoryID := "backup-directory-id"

	lrp := client.latestRecoveryPointID(backupDirectoryID)
	assert.Equal(t, "/agent/backup-directories/backup-directory-id/latest-recovery-points", lrp)
}

func TestClient_GetLatestRecoveryPointID(t *testing.T) {
	setUp()
	defer tearDown()

	backupDirectoryID := "backup-directory-id"
	latestRecoveryPointPath := client.latestRecoveryPointID(backupDirectoryID)

	mux.HandleFunc(path.Join("/api/v1/", latestRecoveryPointPath), func(w http.ResponseWriter, r *http.Request) {
		resp := RecoveryPointResponse{
			Name:              "backup-manual-05/26/2021",
			RecoveryPointType: "INITIAL_REPLICA",
			ID:                "4650cb5f-48d2-48ab-9e2b-15acc99e1323",
			Status:            "COMPLETED",
		}
		assert.NoError(t, json.NewEncoder(w).Encode(resp))
	})

	lrp, err := client.GetLatestRecoveryPointID(backupDirectoryID)
	require.NoError(t, err)
	assert.NotEmpty(t, lrp.Name)
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

func TestClient_ListRecoveryPoints(t *testing.T) {
	setUp()
	defer tearDown()

	backupDirectoryID := "1"
	recoveryPointPath := client.recoveryPointPath(backupDirectoryID)

	mux.HandleFunc(path.Join("/api/v1/", recoveryPointPath), func(w http.ResponseWriter, r *http.Request) {
		resp := ListRecoveryPointsResponse{
			RecoveryPoints: []RecoveryPointResponse{
				{ID: "1", Status: RecoveryPointStatusCompleted, RecoveryPointType: RecoveryPointTypePoint},
			},
		}
		assert.NoError(t, json.NewEncoder(w).Encode(resp))
	})

	rps, err := client.ListRecoveryPoints(context.Background(), backupDirectoryID)
	require.NoError(t, err)
	assert.NotEmpty(t, rps.RecoveryPoints[0].ID)
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
