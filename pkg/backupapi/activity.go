package backupapi

import (
	"context"
	"encoding/json"
	"go.uber.org/zap"
	"net/http"
)

// Activity ...
type Activity struct {
	ID                string        `json:"id"`
	Action            string        `json:"action"`
	Status            string        `json:"status"`
	Message           string        `json:"message"`
	BackupDirectoryID string        `json:"backup_directory_id"`
	PolicyID          string        `json:"policy_id"`
	Progress          string        `json:"progress_restore"`
	RecoveryPoint     RecoveryPoint `json:"recovery_point"`
	MachineID         string        `json:"machine_id"`
	CreatedAt         string        `json:"created_at"`
	UpdatedAt         string        `json:"updated_at"`
}

// ListBackupDirectory ...
type ListActivity struct {
	Activities []Activity `json:"activities"`
}

func (c *Client) listActivityPath() string {
	return "/agent/activity"
}

// ListActivity retrieves list activity.
func (c *Client) ListActivity(ctx context.Context, machineID string, statuses []string) (*ListActivity, error) {
	req, err := c.NewRequest(http.MethodGet, c.listActivityPath(), nil)
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}

	q := req.URL.Query()
	if machineID != "" {
		q.Add("machine_id", machineID)
	}
	for _, status := range statuses {
		q.Add("statuses", status)
	}
	req.URL.RawQuery = q.Encode()

	resp, err := c.Do(req.WithContext(ctx))
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}
	if err := checkResponse(resp); err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}
	defer resp.Body.Close()

	var la ListActivity
	if err := json.NewDecoder(resp.Body).Decode(&la); err != nil {
		return nil, err
	}
	return &la, err
}
