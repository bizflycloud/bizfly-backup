package backupapi

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/cenkalti/backoff"

	"github.com/bizflycloud/bizfly-backup/pkg/storage_vault"

	"go.uber.org/zap"
)

// StorageVault ...
type StorageVault struct {
	ID               string                   `json:"id"`
	Name             string                   `json:"name"`
	CredentialType   string                   `json:"credential_type"`
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
	bo := backoff.NewExponentialBackOff()
	bo.MaxInterval = maxRetry
	bo.MaxElapsedTime = maxRetry

	if restoreKey != nil {
		for {
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
			}
			if resp.StatusCode == 401 {
				c.logger.Sugar().Info("GetRestoreSessionKey access denied: ", resp.StatusCode)
				newSessionKey, err := c.GetRestoreSessionKey(restoreKey.RecoveryPointID, restoreKey.ActionID, restoreKey.CreatedAt)
				if err != nil {
					c.logger.Error("Get restore session key error: ", zap.Error(err))
					return nil, err
				}
				c.logger.Sugar().Info("new session key: ", newSessionKey)
				restoreKey.CreatedAt = newSessionKey.CreatedAt
				restoreKey.RestoreSessionKey = newSessionKey.RestoreSessionKey
			} else if resp.StatusCode == 200 {
				break
			}

			c.logger.Debug("GetCredentialStorageVault. Retrying")
			d := bo.NextBackOff()
			if d == backoff.Stop {
				c.logger.Debug("GetCredentialStorageVault. Retry time out")
				break
			}
			c.logger.Sugar().Info("GetCredentialStorageVault. Retry in ", d)
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

	body := resp.Body
	var vault StorageVault
	if err := json.NewDecoder(body).Decode(&vault); err != nil {
		c.logger.Error("err ", zap.Error(err))
		return nil, err
	}
	return &vault, nil
}

// PutObject stores the data to the storage vault.
func (c *Client) PutObject(storageVault storage_vault.StorageVault, key string, data []byte) error {
	var err error
	bo := backoff.NewExponentialBackOff()
	bo.MaxInterval = maxRetry
	bo.MaxElapsedTime = maxRetry

	for {
		err = storageVault.PutObject(key, data)
		if err == nil {
			break
		}
		if aerr, ok := err.(awserr.Error); ok {
			if (aerr.Code() == "Forbidden" || aerr.Code() == "AccessDenied") && storageVault.Type().CredentialType == "DEFAULT" {
				c.logger.Sugar().Info("GetCredential for refreshing session s3")
				storageVaultID, actID := storageVault.ID()

				vault, err := c.GetCredentialStorageVault(storageVaultID, actID, nil)
				if err != nil {
					c.logger.Error("Error get credential", zap.Error(err))
					break
				}

				err = storageVault.RefreshCredential(vault.Credential)
				if err != nil {
					c.logger.Error("Error refresh credential ", zap.Error(err))
					break
				}
			}
		}

		c.logger.Debug("Put object error. Retrying")
		d := bo.NextBackOff()
		if d == backoff.Stop {
			c.logger.Debug("Put object error. Retry time out.", zap.Error(err))
			break
		}
		c.logger.Sugar().Info("Put object error. Retry in ", d)
	}
	return err
}

// GetObject downloads the object by name in storage vault.
func (c *Client) GetObject(storageVault storage_vault.StorageVault, key string, restoreKey *AuthRestore) ([]byte, error) {
	var err error
	bo := backoff.NewExponentialBackOff()
	bo.MaxInterval = maxRetry
	bo.MaxElapsedTime = maxRetry

	for {
		data, err := storageVault.GetObject(key)
		if err == nil {
			return data, nil
		}
		if aerr, ok := err.(awserr.Error); ok {
			if (aerr.Code() == "Forbidden" || aerr.Code() == "AccessDenied") && storageVault.Type().CredentialType == "DEFAULT" {
				storageVaultID, actID := storageVault.ID()

				// get new restore session key
				newSessionKey, err := c.GetRestoreSessionKey(restoreKey.RecoveryPointID, restoreKey.ActionID, restoreKey.CreatedAt)
				c.logger.Sugar().Info("newSessionKey ", newSessionKey)
				if err != nil {
					c.logger.Error("Get restore session key error: ", zap.Error(err))
					return nil, err
				}
				c.logger.Sugar().Info("new session key: ", newSessionKey)

				restoreKey.CreatedAt = newSessionKey.CreatedAt
				restoreKey.RestoreSessionKey = newSessionKey.RestoreSessionKey

				// get credential storage vault
				vault, err := c.GetCredentialStorageVault(storageVaultID, actID, restoreKey)
				if err != nil {
					c.logger.Error("Error get credential ", zap.Error(err))
					break
				}

				// refresh credential storage vault
				err = storageVault.RefreshCredential(vault.Credential)
				if err != nil {
					c.logger.Error("Error refresht credential ", zap.Error(err))
					break
				}
			}
		}

		c.logger.Debug("GetObject error. Retrying")
		d := bo.NextBackOff()
		if d == backoff.Stop {
			c.logger.Debug("GetObject error. Retry time out")
			break
		}
		c.logger.Sugar().Info("GetObject error. Retry in ", d)
	}
	return nil, err
}
