package volume

// StorageVolume ...
type StorageVolume interface {
	HeadObject(key string) (bool, error)
	PutObject(key string, data []byte) error
	GetObject(key string) ([]byte, error)
	SetCredential(preSign string)
}
