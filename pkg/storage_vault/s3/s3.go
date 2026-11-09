package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/cenkalti/backoff"
	"github.com/spf13/viper"

	"github.com/bizflycloud/bizfly-backup/pkg/backupapi"
	"github.com/bizflycloud/bizfly-backup/pkg/limiter"
	"github.com/bizflycloud/bizfly-backup/pkg/storage_vault"
)

type S3 struct {
	Id               string
	ActionID         string
	Name             string
	StorageBucket    string
	SecretRef        string
	CredentialType   string
	StorageVaultType string
	Location         string
	Region           string
	// session is the S3 client. RefreshCredential swaps it while restore and backup
	// goroutines are using it, so it is only accessed through client() / setClient().
	session atomic.Pointer[awss3.Client]
	// transport is the base HTTP transport behind session (before the rate limiter wraps
	// it, which hides CloseIdleConnections). RefreshCredential closes the previous one's
	// idle connections when it replaces the client.
	transport atomic.Pointer[http.Transport]
	// lim limits this vault's throughput. It is created once and reused by
	// RefreshCredential, so a refresh neither resets the rate budget nor picks up another
	// vault's limits.
	lim limiter.Limiter

	// existsCache memoizes successful HEAD-existence checks within a single
	// agent process to avoid redundant S3 round-trips for chunks that recur
	// across files (intra-run dedup). Key = object key, value = true (exists).
	// Only positive results are cached - missing objects re-check each time so
	// concurrent uploads can race-resolve to a single PUT correctly.
	existsCache sync.Map

	logger       *zap.Logger
	backupClient *backupapi.Client
}

func (s3 *S3) Type() storage_vault.Type {
	tpe := storage_vault.Type{
		StorageVaultType: s3.StorageVaultType,
		CredentialType:   s3.CredentialType,
	}
	return tpe
}

func (s3 *S3) ID() (string, string) {
	return s3.Id, s3.ActionID
}

var _ storage_vault.StorageVault = (*S3)(nil)

var maxPartSize = int64(50 * 1024 * 1024)

// sleepCtx waits for d, returning early with ctx's error when ctx is cancelled.
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

// client returns the current S3 client.
func (s3 *S3) client() *awss3.Client { return s3.session.Load() }

// setClient replaces the S3 client, e.g. after a credential refresh.
func (s3 *S3) setClient(c *awss3.Client) { s3.session.Store(c) }

// apiErrCode extracts the API error code from an aws-sdk-go-v2 error.
// Returns empty string when the error is not a smithy API error.
func apiErrCode(err error) string {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode()
	}
	return ""
}

func apiErrMessage(err error) string {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorMessage()
	}
	return ""
}

func newS3Client(endpoint, region, accessKey, secretKey, token string, httpClient *http.Client, logger *zap.Logger) (*awss3.Client, error) {
	provider := credentials.NewStaticCredentialsProvider(accessKey, secretKey, token)
	if _, err := provider.Retrieve(context.Background()); err != nil {
		logger.Error("Bad credentials", zap.Error(err))
	}

	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(provider),
		awsconfig.WithHTTPClient(httpClient),
	)
	if err != nil {
		return nil, err
	}

	client := awss3.NewFromConfig(cfg, func(o *awss3.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
		o.UsePathStyle = true
		o.EndpointOptions.DisableHTTPS = false
	})
	return client, nil
}

