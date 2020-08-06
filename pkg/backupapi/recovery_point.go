package backupapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
)

const (
	RecoveryPointTypePoint          = "RECOVERY_POINT"
	RecoveryPointTypeInitialReplica = "INITIAL_REPLICA"

	RecoveryPointStatusCreated   = "CREATED"
	RecoveryPointStatusCompleted = "COMPLETED"
	RecoveryPointStatusFAILED    = "FAILED"
)

// ErrUpdateRecoveryPoint indicates that there is error from server when updating recovery point.
var ErrUpdateRecoveryPoint = errors.New("failed to update recovery point")

// RecoveryPoint ...
type RecoveryPoint struct {
	ID                string `json:"id"`
	RecoveryPointType string `json:"recoveryPointType"`
	Status            string `json:"status"`
	PolicyID          string `json:"policy_id"`
	BackupDirectoryID string `json:"backup_directory_id"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

// CreateRecoveryPointResponse is the server response when creating recovery point
type CreateRecoveryPointResponse struct {
	ID            string         `json:"id"`
	RecoveryPoint *RecoveryPoint `json:"recovery_point"`
	Action        string         `json:"action"`
	Status        string         `json:"status"`
}

// CreateRecoveryPointRequest represents a request to create a recovery point.
type CreateRecoveryPointRequest struct {
	PolicyID string `json:"policy_id"`
}

// UpdateRecoveryPointRequest represents a request to update a recovery point.
type UpdateRecoveryPointRequest struct {
	Status string `json:"status"`
}

func (c *Client) createRecoveryPointPath(backupDirectoryID int) string {
	return fmt.Sprintf("/agent/backup-directories/%d/recovery-points", backupDirectoryID)
}

func (c *Client) updateRecoveryPointPath(backupDirectoryID int, recoveryPointID string) string {
	return fmt.Sprintf("/agent/backup-directories/%d/recovery-points/%s", backupDirectoryID, recoveryPointID)
}

func (c *Client) CreateRecoveryPoint(ctx context.Context, backupDirectoryID int, crpr *CreateRecoveryPointRequest) (*CreateRecoveryPointResponse, error) {
	req, err := c.NewRequest(http.MethodPost, c.createRecoveryPointPath(backupDirectoryID), crpr)
	if err != nil {
		return nil, err
	}

	resp, err := c.Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var crp CreateRecoveryPointResponse
	if err := json.NewDecoder(resp.Body).Decode(&crp); err != nil {
		return nil, err
	}

	return &crp, nil
}

func (c *Client) UpdateRecoveryPoint(ctx context.Context, backupDirectoryID int, recoveryPointID string, urpr *UpdateRecoveryPointRequest) error {
	req, err := c.NewRequest(http.MethodPatch, c.updateRecoveryPointPath(backupDirectoryID, recoveryPointID), urpr)
	if err != nil {
		return err
	}

	resp, err := c.Do(req.WithContext(ctx))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	buf, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %w", string(buf), ErrUpdateRecoveryPoint)
	}
	return nil
}
