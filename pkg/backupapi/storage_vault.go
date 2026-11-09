package backupapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/aws/smithy-go"
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

	// mu guards CreatedAt, RestoreSessionKey, gen and refreshing. One AuthRestore is shared
	// by every goroutine of a restore (files and chunks). mu is never held across a network
	// call, so reading the generation or waiting for a refresh never blocks for minutes.
	mu sync.Mutex
	// gen counts successful credential refreshes. A goroutine records it before a request;
	// if it has moved on by the time that request fails with access denied, another
	// goroutine already refreshed and the credential is not refreshed again.
	gen uint64
	// refreshing is non-nil while one goroutine renews the session and credential, and is
	// closed when that attempt ends, successful or not.
	refreshing chan struct{}
}

// restoreSession is a copy of an AuthRestore's session, used while talking to the API
// without holding the lock.
type restoreSession struct {
	createdAt string
	key       string
}

func (r *AuthRestore) generation() uint64 {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.gen
}

func (r *AuthRestore) session() restoreSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	return restoreSession{createdAt: r.CreatedAt, key: r.RestoreSessionKey}
}

// replaceSession stores next only if the session is still old, so a slower caller cannot
// overwrite a newer session stored by a concurrent refresh.
func (r *AuthRestore) replaceSession(old, next restoreSession) {
	if old == next {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.CreatedAt == old.createdAt && r.RestoreSessionKey == old.key {
		r.CreatedAt, r.RestoreSessionKey = next.createdAt, next.key
	}
}

// renewRestoreSession fetches a new restore session key into s.
func (c *Client) renewRestoreSession(ctx context.Context, r *AuthRestore, s *restoreSession) error {
	newSessionKey, err := c.getRestoreSessionKeyCtx(ctx, r.RecoveryPointID, r.ActionID, s.createdAt)
	if err != nil {
		c.logger.Error("Get restore session key error: ", zap.Error(err))
		return err
	}
	s.createdAt = newSessionKey.CreatedAt
	s.key = newSessionKey.RestoreSessionKey
	return nil
}

// refreshVaultCredential renews the restore session (when restoreKey is set) and the storage
// vault credential after an access-denied error. seenGen is restoreKey.generation() read
// before the failing request.
//
// Only one goroutine refreshes at a time. Others either see that gen has moved on (someone
// refreshed after their request started) and return at once, or wait for the refresh in
// progress, giving up when ctx is cancelled. The network calls run without holding the lock.
func (c *Client) refreshVaultCredential(ctx context.Context, storageVault storage_vault.StorageVault, restoreKey *AuthRestore, seenGen uint64) error {
	storageVaultID, actID := storageVault.ID()
	if restoreKey == nil {
		vault, err := c.fetchCredentialStorageVault(ctx, storageVaultID, actID, nil, nil, &backoff.StopBackOff{})
		if err != nil {
			c.logger.Error("Error get credential ", zap.Error(err))
			return err
		}
		return storageVault.RefreshCredential(vault.Credential)
	}

	restoreKey.mu.Lock()
	if restoreKey.gen != seenGen {
		restoreKey.mu.Unlock()
		return nil
	}
	if ch := restoreKey.refreshing; ch != nil {
		restoreKey.mu.Unlock()
		select {
		case <-ch:
			// The caller retries; if that refresh failed it will be denied again with the
			// same gen and try refreshing itself.
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ch := make(chan struct{})
	restoreKey.refreshing = ch
	s := restoreSession{createdAt: restoreKey.CreatedAt, key: restoreKey.RestoreSessionKey}
	restoreKey.mu.Unlock()

	renewed := false
	err := func() error {
		if err := c.renewRestoreSession(ctx, restoreKey, &s); err != nil {
			return err
		}
		renewed = true
		// One attempt: Client.Do already retries the request itself.
		vault, err := c.fetchCredentialStorageVault(ctx, storageVaultID, actID, restoreKey, &s, &backoff.StopBackOff{})
		if err != nil {
			c.logger.Error("Error get credential ", zap.Error(err))
			return err
		}
		if err := storageVault.RefreshCredential(vault.Credential); err != nil {
			c.logger.Error("Error refresh credential ", zap.Error(err))
			return err
		}
		return nil
	}()

	restoreKey.mu.Lock()
	// Keep a renewed session even if a later step failed: the old one may be revoked. A
	// failed renewal leaves the current session as it was.
	if renewed {
		restoreKey.CreatedAt, restoreKey.RestoreSessionKey = s.createdAt, s.key
	}
	if err == nil {
		restoreKey.gen++
	}
	restoreKey.refreshing = nil
	restoreKey.mu.Unlock()
	close(ch)
	return err
}

// credentialStorageVaultPath API
func (c *Client) credentialStorageVaultPath(storageVaultID string, actionID string) string {
	return fmt.Sprintf("/agent/storage_vaults/%s/credential?action_id=%s", storageVaultID, actionID)
}

// GetCredentialStorageVault get a new credential with backend credential not constant.
// It is safe to call while restore goroutines share restoreKey.
func (c *Client) GetCredentialStorageVault(storageVaultID string, actionID string, restoreKey *AuthRestore) (*StorageVault, error) {
	bo := backoff.NewExponentialBackOff()
	bo.MaxInterval = maxRetry
	bo.MaxElapsedTime = maxRetry
	if restoreKey == nil {
		return c.fetchCredentialStorageVault(context.Background(), storageVaultID, actionID, nil, nil, bo)
	}
	old := restoreKey.session()
	s := old
	vault, err := c.fetchCredentialStorageVault(context.Background(), storageVaultID, actionID, restoreKey, &s, bo)
	restoreKey.replaceSession(old, s)
	return vault, err
}

// fetchCredentialStorageVault requests the vault credential. With restoreKey set, s carries
// the session headers and is renewed in place on 401; bo controls retries of the whole
// request on top of the retries Client.Do does by itself.
func (c *Client) fetchCredentialStorageVault(ctx context.Context, storageVaultID string, actionID string, restoreKey *AuthRestore, s *restoreSession, bo backoff.BackOff) (*StorageVault, error) {
	if restoreKey == nil {
		req, err := c.NewRequest(http.MethodGet, c.credentialStorageVaultPath(storageVaultID, actionID), nil)
		if err != nil {
			c.logger.Error("err ", zap.Error(err))
			return nil, err
		}
		resp, err := c.Do(req.WithContext(ctx))
		if err != nil {
			c.logger.Error("err ", zap.Error(err))
			return nil, err
		}
		return c.decodeStorageVault(resp)
	}

	// After renewing the session on 401 the request is retried once at once with the new
	// session, independent of bo: the caller may allow no retries at all (bo is StopBackOff
	// when refreshing), which would otherwise turn a renewed session into a failure.
	renewedOn401 := false
	for {
		req, err := c.NewRequest(http.MethodGet, c.credentialStorageVaultPath(storageVaultID, actionID), nil)
		if err != nil {
			c.logger.Error("err ", zap.Error(err))
			return nil, err
		}
		req = req.WithContext(ctx)
		req.Header.Add("X-Session-Created-At", s.createdAt)
		req.Header.Add("X-Restore-Session-Key", s.key)

		// 401 must come back as a response: Do would retry it for minutes and return a
		// plain error, so an expired session would never be renewed below.
		resp, err := c.doRetry(req, http.StatusUnauthorized)
		if err == nil && resp.StatusCode == http.StatusOK {
			return c.decodeStorageVault(resp)
		}
		if err != nil {
			c.logger.Error("err ", zap.Error(err))
		} else {
			err = fmt.Errorf("GetCredentialStorageVault: status %d", resp.StatusCode)
			if resp.StatusCode == http.StatusUnauthorized {
				c.logger.Sugar().Info("GetRestoreSessionKey access denied: ", resp.StatusCode)
				if rerr := c.renewRestoreSession(ctx, restoreKey, s); rerr != nil {
					_ = resp.Body.Close()
					return nil, rerr
				}
				if !renewedOn401 {
					renewedOn401 = true
					_ = resp.Body.Close()
					continue
				}
			}
			_ = resp.Body.Close()
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}

		d := bo.NextBackOff()
		if d == backoff.Stop {
			c.logger.Debug("GetCredentialStorageVault. Retry time out")
			return nil, err
		}
		c.logger.Sugar().Info("GetCredentialStorageVault. Retry in ", d)
		if serr := sleepCtx(ctx, d); serr != nil {
			return nil, serr
		}
	}
}

func (c *Client) decodeStorageVault(resp *http.Response) (*StorageVault, error) {
	if resp == nil {
		return nil, errors.New("GetCredentialStorageVault: nil response")
	}
	if err := checkResponse(resp); err != nil {
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

// PutObject stores the data to the storage vault.
// PutObject stores data in the storage vault, retrying until maxRetry elapses or ctx is
// cancelled.
func (c *Client) PutObject(ctx context.Context, storageVault storage_vault.StorageVault, key string, data []byte) error {
	var err error
	bo := backoff.NewExponentialBackOff()
	bo.MaxInterval = maxRetry
	bo.MaxElapsedTime = maxRetry

	for {
		err = storageVault.PutObject(ctx, key, data)
		if err == nil {
			break
		}
		var aerr smithy.APIError; if errors.As(err, &aerr) {
			if (aerr.ErrorCode() == "Forbidden" || aerr.ErrorCode() == "AccessDenied") && storageVault.Type().CredentialType == "DEFAULT" {
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
		if serr := sleepCtx(ctx, d); serr != nil {
			return serr
		}
	}
	return err
}

// GetObject downloads the object by name in storage vault. Retries stop when ctx is
// cancelled.
func (c *Client) GetObject(ctx context.Context, storageVault storage_vault.StorageVault, key string, restoreKey *AuthRestore) ([]byte, error) {
	bo := backoff.NewExponentialBackOff()
	bo.MaxInterval = maxRetry
	bo.MaxElapsedTime = maxRetry
	retriedAfterRefresh := false

	for {
		gen := restoreKey.generation()
		data, err := storageVault.GetObject(ctx, key)
		if err == nil {
			return data, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if isAccessDenied(err) {
			if storageVault.Type().CredentialType != "DEFAULT" {
				c.logAccessDeniedNotRetried(storageVault, key, err)
				return nil, err
			}
			if rerr := c.refreshVaultCredential(ctx, storageVault, restoreKey, gen); rerr != nil {
				return nil, rerr
			}
			if !retriedAfterRefresh {
				// Fresh credential: try again now instead of sleeping the backoff.
				retriedAfterRefresh = true
				continue
			}
		}

		c.logger.Debug("GetObject error. Retrying")
		d := bo.NextBackOff()
		if d == backoff.Stop {
			c.logger.Debug("GetObject error. Retry time out")
			return nil, err
		}
		c.logger.Sugar().Info("GetObject error. Retry in ", d)
		if serr := sleepCtx(ctx, d); serr != nil {
			return nil, serr
		}
	}
}

// GetObjectTo streams the object body from storage_vault directly into dst at byte offset
// off and returns the number of bytes written.
//
//   - Each retry restarts the write at off, so a transfer that breaks mid-stream (e.g.
//     connection reset during io.Copy) cannot leave dst misaligned.
//   - When wantLen >= 0, writes are confined to [off, off+wantLen): an object longer than
//     that never touches the neighbouring chunk's bytes, and any length other than wantLen
//     is reported as ErrObjectSizeMismatch. Pass -1 when the size is unknown.
//   - Waiting between retries stops as soon as ctx is cancelled.
//   - Errors that cannot succeed on retry (missing object, size mismatch, a failure writing
//     to dst such as a full disk) are returned at once instead of being retried for minutes.
//   - On access denied, the restore session and vault credential of a DEFAULT vault are
//     refreshed once for all goroutines sharing restoreKey; any other vault fails at once.
func (c *Client) GetObjectTo(ctx context.Context, storageVault storage_vault.StorageVault, key string, restoreKey *AuthRestore, dst io.WriterAt, off int64, wantLen int64) (int64, error) {
	bo := backoff.NewExponentialBackOff()
	bo.MaxInterval = maxRetry
	bo.MaxElapsedTime = maxRetry

	retriedAfterRefresh := false
	w := &chunkWriterAt{dst: dst, lo: off, hi: -1}
	if wantLen >= 0 {
		w.hi = off + wantLen
	}

	for {
		gen := restoreKey.generation()
		n, err := storageVault.GetObjectTo(ctx, key, w, off)
		if err == nil {
			if wantLen >= 0 && n != wantLen {
				return n, fmt.Errorf("%w: key %s: got %d bytes, want %d", ErrObjectSizeMismatch, key, n, wantLen)
			}
			return n, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return n, ctxErr
		}
		if !retryableGetError(err) {
			return n, fmt.Errorf("get object %s: %w", key, err)
		}
		if isAccessDenied(err) {
			// Only DEFAULT vaults get rotating credentials from the API; for any other vault
			// the same credential would be denied again, so retrying is pointless.
			if storageVault.Type().CredentialType != "DEFAULT" {
				c.logAccessDeniedNotRetried(storageVault, key, err)
				return n, fmt.Errorf("get object %s: %w", key, err)
			}
			if rerr := c.refreshVaultCredential(ctx, storageVault, restoreKey, gen); rerr != nil {
				return 0, rerr
			}
			if !retriedAfterRefresh {
				// Fresh credential: try again now instead of sleeping the backoff. Only once,
				// so a credential that keeps being refused falls back to the backoff.
				retriedAfterRefresh = true
				continue
			}
		}

		d := bo.NextBackOff()
		if d == backoff.Stop {
			return 0, fmt.Errorf("get object %s: %w", key, err)
		}
		c.logger.Sugar().Info("GetObjectTo error. Retry in ", d)
		if serr := sleepCtx(ctx, d); serr != nil {
			return 0, serr
		}
	}
}

// ErrObjectSizeMismatch is returned by GetObjectTo when the object delivered by the storage
// backend is shorter or longer than the length recorded in the index.
var ErrObjectSizeMismatch = errors.New("storage_vault: object length mismatch")

// localWriteError marks a failure of the destination writer (disk full, closed file...), as
// opposed to a failure reading from storage. Retrying the download cannot fix it.
type localWriteError struct{ err error }

func (e *localWriteError) Error() string { return "write restored data: " + e.err.Error() }
func (e *localWriteError) Unwrap() error { return e.err }

// chunkWriterAt confines writes for one chunk to [lo, hi) of dst (hi < 0 means no upper
// bound) and tags errors coming from dst as localWriteError.
type chunkWriterAt struct {
	dst    io.WriterAt
	lo, hi int64
}

func (w *chunkWriterAt) WriteAt(p []byte, pos int64) (int, error) {
	if pos < w.lo {
		return 0, fmt.Errorf("chunk write at %d before chunk start %d", pos, w.lo)
	}
	overflow := false
	if w.hi >= 0 && pos+int64(len(p)) > w.hi {
		overflow = true
		if pos >= w.hi {
			p = p[:0]
		} else {
			p = p[:w.hi-pos]
		}
	}
	n, err := w.dst.WriteAt(p, pos)
	if err != nil {
		return n, &localWriteError{err: err}
	}
	if overflow {
		return n, fmt.Errorf("%w: object is longer than %d bytes", ErrObjectSizeMismatch, w.hi-w.lo)
	}
	return n, nil
}

// logAccessDeniedNotRetried explains why an access-denied error on a vault whose credential
// is not issued by the backup API fails the download without retry.
func (c *Client) logAccessDeniedNotRetried(storageVault storage_vault.StorageVault, key string, err error) {
	code := ""
	var aerr smithy.APIError
	if errors.As(err, &aerr) {
		code = aerr.ErrorCode()
	}
	hint := "the vault credential is not valid for this object; check the credential configured for the vault"
	if code == "SignatureDoesNotMatch" {
		hint = "request signature rejected; most often the system clock is out of sync, otherwise the secret key is wrong"
	}
	c.logger.Error("Access denied on storage vault, not retrying",
		zap.String("credential_type", storageVault.Type().CredentialType),
		zap.String("code", code),
		zap.String("key", key),
		zap.String("hint", hint),
		zap.Error(err))
}

// isAccessDenied reports whether err means the credential was refused: any HTTP 403
// (whatever its code, also without an XML body), or one of the credential error codes S3
// returns with another status (400 for expired or invalid tokens).
func isAccessDenied(err error) bool {
	var se interface{ HTTPStatusCode() int }
	if errors.As(err, &se) && se.HTTPStatusCode() == http.StatusForbidden {
		return true
	}
	var aerr smithy.APIError
	if !errors.As(err, &aerr) {
		return false
	}
	switch aerr.ErrorCode() {
	case "Forbidden", "AccessDenied", "SignatureDoesNotMatch",
		"ExpiredToken", "TokenRefreshRequired", "InvalidAccessKeyId", "InvalidToken":
		return true
	}
	return false
}

// retryableGetError reports whether a failed download may succeed if tried again.
func retryableGetError(err error) bool {
	var we *localWriteError
	if errors.As(err, &we) || errors.Is(err, ErrObjectSizeMismatch) {
		return false
	}
	if isAccessDenied(err) {
		// Handled by the caller: refresh for DEFAULT vaults, fail otherwise.
		return true
	}
	var aerr smithy.APIError
	if errors.As(err, &aerr) {
		switch aerr.ErrorCode() {
		case "NoSuchKey", "NoSuchBucket", "InvalidObjectState":
			return false
		case "RequestTimeout", "SlowDown", "Throttling", "ThrottlingException":
			return true
		}
	}
	// Any other 4xx means the request itself is wrong (InvalidRange, InvalidArgument...),
	// and sending it again cannot help. 408 and 429 are the transient exceptions.
	var se interface{ HTTPStatusCode() int }
	if errors.As(err, &se) {
		code := se.HTTPStatusCode()
		if code >= 400 && code < 500 && code != http.StatusRequestTimeout && code != http.StatusTooManyRequests {
			return false
		}
	}
	return true
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
