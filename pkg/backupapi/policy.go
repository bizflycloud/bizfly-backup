package backupapi

// Policy ...
type Policy struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	SchedulePattern string `json:"schedule_pattern"`
	RetentionHours  int    `json:"retention_hours"`
	RetentionDays   int    `json:"retention_days"`
	RetentionWeeks  int    `json:"retention_weeks"`
	RetentionMonths int    `json:"retention_months"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
	TenantID        string `json:"tenant_id"`
}

// PolicyDirectories ...
type PolicyDirectories struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	SchedulePattern   string   `json:"schedule_pattern"`
	RetentionHours    int      `json:"retention_hours"`
	RetentionDays     int      `json:"retention_days"`
	RetentionWeeks    int      `json:"retention_weeks"`
	RetentionMonths   int      `json:"retention_months"`
	CreatedAt         string   `json:"created_at"`
	UpdatedAt         string   `json:"updated_at"`
	TenantID          string   `json:"tenant_id"`
	BackupDirectories []string `json:"backup-directories"`
}
