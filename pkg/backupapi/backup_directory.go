package backupapi

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
