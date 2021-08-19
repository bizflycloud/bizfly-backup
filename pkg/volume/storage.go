package volume

// StorageVolume ...
type StorageVolume interface {
	// HeadObject a boolean value whether object name existing in storage.
	HeadObject(key string) (bool, string, error)

	// PutObject stores the data to the storage backend.
	PutObject(key string, data []byte) error

	// GetObject downloads the object by name in storage.
	GetObject(key string) ([]byte, error)

	// SetCredential sets a new credential with backend credential not constant.
	RefreshCredential(credential Credential) error

	// ID return id of volume
	ID() (string, string)

	// Type
	Type() Type
}

type Type struct {
	VolumeType  string
	StorageType string
}

type Credential struct {
	AwsAccessKeyId     string `json:"aws_access_key_id,omitempty"`
	AwsSecretAccessKey string `json:"aws_secret_access_key,omitempty"`
	AwsLocation        string `json:"aws_location,omitempty"`
	Token              string `json:"token,omitempty"`
	Region             string `json:"region,omitempty"`
	Username           string `json:"username,omitempty"`
	Password           string `json:"password,omitempty"`
}