func NewS3Default(vault backupapi.StorageVault, actionID string, limitUpload, limitDownload int, backupClient *backupapi.Client) (*S3, error) {
	s3 := &S3{
		Id:               vault.ID,
		ActionID:         actionID,
		Name:             vault.Name,
		StorageBucket:    vault.StorageBucket,
		SecretRef:        vault.SecretRef,
		CredentialType:   vault.CredentialType,
		StorageVaultType: vault.StorageVaultType,
		Location:         vault.Credential.AwsLocation,
		Region:           vault.Credential.Region,
		backupClient:     backupClient,
	}

	if s3.logger == nil {
		l, err := backupapi.WriteLog()
		if err != nil {
			return nil, err
		}
		s3.logger = l
	}

	// using a Custom HTTP Transport
	rt, err := storage_vault.Transport(storage_vault.TransportOptions{
		Connect:          30 * time.Second,
		ExpectContinue:   1 * time.Second,
		IdleConn:         90 * time.Second,
		ConnKeepAlive:    30 * time.Second,
		MaxAllIdleConns:  100,
		MaxHostIdleConns: 100,
		ResponseHeader:   10 * time.Second,
		TLSHandshake:     10 * time.Second,
	})
	if err != nil {
		s3.logger.Error("Got an error creating custom HTTP client", zap.Error(err))
	}

	base, _ := rt.(*http.Transport)
	// wrap the transport so that the throughput via HTTP is limited
	s3.lim = limiter.NewStaticLimiter(limitUpload, limitDownload)
	rt = s3.lim.Transport(rt)

	client, err := newS3Client(
		vault.Credential.AwsLocation,
		vault.Credential.Region,
		vault.Credential.AwsAccessKeyId,
		vault.Credential.AwsSecretAccessKey,
		vault.Credential.Token,
		&http.Client{Transport: rt},
		s3.logger,
	)
	if err != nil {
		return nil, err
	}
	s3.setClient(client)
	s3.transport.Store(base)

	go s3.warmConnection()

	return s3, nil
}

func (s3 *S3) warmConnection() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = s3.client().HeadBucket(ctx, &awss3.HeadBucketInput{
		Bucket: aws.String(s3.StorageBucket),
	})
}

type HTTPClient struct{}

var (
	HttpClient = HTTPClient{}
)

const (
	maxRetry = 3 * time.Minute
)

func (s3 *S3) VerifyObject(ctx context.Context, key string) (bool, bool, string, error) {
	if _, ok := s3.existsCache.Load(key); ok {
		return true, true, "", nil
	}
	var isExist bool
	var integrity bool
	var etag string
	var err error
	bo := backoff.NewExponentialBackOff()
	bo.MaxInterval = maxRetry
	bo.MaxElapsedTime = maxRetry

	for {
		isExist, etag, err = s3.HeadObject(ctx, key)
		if err == nil {
			if isExist {
				integrity = strings.Contains(etag, key)
				if integrity {
					s3.existsCache.Store(key, struct{}{})
				}
			}
			break
		}
		if code := apiErrCode(err); code != "" {
			if code == "NotFound" {
				err = nil
				break
			}
			s3.logger.Sugar().Errorf("VerifyObject error: %s %s", code, apiErrMessage(err))
			if (code == "AccessDenied" || code == "Forbidden" || code == "SignatureDoesNotMatch") && s3.Type().CredentialType == "DEFAULT" {
				s3.logger.Sugar().Info("GetCredential in head object ", key)
				storageVaultID, actID := s3.ID()
				vault, verr := s3.backupClient.GetCredentialStorageVault(storageVaultID, actID, nil)
				if verr != nil {
					s3.logger.Error("Error get credential", zap.Error(verr))
					break
				}

				if rerr := s3.RefreshCredential(vault.Credential); rerr != nil {
					s3.logger.Error("Error refresh credential ", zap.Error(rerr))
					break
				}
			}
		}

		s3.logger.Error("VerifyObject. Retrying", zap.Error(err))
		d := bo.NextBackOff()
		if d == backoff.Stop {
			s3.logger.Debug("VerifyObject. Retry time out")
			break
		}
		s3.logger.Sugar().Info("VerifyObject. Retry in ", d)
		if serr := sleepCtx(ctx, d); serr != nil {
			return isExist, integrity, etag, serr
		}
	}
	return isExist, integrity, etag, err
}

