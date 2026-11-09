package storage_vault

import (
	"context"
	"io"
)

// storageVault ...
type StorageVault interface {
	// HeadObject a boolean value whether object name existing in storage.
	HeadObject(ctx context.Context, key string) (bool, string, error)

	// PutObject stores the data to the storage backend.
	PutObject(ctx context.Context, key string, data []byte) error

	// GetObject downloads the object by name in storage.
	GetObject(ctx context.Context, key string) ([]byte, error)

	// GetObjectTo streams the object body directly into dst starting at byte offset off,
	// avoiding buffering the whole chunk in memory. Every call (including a retry after a
	// partial write) must start writing at off again, so dst is never left misaligned.
	// The request and the body copy must stop when ctx is cancelled. Implementations should
	// not run their own backoff loop for transient errors: the caller owns retry policy.
	// Implementations MUST close their internal response body.
	GetObjectTo(ctx context.Context, key string, dst io.WriterAt, off int64) (int64, error)

	// SetCredential sets a new credential with backend credential not constant.
	RefreshCredential(credential Credential) error

	// ID return id of storage vault
	ID() (string, string)

	// Type
	Type() Type
}

type Type struct {
	StorageVaultType string
	CredentialType   string
}

type Credential struct {
	AwsAccessKeyId     string `json:"aws_access_key_id,omitempty"`
	AwsSecretAccessKey string `json:"aws_secret_access_key,omitempty"`
	AwsLocation        string `json:"aws_location,omitempty"`
	Token              string `json:"token,omitempty"`
	Region             string `json:"region,omitempty"`
}
