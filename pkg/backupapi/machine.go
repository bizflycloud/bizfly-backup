package backupapi

// Machine ...
type Machine struct {
	ID           string `json:"id"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	Name         string `json:"name"`
	HostName     string `json:"host_name"`
	IPAddress    string `json:"ip_address"`
	OSVersion    string `json:"os_version"`
	AgentVersion string `json:"agent_version"`
	TenantID     string `json:"tenant_id"`
}
