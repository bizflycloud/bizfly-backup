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
	Name              string `json:"name"`
	RecoveryPointType string `json:"recovery_point_type"`
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
	Volume        *Volume        `json:"volume"`
}

// CreateRecoveryPointRequest represents a request to create a recovery point.
type CreateRecoveryPointRequest struct {
	PolicyID          string `json:"policy_id"`
	Name              string `json:"name"`
	RecoveryPointType string `json:"recovery_point_type"`
	ChangedTime       string `json:"changed_time"`
	ModifiedTime      string `json:"modified_time"`
	AccessTime        string `json:"access_time"`
	Mode              string `json:"mode"`
	UID               string `json:"uid"`
	GID               string `json:"gid"`
}

// CreateRestoreRequest represents a request manual backup.
type CreateRestoreRequest struct {
	MachineID string `json:"machine_id"`
	Path      string `json:"path"`
}

// UpdateRecoveryPointRequest represents a request to update a recovery point.
type UpdateRecoveryPointRequest struct {
	Status string `json:"status"`
}

func (c *Client) recoveryPointPath(backupDirectoryID string) string {
	return "/agent/backup-directories/" + backupDirectoryID + "/recovery-points"
}

func (c *Client) recoveryPointItemPath(backupDirectoryID string, recoveryPointID string) string {
	return fmt.Sprintf("/agent/backup-directories/%s/recovery-points/%s", backupDirectoryID, recoveryPointID)
}

func (c *Client) recoveryPointActionPath(recoveryPointID string) string {
	return fmt.Sprintf("/agent/recovery-points/%s/action", recoveryPointID)
}

func (c *Client) saveChunkPath(recoveryPointID string, fileID string) string {
	return fmt.Sprintf("/agent/recovery-points/%s/file/%s/chunks", recoveryPointID, fileID)
}

func (c *Client) getListFilePath(recoveryPointID string) string {
	return fmt.Sprintf("/agent/recovery-points/%s/list-files", recoveryPointID)
}

func (c *Client) infoFile(recoveryPointID string, itemID string) string {
	return fmt.Sprintf("/agent/auth/%s/file/%s", recoveryPointID, itemID)
}

func (c *Client) CreateRecoveryPoint(ctx context.Context, backupDirectoryID string, crpr *CreateRecoveryPointRequest) (*CreateRecoveryPointResponse, error) {
	req, err := c.NewRequest(http.MethodPost, c.recoveryPointPath(backupDirectoryID), crpr)
	if err != nil {
		return nil, err
	}

	resp, err := c.Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	if err := checkResponse(resp); err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var crp CreateRecoveryPointResponse
	if err := json.NewDecoder(resp.Body).Decode(&crp); err != nil {
		return nil, err
	}

	return &crp, nil
}

func (c *Client) UpdateRecoveryPoint(ctx context.Context, backupDirectoryID string, recoveryPointID string, urpr *UpdateRecoveryPointRequest) error {
	req, err := c.NewRequest(http.MethodPatch, c.recoveryPointItemPath(backupDirectoryID, recoveryPointID), urpr)
	if err != nil {
		return err
	}

	resp, err := c.Do(req.WithContext(ctx))
	if err != nil {
		return err
	}
	if err := checkResponse(resp); err != nil {
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

// ListRecoveryPoints list all recovery points of given backup directory.
func (c *Client) ListRecoveryPoints(ctx context.Context, backupDirectoryID string) ([]RecoveryPoint, error) {
	req, err := c.NewRequest(http.MethodGet, c.recoveryPointPath(backupDirectoryID), nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	if err := checkResponse(resp); err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var rps []RecoveryPoint
	if err := json.NewDecoder(resp.Body).Decode(&rps); err != nil {
		return nil, err
	}
	return rps, nil
}

// RequestRestore requests restore
func (c *Client) RequestRestore(recoveryPointID string, crr *CreateRestoreRequest) error {
	req, err := c.NewRequest(http.MethodPost, c.recoveryPointActionPath(recoveryPointID), crr)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}

	if err := checkResponse(resp); err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}
