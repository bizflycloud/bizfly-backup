package backupapi

// Volume ...
type Volume struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	StorageType   string `json:"storage_type"`
	StorageBucket string `json:"storage_bucket"`
	VolumeType    string `json:"volume_type"`
	SecretRef     string `json:"secret_ref"`
}
