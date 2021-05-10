package volume

import (
	"errors"
	"fmt"
	"github.com/bizflycloud/bizfly-backup/pkg/backupapi"
	"github.com/bizflycloud/bizfly-backup/pkg/volume/s3"
)

// A StorageVolume manages data stored somewhere.
type StorageVolume interface {
	// PutObject stores the data to the storage backend.
	PutObject(name string, data []byte) error

	// Test a boolean value whether object name existing in storage.
	TestObject(name string) (bool, error)

	// GetObject downloads the object by name in storage.
	GetObject(name string) ([]byte, error)

	// SetCredential sets a new credential with backend credential not constant.
	SetCredential(preSign string)
}

func NewStorageVolume(volume *backupapi.Volume) (StorageVolume, error) {
	if volume.VolumeType == "S3" {
		return s3.NewS3Default(volume.Name, volume.StorageBucket, volume.SecretRef), nil
	}
	return nil, errors.New(fmt.Sprintf("volume type unsupport %s", volume.VolumeType))
}
