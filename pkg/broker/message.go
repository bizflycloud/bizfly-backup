package broker

import "errors"

const (
	BackupManual  = "backup_manual"
	RestoreManual = "restore_manual"
	ConfigUpdate  = "config_update"
	AgentUpgrade  = "agent_upgrade"
)

// ErrUnknownEventType is raised when receiving unhandled event from broker.
var ErrUnknownEventType = errors.New("unknown event type")

// Message is the message event format.
type Message struct {
	EventType string `json:"event_type"`
	MachineID string `json:"machine_id"`
	CreatedAt string `json:"created_at"`

	// For performing backup/update cron/manual.
	BackupDirectory   string `json:"backup_directory"`
	BackupDirectoryID string `json:"backup_directory_id"`
	PolicyID          string `json:"policy_id"`

	// For performing restore.
	SourceMachineID      string `json:"source_machine_id"`
	DestinationMachineID string `json:"dest_machine_id"`
	SourceDirectory      string `json:"source_directory"`
	DestinationDirectory string `json:"dest_directory"`
	RecoveryPointID      string `json:"recovery_point_id"`
	RestoreSessionKey    string `json:"restore_session_key"`
}
