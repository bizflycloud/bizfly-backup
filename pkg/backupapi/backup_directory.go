package backupapi

import (
	"encoding/json"
	"fmt"
	"net/http"

	"go.uber.org/zap"
)

// BackupDirectory ...
type BackupDirectory struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
	Quota       int    `json:"quota"`
	Size        int    `json:"size"`
	MachineID   string `json:"machine_id"`
	TenantID    string `json:"tenant_id"`
}

// ListBackupDirectory ...
type ListBackupDirectory struct {
	Directories []BackupDirectory `json:"directories"`
}

// CreateManualBackupRequest represents a request manual backup.
type CreateManualBackupRequest struct {
	Action      string `json:"action"`
	StorageType string `json:"storage_type"`
	Name        string `json:"name"`
}

// UpdateState ...
type UpdateState struct {
	EventType   string        `json:"event_type"`
	Directories []Directories `json:"directories"`
}

// Directories ...
type Directories struct {
	ID   string `json:"id"`
	Size int    `json:"size"`
}

func (c *Client) backupDirectoryPath(id string) string {
	return "/agent/backup-directories/" + id
}

func (c *Client) backupDirectoryActionPath(id string) string {
	return fmt.Sprintf("/agent/backup-directories/%s/action", id)
}

func (c *Client) listBackupDirectoryPath() string {
	return "/agent/backup-directories"
}

// GetBackupDirectory retrieves a backup directory by given id.
func (c *Client) GetBackupDirectory(id string) (*BackupDirectory, error) {
	req, err := c.NewRequest(http.MethodGet, c.backupDirectoryPath(id), nil)
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

	var bd BackupDirectory
	if err := json.NewDecoder(resp.Body).Decode(&bd); err != nil {
		return nil, err
	}
	return &bd, err
}

// RequestBackupDirectory requests a manual backup.
func (c *Client) RequestBackupDirectory(id string, cmbr *CreateManualBackupRequest) error {
	req, err := c.NewRequest(http.MethodPost, c.backupDirectoryActionPath(id), cmbr)
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

// ListBackupDirectory retrieves list backup directory.
func (c *Client) ListBackupDirectory() (*ListBackupDirectory, error) {
	req, err := c.NewRequest(http.MethodGet, c.listBackupDirectoryPath(), nil)
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

	var lbd ListBackupDirectory
	if err := json.NewDecoder(resp.Body).Decode(&lbd); err != nil {
		return nil, err
	}
	return &lbd, err
}
