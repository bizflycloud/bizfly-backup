package volume

// StorageVolume ...
type StorageVolume interface {
	// HeadObject a boolean value whether object name existing in storage.
	HeadObject(listKey []string, key string) (int, bool)

	// PutObject stores the data to the storage backend.
	PutObject(key string, data []byte) error

	// GetObject downloads the object by name in storage.
	GetObject(key string) ([]byte, error)

	// SetCredential sets a new credential with backend credential not constant.
	SetCredential(preSign string)
}
