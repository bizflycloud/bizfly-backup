package backupapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_CreateRecoveryPoint(t *testing.T) {
	setUp()
	defer tearDown()

	backupDirectoryID := 1
	policyID := "policy-id"
	recoveryPointPath := client.createRecoveryPointPath(backupDirectoryID)

	mux.HandleFunc(recoveryPointPath, func(w http.ResponseWriter, r *http.Request) {
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

	backupDirectoryID := 1
	recoveryPointID := "recovery-point-id"
	recoveryPointPath := client.updateRecoveryPointPath(backupDirectoryID, recoveryPointID)
	status := RecoveryPointStatusFAILED

	mux.HandleFunc(recoveryPointPath, func(w http.ResponseWriter, r *http.Request) {
		var urcr UpdateRecoveryPointRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&urcr))
		assert.Equal(t, status, urcr.Status)
	})

	err := client.UpdateRecoveryPoint(context.Background(), backupDirectoryID, recoveryPointID, &UpdateRecoveryPointRequest{Status: RecoveryPointStatusFAILED})
	require.NoError(t, err)
}
