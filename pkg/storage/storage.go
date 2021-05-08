package storage

// Backend ...
type Backend interface {
	HeadObject(key string) (bool, error)
	PutObject(key string, data []byte) error
	GetObject(key string) ([]byte, error)
}
