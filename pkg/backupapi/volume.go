package backupapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/bizflycloud/bizfly-backup/pkg/storage_vault"
	"go.uber.org/zap"
)

var backoffSchedule = []time.Duration{
	1 * time.Second,
	3 * time.Second,
	5 * time.Second,
	10 * time.Second,
	20 * time.Second,
	30 * time.Second,
}

// StorageVault ...
type StorageVault struct {
	ID               string                   `json:"id"`
	Name             string                   `json:"name"`
	UserType         string                   `json:"user_type"`
	StorageBucket    string                   `json:"storage_bucket"`
	StorageVaultType string                   `json:"storage_vault_type"`
	SecretRef        string                   `json:"secret_ref"`
	CreatedAt        string                   `json:"created_at"`
	UpdatedAt        string                   `json:"updated_at"`
	Deleted          bool                     `json:"deleted"`
	EncryptionKey    string                   `json:"encryption_key"`
	Credential       storage_vault.Credential `json:"credential"`
}

type AuthRestore struct {
	RecoveryPointID   string
	ActionID          string
	CreatedAt         string
	RestoreSessionKey string
}

// credentialStorageVaultPath API
func (c *Client) credentialStorageVaultPath(storageVaultID string, actionID string) string {
	return fmt.Sprintf("/agent/storage_vaults/%s/credential?action_id=%s", storageVaultID, actionID)
}

// GetCredentialStorageVault get a new credential with backend credential not constant.
func (c *Client) GetCredentialStorageVault(storageVaultID string, actionID string, restoreKey *AuthRestore) (*StorageVault, error) {
	var resp *http.Response
	var err error
	if restoreKey != nil {
		for _, backoff := range backoffSchedule {
			req, err := c.NewRequest(http.MethodGet, c.credentialStorageVaultPath(storageVaultID, actionID), nil)
			if err != nil {
				c.logger.Error("err ", zap.Error(err))
				return nil, err
			}
			req.Header.Add("X-Session-Created-At", restoreKey.CreatedAt)
			req.Header.Add("X-Restore-Session-Key", restoreKey.RestoreSessionKey)
			resp, err = c.Do(req)
			if err != nil {
				c.logger.Error("err ", zap.Error(err))
				time.Sleep(backoff)
				continue
			}
			if resp.StatusCode == 401 {
				c.logger.Sugar().Info("GetCredential access denied: ", resp.StatusCode)
				newSessionKey, err := c.GetRestoreSessionKey(restoreKey.RecoveryPointID, restoreKey.ActionID, restoreKey.CreatedAt)
				if err != nil {
					c.logger.Error("Get restore session key error: ", zap.Error(err))
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
		req, err := c.NewRequest(http.MethodGet, c.credentialStorageVaultPath(storageVaultID, actionID), nil)
		if err != nil {
			c.logger.Error("err ", zap.Error(err))
			return nil, err
		}
		resp, err = c.Do(req)
		if err != nil {
			c.logger.Error("err ", zap.Error(err))
			return nil, err
		}
	}

	if err = checkResponse(resp); err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}
	defer resp.Body.Close()

	var vault StorageVault
	if err := json.NewDecoder(resp.Body).Decode(&vault); err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}
	return &vault, nil
}

// HeadObject a boolean value whether object name existing in storage vault.
func (c *Client) HeadObject(storageVault storage_vault.StorageVault, key string) (bool, string, error) {
	var isExist bool
	var etag string
	var err error
	for _, backoff := range backoffSchedule {
		isExist, etag, err = storageVault.HeadObject(key)
		if err == nil {
			break
		}
		if aerr, ok := err.(awserr.Error); ok {
			if aerr.Code() == "Forbidden" && storageVault.Type().UserType == "DEFAULT" {
				c.logger.Sugar().Info("GetCredential in head object ", key)
				storageVaultID, actID := storageVault.ID()
				vault, err := c.GetCredentialStorageVault(storageVaultID, actID, nil)
				if err != nil {
					c.logger.Error("Error get credential", zap.Error(err))
					break
				}
				err = storageVault.RefreshCredential(vault.Credential)
				if err != nil {
					c.logger.Error("Error refresht credential ", zap.Error(err))
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

// PutObject stores the data to the storage vault.
func (c *Client) PutObject(storageVault storage_vault.StorageVault, key string, data []byte) error {
	var err error
	for _, backoff := range backoffSchedule {
		err = storageVault.PutObject(key, data)
		if err == nil {
			break
		}
		if aerr, ok := err.(awserr.Error); ok {
			if aerr.Code() == "Forbidden" && storageVault.Type().UserType == "DEFAULT" {
				c.logger.Sugar().Info("GetCredential for refreshing session s3")
				storageVaultID, actID := storageVault.ID()
				vault, err := c.GetCredentialStorageVault(storageVaultID, actID, nil)
				if err != nil {
					c.logger.Error("Error get credential", zap.Error(err))
					break
				}
				err = storageVault.RefreshCredential(vault.Credential)
				if err != nil {
					c.logger.Error("Error refresht credential ", zap.Error(err))
					break
				}
			}
		}
		c.logger.Error("Error PutObject", zap.Error(err))
		time.Sleep(backoff)
	}
	return err
}

// GetObject downloads the object by name in storage vault.
func (c *Client) GetObject(storageVault storage_vault.StorageVault, key string, restoreKey *AuthRestore) ([]byte, error) {
	var err error
	for _, backoff := range backoffSchedule {
		data, err := storageVault.GetObject(key)
		if err == nil {
			return data, nil
		}
		if aerr, ok := err.(awserr.Error); ok {
			if aerr.Code() != "" && storageVault.Type().UserType == "DEFAULT" {
				storageVaultID, actID := storageVault.ID()
				vault, err := c.GetCredentialStorageVault(storageVaultID, actID, restoreKey)
				if err != nil {
					c.logger.Error("Error get credential ", zap.Error(err))
					break
				}
				err = storageVault.RefreshCredential(vault.Credential)
				if err != nil {
					c.logger.Error("Error refresht credential ", zap.Error(err))
					break
				}
			}
		}
		c.logger.Error("Error GetObject", zap.Error(err))
		time.Sleep(backoff)
	}
	return nil, err
}
