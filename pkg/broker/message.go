package broker

import (
	"errors"

	"github.com/bizflycloud/bizfly-backup/pkg/backupapi"
)

const (
	BackupManual                        = "backup_manual"
	RestoreManual                       = "restore_manual"
	ConfigUpdate                        = "update_config"
	ConfigRefresh                       = "refresh_config"
	AgentUpgrade                        = "agent_upgrade"
	ConfigUpdateActionAddPolicy         = "add_policy"
	ConfigUpdateActionDelPolicy         = "del_policy"
	ConfigUpdateActionUpdatePolicy      = "update_policy"
	ConfigUpdateActionActiveDirectory   = "active_directory"
	ConfigUpdateActionDeactiveDirectory = "deactive_directory"
	ConfigUpdateActionAddDirectory      = "add_directory"
	ConfigUpdateActionDelDirectory      = "del_directory"
	StatusNotify                        = "status_notify"
)

// ErrUnknownEventType is raised when receiving unhandled event from broker.
var ErrUnknownEventType = errors.New("unknown event type")

// Message is the message event format.
type Message struct {
	EventType string `json:"event_type"`
	MachineID string `json:"machine_id"`
	CreatedAt string `json:"created_at"`

	// For notify status
	Status string `json:"status"`

	// For performing backup/update cron/manual.
	BackupDirectory       string `json:"backup_directory"`
	BackupDirectoryID     string `json:"backup_directory_id"`
	PolicyID              string `json:"policy_id"`
	Name                  string `json:"name"`
	LatestRecoveryPointID string `json:"latest_rp_id"`

	// For performing restore.
	SourceMachineID      string `json:"source_machine_id"`
	DestinationMachineID string `json:"dest_machine_id"`
	SourceDirectory      string `json:"source_directory"`
	DestinationDirectory string `json:"dest_directory"`
	RecoveryPointID      string `json:"recovery_point_id"`
	RestoreSessionKey    string `json:"restore_session_key"`
	ActionId             string `json:"action_id"`
	VolumeType           string `json:"volume_type"`

	// For config update
	BackupDirectories []backupapi.BackupDirectoryConfig `json:"backup_directories"`
	Action            string                            `json:"action"`
}