func (s3 *S3) PutObject(ctx context.Context, key string, data []byte) error {
	var err error
	var once bool
	bo := backoff.NewExponentialBackOff()
	bo.MaxInterval = maxRetry
	bo.MaxElapsedTime = maxRetry
	for {
		isExist, integrity, _, _ := s3.VerifyObject(ctx, key)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if isExist {
			if !integrity {
				if int64(len(data)) > maxPartSize {
					err = s3.putObjectMultiPart(ctx, key, data)
				} else {
					_, err = s3.client().PutObject(ctx, &awss3.PutObjectInput{
						Bucket: aws.String(s3.StorageBucket),
						Key:    aws.String(key),
						Body:   bytes.NewReader(data),
					})
				}

				if err == nil {
					s3.existsCache.Store(key, struct{}{})
					break
				}
			} else {
				break
			}
		} else {
			if int64(len(data)) > maxPartSize {
				err = s3.putObjectMultiPart(ctx, key, data)
			} else {
				_, err = s3.client().PutObject(ctx, &awss3.PutObjectInput{
					Bucket: aws.String(s3.StorageBucket),
					Key:    aws.String(key),
					Body:   bytes.NewReader(data),
				})
			}
			if err == nil {
				s3.existsCache.Store(key, struct{}{})
				break
			}
		}
		if code := apiErrCode(err); code != "" {
			s3.logger.Sugar().Errorf("PutObject error: %s %s", code, apiErrMessage(err))
			if code == "AccessDenied" || code == "Forbidden" || code == "SignatureDoesNotMatch" {
				if once {
					s3.logger.Error("Return false cause in put object: ", zap.Error(err), zap.String("code", code), zap.String("key", key))
					return err
				}
				s3.logger.Info("Put object one more time")
				once = true
				n := rand.Intn(3)
				if serr := sleepCtx(ctx, time.Duration(n)*time.Second); serr != nil {
					return serr
				}
			}
		}
		s3.logger.Debug("PutObject error. Retrying")
		d := bo.NextBackOff()
		if d == backoff.Stop {
			s3.logger.Debug("PutObject error. Retry time out")
			break
		}
		s3.logger.Sugar().Info("PutObject error. Retry in ", d)
		if serr := sleepCtx(ctx, d); serr != nil {
			return serr
		}
	}

	return err
}

func (s3 *S3) putObjectMultiPart(ctx context.Context, key string, data []byte) error {
	respMPU, err := s3.createMultiPartUpload(ctx, key)
	if err != nil {
		return err
	}
	totalLen := int64(len(data))
	numParts := int((totalLen + maxPartSize - 1) / maxPartSize)
	completedParts := make([]s3types.CompletedPart, numParts)

	const maxParallelParts = 4
	sem := make(chan struct{}, maxParallelParts)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for i := 0; i < numParts; i++ {
		curr := int64(i) * maxPartSize
		partLength := maxPartSize
		if remaining := totalLen - curr; remaining < maxPartSize {
			partLength = remaining
		}
		partNumber := int32(i + 1)
		idx := i

		mu.Lock()
		if firstErr != nil {
			mu.Unlock()
			break
		}
		mu.Unlock()

		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			completedPart, perr := s3.uploadPart(ctx, respMPU, data[curr:curr+partLength], partNumber)
			if perr != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = perr
				}
				mu.Unlock()
				return
			}
			completedParts[idx] = completedPart
		}()
	}
	wg.Wait()

	if firstErr != nil {
		fmt.Println(firstErr.Error())
		// Abort even when ctx is cancelled, so no orphaned parts stay billed in the bucket.
		if aerr := s3.abortMultiPartUpload(context.WithoutCancel(ctx), respMPU); aerr != nil {
			s3.logger.Sugar().Error(aerr.Error())
			return aerr
		}
		return firstErr
	}

	completeResponse, err := s3.completeMultiPartUpload(ctx, respMPU, completedParts)
	if err != nil {
		s3.logger.Sugar().Error(err.Error())
		return err
	}

	s3.logger.Sugar().Infof("Successfully uploaded file: %s", aws.ToString(completeResponse.Key))
	return nil
}

