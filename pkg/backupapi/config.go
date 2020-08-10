package backupapi

// BackupDirectoryConfig is the cron policies for given directory.
type BackupDirectoryConfig struct {
	ID       string                        `json:"id"`
	Name     string                        `json:"name"`
	Path     string                        `json:"path"`
	Policies []BackupDirectoryConfigPolicy `json:"policies"`
}

// BackupDirectoryConfigPolicy is the cron policy.
type BackupDirectoryConfigPolicy struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	SchedulePattern string `json:"schedule_pattern"`
	Activated       bool   `json:"activated"`
}
