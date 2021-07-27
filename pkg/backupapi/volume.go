package backupapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go/aws/awserr"
	"go.uber.org/zap"

	"github.com/bizflycloud/bizfly-backup/pkg/volume"
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

type AuthRestore struct {
	RecoveryPointID   string
	ActionID          string
	CreatedAt         string
	RestoreSessionKey string
}

// credentialVolumePath API
func (c *Client) credentialVolumePath(volumeID string, actionID string) string {
	return fmt.Sprintf("/agent/volumes/%s/credential?action_id=%s", volumeID, actionID)
}

// GetCredentialVolume get a new credential with backend credential not constant.
func (c *Client) GetCredentialVolume(volumeID string, actionID string, restoreKey *AuthRestore) (*Volume, error) {

	var resp *http.Response
	var err error
	if restoreKey != nil {
		for _, backoff := range backoffSchedule {
			req, err := c.NewRequest(http.MethodGet, c.credentialVolumePath(volumeID, actionID), nil)
			if err != nil {
				return nil, err
			}
			req.Header.Add("X-Session-Created-At", restoreKey.CreatedAt)
			req.Header.Add("X-Restore-Session-Key", restoreKey.RestoreSessionKey)
			resp, err = c.Do(req)
			if err != nil {
				time.Sleep(backoff)
				continue
			}
			if resp.StatusCode == 401 {
				c.logger.Sugar().Info("GetCredential access denied: ", resp.StatusCode)
				newSessionKey, err := c.GetRestoreSessionKey(restoreKey.RecoveryPointID, restoreKey.ActionID, restoreKey.CreatedAt)
				if err != nil {
					c.logger.Sugar().Info("Get restore session key error: ", err)
					return nil, err
				}
				c.logger.Sugar().Info("new session key: ", newSessionKey)
				restoreKey.CreatedAt = newSessionKey.CreatedAt
				restoreKey.RestoreSessionKey = newSessionKey.RestoreSessionKey
				continue
			} else if resp.StatusCode == 200 {
				break
			}
			time.Sleep(backoff)
			continue
		}
	} else {
		req, err := c.NewRequest(http.MethodGet, c.credentialVolumePath(volumeID, actionID), nil)
		if err != nil {
			return nil, err
		}
		resp, err = c.Do(req)
		if err != nil {
			return nil, err
		}
	}

	if err = checkResponse(resp); err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var vol Volume
	if err := json.NewDecoder(resp.Body).Decode(&vol); err != nil {
		return nil, err
	}
	return &vol, nil
}

// HeadObject a boolean value whether object name existing in storage volume.
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
				c.logger.Sugar().Info("GetCredential in head object ", key)
				volID, actID := volume.ID()
				vol, err := c.GetCredentialVolume(volID, actID, nil)
				if err != nil {
					c.logger.Error("Error get credential", zap.Error(err))
					break
				}
				err = volume.RefreshCredential(vol.Credential)
				if err != nil {
					break
				}
			} else if aerr.Code() == "NotFound" {
				err = nil
				break
			}
		} else {
			c.logger.Error("Error head object", zap.Error(err))
			time.Sleep(backoff)
		}

	}
	return isExist, etag, err
}

// PutObject stores the data to the storage volume.
func (c *Client) PutObject(volume volume.StorageVolume, key string, data []byte) error {
	var err error
	for _, backoff := range backoffSchedule {
		err = volume.PutObject(key, data)
		if err == nil {
			break
		}
		if aerr, ok := err.(awserr.Error); ok {
			if aerr.Code() == "Forbidden" && volume.Type().StorageType == "DEFAULT" {
				c.logger.Sugar().Info("GetCredential for refreshing session s3")
				volID, actID := volume.ID()
				vol, err := c.GetCredentialVolume(volID, actID, nil)
				if err != nil {
					c.logger.Error("Error get credential", zap.Error(err))
					break
				}
				err = volume.RefreshCredential(vol.Credential)
				if err != nil {
					break
				}
			}
		}
		c.logger.Error("Error PutObject", zap.Error(err))
		time.Sleep(backoff)
	}
	return err
}

// GetObject downloads the object by name in storage volume.
func (c *Client) GetObject(volume volume.StorageVolume, key string, restoreKey *AuthRestore) ([]byte, error) {
	var err error
	for _, backoff := range backoffSchedule {
		data, err := volume.GetObject(key)
		if err == nil {
			return data, nil
		}
		if aerr, ok := err.(awserr.Error); ok {
			if aerr.Code() != "" && volume.Type().StorageType == "DEFAULT" {
				volID, actID := volume.ID()
				vol, err := c.GetCredentialVolume(volID, actID, restoreKey)
				if err != nil {
					break
				}
				err = volume.RefreshCredential(vol.Credential)
				if err != nil {
					break
				}
			}
		}
		c.logger.Error("Error GetObject", zap.Error(err))
		time.Sleep(backoff)
	}
	return nil, err
}