// refreshCredentialIfNeeded attempts to renew temporary S3 credentials when the
// server rejects a request with AccessDenied/Forbidden/SignatureDoesNotMatch.
// Returns true if credentials were refreshed and the caller should retry the
// request with the new session; false when no refresh happened (not a DEFAULT
// credential type or refresh itself failed).
func (s3 *S3) refreshCredentialIfNeeded(key, op string) bool {
	if s3.Type().CredentialType != "DEFAULT" {
		return false
	}
	s3.logger.Sugar().Infof("%s: refreshing credential for key %s", op, key)
	storageVaultID, actID := s3.ID()
	vault, verr := s3.backupClient.GetCredentialStorageVault(storageVaultID, actID, nil)
	if verr != nil {
		s3.logger.Error("Error get credential", zap.Error(verr))
		return false
	}
	if rerr := s3.RefreshCredential(vault.Credential); rerr != nil {
		s3.logger.Error("Error refresh credential ", zap.Error(rerr))
		return false
	}
	return true
}

func (s3 *S3) GetObject(ctx context.Context, key string) ([]byte, error) {
	var err error
	var once, refreshed bool
	bo := backoff.NewExponentialBackOff()
	bo.MaxInterval = maxRetry
	bo.MaxElapsedTime = maxRetry
	var obj *awss3.GetObjectOutput
	for {
		obj, err = s3.client().GetObject(ctx, &awss3.GetObjectInput{
			Bucket: aws.String(s3.StorageBucket),
			Key:    aws.String(key),
		})
		if err == nil && obj != nil {
			break
		}
		if err == nil && obj == nil {
			err = errors.New("s3: GetObject returned nil response")
		}

		if code := apiErrCode(err); code != "" {
			if code == "NoSuchKey" {
				return nil, err
			}

			s3.logger.Sugar().Errorf("GetObject error: %s %s", code, apiErrMessage(err))
			if code == "AccessDenied" || code == "Forbidden" || code == "SignatureDoesNotMatch" {
				// Refresh at most once: a credential that is refused again after a
				// successful refresh used to loop here without any wait.
				if !refreshed && s3.refreshCredentialIfNeeded(key, "GetObject") {
					refreshed = true
					continue
				}
				if once {
					s3.logger.Error("Return false cause in get object: ", zap.Error(err), zap.String("code", code), zap.String("key", key))
					return nil, err
				}
				s3.logger.Sugar().Info("Get object one more time ", key)
				once = true
				n := rand.Intn(3)
				if serr := sleepCtx(ctx, time.Duration(n)*time.Second); serr != nil {
					return nil, serr
				}
			} else {
				return nil, err
			}
		}
		s3.logger.Debug("GetObject error. Retrying")
		d := bo.NextBackOff()
		if d == backoff.Stop {
			s3.logger.Debug("GetObject error. Retry time out")
			break
		}
		s3.logger.Sugar().Info("GetObject error. Retry in ", d)
		if serr := sleepCtx(ctx, d); serr != nil {
			return nil, serr
		}
	}

	if err != nil {
		return nil, err
	}
	if obj == nil || obj.Body == nil {
		return nil, errors.New("s3: GetObject returned nil body")
	}

	body, err := io.ReadAll(obj.Body)
	_ = obj.Body.Close()
	return body, err
}

