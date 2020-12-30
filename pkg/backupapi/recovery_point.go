package backupapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

//
type CompleteMultiPartUploadRequest struct {
	Size       int    `json:"size"`
	Mode       string `json:"mode"`
	Path       string `json:"path"`
	IsDir      bool   `json:"is_dir"`
	ModifiedAt string `json:"modified_at"`
}

func (c *Client) recoveryPointPath(backupDirectoryID string) string {
	return "/agent/backup-directories/" + backupDirectoryID + "/recovery-points"
}

func (c *Client) recoveryPointItemPath(backupDirectoryID string, recoveryPointID string) string {
	return fmt.Sprintf("/agent/backup-directories/%s/recovery-points/%s", backupDirectoryID, recoveryPointID)
}

func (c *Client) downloadFileContentPath(recoveryPointID string) string {
	return fmt.Sprintf("/agent/recovery-points/%s/file/download", recoveryPointID)
}

func (c *Client) recoveryPointActionPath(recoveryPointID string) string {
	return fmt.Sprintf("/agent/recovery-points/%s/action", recoveryPointID)
}

func (c *Client) initMultipartPath(recoveryPointID string) string {
	return fmt.Sprintf("/agent/recovery-points/%s/file/multipart/init", recoveryPointID)
}

func (c *Client) uploadPartPath(recoveryPointID string) string {
	return fmt.Sprintf("/agent/recovery-points/%s/file/multipart/put", recoveryPointID)
}

func (c *Client) completeMultipartPath(recoveryPointID string) string {
	return fmt.Sprintf("/agent/recovery-points/%s/file/multipart/complete", recoveryPointID)
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

// DownloadFileContent downloads file content at given recovery point id, write the content to writer.
func (c *Client) DownloadFileContent(ctx context.Context, createdAt string, restoreSessionKey string, recoveryPointID string, w io.Writer) error {
	req, err := c.NewRequest(http.MethodGet, c.downloadFileContentPath(recoveryPointID), nil)
	if err != nil {
		return err
	}
	req.Header.Add("X-Session-Created-At", createdAt)
	req.Header.Add("X-Restore-Session-Key", restoreSessionKey)
	q := req.URL.Query()
	q.Set("name", recoveryPointID+".zip")
	req.URL.RawQuery = q.Encode()

	resp, err := c.Do(req.WithContext(ctx))
	if err != nil {
		return err
	}
	if err := checkResponse(resp); err != nil {
		return err
	}
	defer resp.Body.Close()

	_, err = io.Copy(w, resp.Body)
	return err
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

func (c *Client) InitMultipart(ctx context.Context, recoveryPointID string) (*Multipart, error) {
	req, err := c.NewRequest(http.MethodPost, c.initMultipartPath(recoveryPointID), nil)
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

	var m Multipart
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (c *Client) CompleteMultipart(ctx context.Context, recoveryPointID, uploadID string, cmpr *CompleteMultiPartUploadRequest) error {
	req, err := c.NewRequest(http.MethodPost, c.completeMultipartPath(recoveryPointID), cmpr)
	if err != nil {
		return err
	}
	q := req.URL.Query()
	q.Add("upload_id", uploadID)
	req.URL.RawQuery = q.Encode()

	resp, err := c.Do(req.WithContext(ctx))
	if err != nil {
		return err
	}
	if err := checkResponse(resp); err != nil {
		return err
	}
	defer resp.Body.Close()

	_, err = io.Copy(ioutil.Discard, resp.Body)
	return err
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
