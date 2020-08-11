package backupapi

import (
	"encoding/json"
	"net/http"
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

func (c *Client) backupDirectoryPath(id string) string {
	return "/agent/backup-directories/" + id
}

// GetBackupDirectory retrieves a backup directory by given id.
func (c *Client) GetBackupDirectory(id string) (*BackupDirectory, error) {
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