// GetObjectTo streams the object directly into dst at offset off without buffering the full
// payload in memory. The write position is derived from off on every call, so a caller may
// retry with the same dst after a partial copy without corrupting the destination.
//
// It makes exactly one request and returns any error as-is, including access denied: retry,
// backoff and credential refresh belong to the caller (backupapi.Client.GetObjectTo), which
// refreshes once for all concurrent downloads instead of once per goroutine.
func (s3 *S3) GetObjectTo(ctx context.Context, key string, dst io.WriterAt, off int64) (int64, error) {
	obj, err := s3.client().GetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(s3.StorageBucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if code := apiErrCode(err); code != "" {
			s3.logger.Sugar().Errorf("GetObjectTo error: %s %s", code, apiErrMessage(err))
		}
		return 0, err
	}
	if obj == nil || obj.Body == nil {
		return 0, errors.New("s3: GetObjectTo returned nil body")
	}
	defer obj.Body.Close()
	return io.Copy(io.NewOffsetWriter(dst, off), obj.Body)
}

func (s3 *S3) HeadObject(ctx context.Context, key string) (bool, string, error) {
	var err error
	var headObject *awss3.HeadObjectOutput
	var once bool
	bo := backoff.NewExponentialBackOff()
	bo.MaxInterval = maxRetry
	bo.MaxElapsedTime = maxRetry
	for {
		headObject, err = s3.client().HeadObject(ctx, &awss3.HeadObjectInput{
			Bucket: aws.String(s3.StorageBucket),
			Key:    aws.String(key),
		})
		if err == nil {
			if headObject == nil {
				return false, "", errors.New("s3: HeadObject returned nil response")
			}
			return true, aws.ToString(headObject.ETag), nil
		}

		if code := apiErrCode(err); code != "" {
			if code == "NotFound" {
				return false, "", err
			}

			s3.logger.Sugar().Errorf("HeadObject error: %s %s", code, apiErrMessage(err))
			if code == "AccessDenied" || code == "Forbidden" {
				if once {
					s3.logger.Error("Return false cause in head object: ", zap.Error(err), zap.String("code", code), zap.String("key", key))
					return false, "", err
				}
				s3.logger.Sugar().Info("Head object one more time ", key)
				once = true
				n := rand.Intn(3)
				if serr := sleepCtx(ctx, time.Duration(n)*time.Second); serr != nil {
					return false, "", serr
				}
			}
		}
		s3.logger.Debug("Head object error. Retrying")
		d := bo.NextBackOff()
		if d == backoff.Stop {
			s3.logger.Debug("Head object error. Retry time out", zap.Error(err))
			break
		}
		s3.logger.Sugar().Info("Head object error. Retry in ", d)
		if serr := sleepCtx(ctx, d); serr != nil {
			return false, "", serr
		}

	}
	return false, "", err
}

func (s3 *S3) createMultiPartUpload(ctx context.Context, key string) (*awss3.CreateMultipartUploadOutput, error) {
	var err error
	var once bool
	bo := backoff.NewExponentialBackOff()
	bo.MaxInterval = maxRetry
	bo.MaxElapsedTime = maxRetry
	for {
		resp, cerr := s3.client().CreateMultipartUpload(ctx, &awss3.CreateMultipartUploadInput{
			Bucket: aws.String(s3.StorageBucket),
			Key:    aws.String(key),
		})
		err = cerr
		if err == nil {
			s3.logger.Sugar().Info("Created MultiPartUpload for ", key)
			return resp, nil
		}

		if code := apiErrCode(err); code != "" {
			if code == "NotFound" {
				return nil, err
			}

			s3.logger.Sugar().Errorf("CreateMultipartUpload error: %s %s", code, apiErrMessage(err))
			if code == "AccessDenied" || code == "Forbidden" {
				if once {
					s3.logger.Error("Return false cause in CreateMultipartUpload object: ", zap.Error(err), zap.String("code", code), zap.String("key", key))
					return nil, err
				}
				s3.logger.Sugar().Info("CreateMultipartUpload one more time ", key)
				once = true
				n := rand.Intn(3)
				if serr := sleepCtx(ctx, time.Duration(n)*time.Second); serr != nil {
					return nil, serr
				}
			}
		}
		s3.logger.Debug("CreateMultipartUpload  error. Retrying")
		d := bo.NextBackOff()
		if d == backoff.Stop {
			s3.logger.Debug("CreateMultipartUpload error. Retry time out", zap.Error(err))
			break
		}
		s3.logger.Sugar().Info("CreateMultipartUpload error. Retry in ", d)
		if serr := sleepCtx(ctx, d); serr != nil {
			return nil, serr
		}

	}
	return nil, err
}

