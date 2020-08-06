package backupapi

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// BackupDirectory ...
type BackupDirectory struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
	Quota       int    `json:"quota"`
	Size        int    `json:"size"`
	MachineID   string `json:"machine_id"`
	TenantID    string `json:"tenant_id"`
}

func (c *Client) backupDirectoryPath(id int) string {
	return fmt.Sprintf("/agent/backup-directories/%d", id)
}

// GetBackupDirectory retrieves a backup directory by given id.
func (c *Client) GetBackupDirectory(id int) (*BackupDirectory, error) {
	req, err := c.NewRequest(http.MethodGet, c.backupDirectoryPath(id), nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var bd BackupDirectory
	if err := json.NewDecoder(resp.Body).Decode(&bd); err != nil {
		return nil, err
	}
	return &bd, err
}
