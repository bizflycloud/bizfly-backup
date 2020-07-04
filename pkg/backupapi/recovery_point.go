package backupapi

const (
	RecoveryPointTypePoint          = "RECOVERY_POINT"
	RecoveryPointTypeInitialReplica = "INITIAL_REPLICA"

	RecoveryPointStatusCreated   = "CREATED"
	RecoveryPointStatusCompleted = "COMPLETED"
	RecoveryPointStatusFAILED    = "FAILED"
)

// RecoveryPoint ...
type RecoveryPoint struct {
	ID                string `json:"id"`
	RecoveryPointType string `json:"recoveryPointType"`
	Status            string `json:"status"`
	SessionID         string `json:"session_id"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}
