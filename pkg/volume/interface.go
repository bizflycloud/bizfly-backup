package volume

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
