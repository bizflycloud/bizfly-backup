package cache

type Chunk struct {
	BackupDirectoryID string            `json:"backup_directory_id"`
	RecoveryPointID   string            `json:"recovery_point_id"`
	Chunks            map[string][]uint `json:"chunks"`
}

func NewChunk(bdID string, rpID string) *Chunk {
	return &Chunk{
		BackupDirectoryID: bdID,
		RecoveryPointID:   rpID,
		Chunks:            make(map[string][]uint),
	}
}
