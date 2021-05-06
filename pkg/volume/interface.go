package volume

// A Volume manages data stored somewhere.
type Volume interface {
	// PutObject stores the data to the storage backend.
	PutObject(name string, data []byte) error

	// Test a boolean value whether object name existing in storage.
	TestObject(name string) (bool, error)

	// GetObject downloads the object by name in storage.
	GetObject(name string) ([]byte, error)
}
