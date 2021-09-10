package backupapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"net/http"

	"go.uber.org/zap"
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

// LatestRecoveryPointID get a id latest recovery point of backup directory id.
type RecoveryPointResponse struct {
	Name              string `json:"name"`
	RecoveryPointType string `json:"recovery_point_type"`
	ID                string `json:"id"`
	Status            string `json:"status"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
	IndexHash         string `json:"index_hash"`
}

func (c *Client) recoveryPointPath(backupDirectoryID string) string {
	return fmt.Sprintf("/agent/backup-directories/%s/recovery-points", backupDirectoryID)
}

func (c *Client) recoveryPointInfo(recoveryPointID string) string {
	return fmt.Sprintf("/agent/recovery-points/%s", recoveryPointID)
}

func (c *Client) recoveryPointActionPath(recoveryPointID string) string {
	return fmt.Sprintf("/agent/recovery-points/%s/action", recoveryPointID)
}

func (c *Client) latestRecoveryPointID(backupDirectoryID string) string {
	return fmt.Sprintf("/agent/backup-directories/%s/latest-recovery-points", backupDirectoryID)
}

func (c *Client) GetRecoveryPointInfo(recoveryPointID string) (*RecoveryPointResponse, error) {
	req, err := c.NewRequest(http.MethodGet, c.recoveryPointInfo(recoveryPointID), nil)
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}
	if err := checkResponse(resp); err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}
	defer resp.Body.Close()
	var lrp RecoveryPointResponse
	if err := json.NewDecoder(resp.Body).Decode(&lrp); err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}
	return &lrp, nil
}

func (c *Client) GetLatestRecoveryPointID(backupDirectoryID string) (*RecoveryPointResponse, error) {
	req, err := c.NewRequest(http.MethodGet, c.latestRecoveryPointID(backupDirectoryID), nil)
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}
	if err := checkResponse(resp); err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}
	defer resp.Body.Close()

	var lrp RecoveryPointResponse
	if err := json.NewDecoder(resp.Body).Decode(&lrp); err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}
	return &lrp, nil
}

func (c *Client) CreateRecoveryPoint(ctx context.Context, backupDirectoryID string, crpr *CreateRecoveryPointRequest) (*CreateRecoveryPointResponse, error) {
	req, err := c.NewRequest(http.MethodPost, c.recoveryPointPath(backupDirectoryID), crpr)
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}

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
	var crp CreateRecoveryPointResponse
	if err := json.NewDecoder(resp.Body).Decode(&crp); err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}

	return &crp, nil
}

// ListRecoveryPoints list all recovery points of given backup directory.
func (c *Client) ListRecoveryPoints(ctx context.Context, backupDirectoryID string) ([]RecoveryPoint, error) {
	req, err := c.NewRequest(http.MethodGet, c.recoveryPointPath(backupDirectoryID), nil)
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}

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

	var rps []RecoveryPoint
	if err := json.NewDecoder(resp.Body).Decode(&rps); err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}
	return rps, nil
}

// RequestRestore requests restore
func (c *Client) RequestRestore(recoveryPointID string, crr *CreateRestoreRequest) error {
	req, err := c.NewRequest(http.MethodPost, c.recoveryPointActionPath(recoveryPointID), crr)
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return err
	}

	if err := checkResponse(resp); err != nil {
		c.logger.Error("err ", zap.Error(err))
		return err
	}
	defer resp.Body.Close()
	return nil
}

func (c *Client) GetRestoreSessionKey(recoveryPointID string, actionID string, createdAt string) (*RestoreResponse, error) {
	reqURL, err := c.urlStringFromRelPath(c.getRestoreSessionKey(recoveryPointID))
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}

	req, err := c.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}
	q := req.URL.Query()
	q.Add("action_id", actionID)
	q.Add("created_at", createdAt)
	req.URL.RawQuery = q.Encode()

	resp, err := c.Do(req)
	if err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}

	var restoreRsp RestoreResponse
	if err := json.NewDecoder(resp.Body).Decode(&restoreRsp); err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}
	return &restoreRsp, nil
}

func (c *Client) getRestoreSessionKey(recoveryPointID string) string {
	return fmt.Sprintf("/agent/recovery-points/%s/restore-key", recoveryPointID)
}

type RestoreResponse struct {
	ActionID          string `json:"action_id"`
	CreatedAt         string `json:"created_at"`
	RestoreSessionKey string `json:"restore_session_key"`
}
