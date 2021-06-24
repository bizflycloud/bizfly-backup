package backupapi

import (
	"encoding/json"
	"fmt"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/bizflycloud/bizfly-backup/pkg/volume"
	log "github.com/sirupsen/logrus"
	"net/http"
	"time"
)

var backoffSchedule = []time.Duration{
	1 * time.Second,
	3 * time.Second,
	5 * time.Second,
	10 * time.Second,
	20 * time.Second,
	30 * time.Second,
}

// Volume ...
type Volume struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	StorageType   string            `json:"storage_type"`
	StorageBucket string            `json:"storage_bucket"`
	VolumeType    string            `json:"volume_type"`
	SecretRef     string            `json:"secret_ref"`
	CreatedAt     string            `json:"created_at"`
	UpdatedAt     string            `json:"updated_at"`
	Deleted       bool              `json:"deleted"`
	EncryptionKey string            `json:"encryption_key"`
	Credential    volume.Credential `json:"credential"`
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

func (c *Client) HeadObject(volume volume.StorageVolume, key string) (bool, string, error) {
	var isExist bool
	var etag string
	var err error
	for _, backoff := range backoffSchedule {
		isExist, etag, err = volume.HeadObject(key)
		if err == nil {
			break
		}
		if aerr, ok := err.(awserr.Error); ok {
			if aerr.Code() == "Forbidden" && volume.Type().StorageType == "DEFAULT" {
				log.Info("GetCredential in head object")
				vol, err := c.GetCredentialVolume(volume.ID())
				if err != nil {
					log.Error("Error get credential")
					break
				}
				err = volume.RefreshCredential(vol.Credential)
				if err != nil {
					break
				}
			} else if aerr.Code() == "NotFound" {
				break
			}
		} else {
			log.Error("Error head object", err)
			time.Sleep(backoff)
		}

	}
	return isExist, etag, err
}

func (c *Client) PutObject(volume volume.StorageVolume, key string, data []byte) error {
	var err error
	for _, backoff := range backoffSchedule {
		err = volume.PutObject(key, data)
		if err == nil {
			break
		}
		if aerr, ok := err.(awserr.Error); ok {
			if aerr.Code() == "Forbidden" && volume.Type().StorageType == "DEFAULT" {
				log.Info("GetCredential for refreshing session s3")
				vol, err := c.GetCredentialVolume(volume.ID())
				if err != nil {
					log.Error("Error get credential")
					break
				}
				err = volume.RefreshCredential(vol.Credential)
				if err != nil {
					break
				}
			}
		}
		log.Error("Error PutObject", err)
		time.Sleep(backoff)
	}
	return err
}

func (c *Client) GetObject(volume volume.StorageVolume, key string) ([]byte, error) {
	var err error
	for _, backoff := range backoffSchedule {
		data, err := volume.GetObject(key)
		if err == nil {
			return data, nil
		}
		if aerr, ok := err.(awserr.Error); ok {
			if aerr.Code() != "" && volume.Type().StorageType == "DEFAULT" {
				vol, err := c.GetCredentialVolume(volume.ID())
				if err != nil {
					break
				}
				err = volume.RefreshCredential(vol.Credential)
				if err != nil {
					break
				}
			}
		}
		log.Error("Error GetObject", err)
		time.Sleep(backoff)
	}
	return nil, err
}
