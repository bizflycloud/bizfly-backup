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
		var urcr UpdateRecoveryPointRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&urcr))
		assert.Equal(t, status, urcr.Status)
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