func (s3 *S3) completeMultiPartUpload(ctx context.Context, mpuOut *awss3.CreateMultipartUploadOutput, parts []s3types.CompletedPart) (*awss3.CompleteMultipartUploadOutput, error) {
	var err error
	var once bool
	bo := backoff.NewExponentialBackOff()
	bo.MaxInterval = maxRetry
	bo.MaxElapsedTime = maxRetry
	for {
		resp, cerr := s3.client().CompleteMultipartUpload(ctx, &awss3.CompleteMultipartUploadInput{
			Bucket:   mpuOut.Bucket,
			Key:      mpuOut.Key,
			UploadId: mpuOut.UploadId,
			MultipartUpload: &s3types.CompletedMultipartUpload{
				Parts: parts,
			},
		})
		err = cerr
		if err == nil {
			s3.logger.Sugar().Info("Completed Multipart Upload ", aws.ToString(mpuOut.Key))
			return resp, nil
		}

		if code := apiErrCode(err); code != "" {
			if code == "NotFound" {
				return nil, err
			}

			s3.logger.Sugar().Errorf("CompleteMultipartUpload error: %s %s", code, apiErrMessage(err))
			if code == "AccessDenied" || code == "Forbidden" {
				if once {
					s3.logger.Error("Return false cause in CompleteMultipartUpload: ", zap.Error(err), zap.String("code", code), zap.String("key", aws.ToString(mpuOut.Key)))
					return nil, err
				}
				s3.logger.Sugar().Info("CompleteMultipartUpload one more time ", aws.ToString(mpuOut.Key))
				once = true
				n := rand.Intn(3)
				if serr := sleepCtx(ctx, time.Duration(n)*time.Second); serr != nil {
					return nil, serr
				}
			}
		}
		s3.logger.Debug("CompleteMultipartUpload  error. Retrying")
		d := bo.NextBackOff()
		if d == backoff.Stop {
			s3.logger.Debug("CompleteMultipartUpload error. Retry time out", zap.Error(err))
			break
		}
		s3.logger.Sugar().Info("CompleteMultipartUpload error. Retry in ", d)
		if serr := sleepCtx(ctx, d); serr != nil {
			return nil, serr
		}

	}
	return nil, err
}

func (s3 *S3) abortMultiPartUpload(ctx context.Context, mpuOut *awss3.CreateMultipartUploadOutput) error {
	var err error
	var once bool
	bo := backoff.NewExponentialBackOff()
	bo.MaxInterval = maxRetry
	bo.MaxElapsedTime = maxRetry
	for {
		_, aerr := s3.client().AbortMultipartUpload(ctx, &awss3.AbortMultipartUploadInput{
			Bucket:   mpuOut.Bucket,
			Key:      mpuOut.Key,
			UploadId: mpuOut.UploadId,
		})
		err = aerr
		if err == nil {
			s3.logger.Sugar().Info("AbortMultipartUpload Upload ", aws.ToString(mpuOut.Key))
			return nil
		}

		if code := apiErrCode(err); code != "" {
			if code == "NotFound" {
				return err
			}

			s3.logger.Sugar().Errorf("AbortMultipartUpload error: %s %s", code, apiErrMessage(err))
			if code == "AccessDenied" || code == "Forbidden" {
				if once {
					s3.logger.Error("Return false cause in AbortMultipartUpload: ", zap.Error(err), zap.String("code", code), zap.String("key", aws.ToString(mpuOut.Key)))
					return err
				}
				s3.logger.Sugar().Info("AbortMultipartUpload one more time ", aws.ToString(mpuOut.Key))
				once = true
				n := rand.Intn(3)
				if serr := sleepCtx(ctx, time.Duration(n)*time.Second); serr != nil {
					return serr
				}
			}
		}
		s3.logger.Debug("AbortMultipartUpload  error. Retrying")
		d := bo.NextBackOff()
		if d == backoff.Stop {
			s3.logger.Debug("AbortMultipartUpload error. Retry time out", zap.Error(err))
			break
		}
		s3.logger.Sugar().Info("AbortMultipartUpload error. Retry in ", d)
		if serr := sleepCtx(ctx, d); serr != nil {
			return serr
		}

	}
	return err
}

