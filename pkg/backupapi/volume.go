package backupapi

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Volume ...
type Volume struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	StorageType   string     `json:"storage_type"`
	StorageBucket string     `json:"storage_bucket"`
	VolumeType    string     `json:"volume_type"`
	SecretRef     string     `json:"secret_ref"`
	CreatedAt     string     `json:"created_at"`
	UpdatedAt     string     `json:"updated_at"`
	Deleted       bool       `json:"deleted"`
	EncryptionKey string     `json:"encryption_key"`
	Credential    Credential `json:"credential"`
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

func (c *Client) credentialVolumePath(volumeID string, actionID string) string {
	return fmt.Sprintf("/agent/volumes/%s/credential?action_id=%s", volumeID, actionID)
}

func (c *Client) GetCredentialVolume(volumeID string, actionID string) (*Volume, error) {
	req, err := c.NewRequest(http.MethodGet, c.credentialVolumePath(volumeID, actionID), nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	if err := checkResponse(resp); err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var vol Volume
	if err := json.NewDecoder(resp.Body).Decode(&vol); err != nil {
		return nil, err
	}
	return &vol, nil
}