func (s3 *S3) uploadPart(ctx context.Context, resp *awss3.CreateMultipartUploadOutput, fileBytes []byte, partNum int32) (s3types.CompletedPart, error) {
	tryNum := 1
	maxRetries := 3
	partInput := &awss3.UploadPartInput{
		Body:          bytes.NewReader(fileBytes),
		Bucket:        resp.Bucket,
		Key:           resp.Key,
		PartNumber:    aws.Int32(partNum),
		UploadId:      resp.UploadId,
		ContentLength: aws.Int64(int64(len(fileBytes))),
	}

	for tryNum <= maxRetries {
		// A fresh reader per attempt: a failed attempt may have consumed the previous one,
		// and retrying with it would send an empty or truncated part.
		partInput.Body = bytes.NewReader(fileBytes)
		uploadResult, err := s3.client().UploadPart(ctx, partInput)
		if err != nil {
			if tryNum == maxRetries || ctx.Err() != nil {
				return s3types.CompletedPart{}, err
			}
			s3.logger.Sugar().Infof("Retrying to upload part #%v", partNum)
			if serr := sleepCtx(ctx, time.Duration(tryNum)*time.Second); serr != nil {
				return s3types.CompletedPart{}, serr
			}
			tryNum++
		} else {
			s3.logger.Sugar().Infof("Uploaded part #%v", partNum)
			return s3types.CompletedPart{
				ETag:       uploadResult.ETag,
				PartNumber: aws.Int32(partNum),
			}, nil
		}
	}
	return s3types.CompletedPart{}, nil
}

func (s3 *S3) RefreshCredential(credential storage_vault.Credential) error {
	// using a Custom HTTP Transport
	rt, err := storage_vault.Transport(storage_vault.TransportOptions{
		Connect:          30 * time.Second,
		ExpectContinue:   1 * time.Second,
		IdleConn:         90 * time.Second,
		ConnKeepAlive:    30 * time.Second,
		MaxAllIdleConns:  100,
		MaxHostIdleConns: 100,
		ResponseHeader:   10 * time.Second,
		TLSHandshake:     10 * time.Second,
	})
	if err != nil {
		s3.logger.Error("Got an error creating custom HTTP client", zap.Error(err))
	}

	base, _ := rt.(*http.Transport)
	// wrap the transport so that the throughput via HTTP is limited
	lim := s3.lim
	if lim == nil {
		// Vault not built by NewS3Default: fall back to the configured limits.
		lim = limiter.NewStaticLimiter(viper.GetInt("limit_upload"), viper.GetInt("limit_download"))
	}
	rt = lim.Transport(rt)

	client, err := newS3Client(
		s3.Location,
		s3.Region,
		credential.AwsAccessKeyId,
		credential.AwsSecretAccessKey,
		credential.Token,
		&http.Client{Transport: rt},
		s3.logger,
	)
	if err != nil {
		s3.logger.Error("err ", zap.Error(err))
		return err
	}
	s3.setClient(client)
	if old := s3.transport.Swap(base); old != nil {
		// Requests in flight on the old client keep their connections; only idle ones,
		// which would otherwise linger until they time out, are closed.
		old.CloseIdleConnections()
	}
	s3.logger.Info("Refresh credential success")
	return nil
}
