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
	S3Session        *awss3.Client

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
var uploadKb, downloadKb int

var maxPartSize = int64(50 * 1024 * 1024)

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
	uploadKb, downloadKb = limitUpload, limitDownload

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

	// wrap the transport so that the throughput via HTTP is limited
	lim := limiter.NewStaticLimiter(limitUpload, limitDownload)
	rt = lim.Transport(rt)

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
	s3.S3Session = client

	go s3.warmConnection()

	return s3, nil
}

func (s3 *S3) warmConnection() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = s3.S3Session.HeadBucket(ctx, &awss3.HeadBucketInput{
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

func (s3 *S3) VerifyObject(key string) (bool, bool, string, error) {
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
		isExist, etag, err = s3.HeadObject(key)
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
	}
	return isExist, integrity, etag, err
}

func (s3 *S3) PutObject(key string, data []byte) error {
	var err error
	var once bool
	bo := backoff.NewExponentialBackOff()
	bo.MaxInterval = maxRetry
	bo.MaxElapsedTime = maxRetry
	for {
		isExist, integrity, _, _ := s3.VerifyObject(key)
		if isExist {
			if !integrity {
				if int64(len(data)) > maxPartSize {
					err = s3.putObjectMultiPart(key, data)
				} else {
					_, err = s3.S3Session.PutObject(context.Background(), &awss3.PutObjectInput{
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
				err = s3.putObjectMultiPart(key, data)
			} else {
				_, err = s3.S3Session.PutObject(context.Background(), &awss3.PutObjectInput{
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
				time.Sleep(time.Duration(n) * time.Second)
			}
		}
		s3.logger.Debug("PutObject error. Retrying")
		d := bo.NextBackOff()
		if d == backoff.Stop {
			s3.logger.Debug("PutObject error. Retry time out")
			break
		}
		s3.logger.Sugar().Info("PutObject error. Retry in ", d)
		time.Sleep(d)
	}

	return err
}

func (s3 *S3) putObjectMultiPart(key string, data []byte) error {
	respMPU, err := s3.createMultiPartUpload(key)
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
			completedPart, perr := s3.uploadPart(respMPU, data[curr:curr+partLength], partNumber)
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
		if aerr := s3.abortMultiPartUpload(respMPU); aerr != nil {
			s3.logger.Sugar().Error(aerr.Error())
			return aerr
		}
		return firstErr
	}

	completeResponse, err := s3.completeMultiPartUpload(respMPU, completedParts)
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

func (s3 *S3) GetObject(key string) ([]byte, error) {
	var err error
	var once bool
	bo := backoff.NewExponentialBackOff()
	bo.MaxInterval = maxRetry
	bo.MaxElapsedTime = maxRetry
	var obj *awss3.GetObjectOutput
	for {
		obj, err = s3.S3Session.GetObject(context.Background(), &awss3.GetObjectInput{
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
				if s3.refreshCredentialIfNeeded(key, "GetObject") {
					continue
				}
				if once {
					s3.logger.Error("Return false cause in get object: ", zap.Error(err), zap.String("code", code), zap.String("key", key))
					return nil, err
				}
				s3.logger.Sugar().Info("Get object one more time ", key)
				once = true
				n := rand.Intn(3)
				time.Sleep(time.Duration(n) * time.Second)
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
		time.Sleep(d)
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

// GetObjectTo streams the object directly to dst without buffering the full payload in memory.
// Falls back to GetObject's retry/credential refresh semantics for the request itself.
func (s3 *S3) GetObjectTo(key string, dst io.Writer) (int64, error) {
	var err error
	var once bool
	bo := backoff.NewExponentialBackOff()
	bo.MaxInterval = maxRetry
	bo.MaxElapsedTime = maxRetry
	var obj *awss3.GetObjectOutput
	for {
		obj, err = s3.S3Session.GetObject(context.Background(), &awss3.GetObjectInput{
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
				return 0, err
			}
			s3.logger.Sugar().Errorf("GetObjectTo error: %s %s", code, apiErrMessage(err))
			if code == "AccessDenied" || code == "Forbidden" || code == "SignatureDoesNotMatch" {
				if s3.refreshCredentialIfNeeded(key, "GetObjectTo") {
					continue
				}
				if once {
					return 0, err
				}
				once = true
				time.Sleep(time.Duration(rand.Intn(3)) * time.Second)
			} else {
				return 0, err
			}
		}
		d := bo.NextBackOff()
		if d == backoff.Stop {
			break
		}
		time.Sleep(d)
	}

	if err != nil {
		return 0, err
	}
	if obj == nil || obj.Body == nil {
		return 0, errors.New("s3: GetObjectTo returned nil body")
	}
	defer obj.Body.Close()
	return io.Copy(dst, obj.Body)
}

func (s3 *S3) HeadObject(key string) (bool, string, error) {
	var err error
	var headObject *awss3.HeadObjectOutput
	var once bool
	bo := backoff.NewExponentialBackOff()
	bo.MaxInterval = maxRetry
	bo.MaxElapsedTime = maxRetry
	for {
		headObject, err = s3.S3Session.HeadObject(context.Background(), &awss3.HeadObjectInput{
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
				time.Sleep(time.Duration(n) * time.Second)
			}
		}
		s3.logger.Debug("Head object error. Retrying")
		d := bo.NextBackOff()
		if d == backoff.Stop {
			s3.logger.Debug("Head object error. Retry time out", zap.Error(err))
			break
		}
		s3.logger.Sugar().Info("Head object error. Retry in ", d)
		time.Sleep(d)

	}
	return false, "", err
}

func (s3 *S3) createMultiPartUpload(key string) (*awss3.CreateMultipartUploadOutput, error) {
	var err error
	var once bool
	bo := backoff.NewExponentialBackOff()
	bo.MaxInterval = maxRetry
	bo.MaxElapsedTime = maxRetry
	for {
		resp, cerr := s3.S3Session.CreateMultipartUpload(context.Background(), &awss3.CreateMultipartUploadInput{
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
				time.Sleep(time.Duration(n) * time.Second)
			}
		}
		s3.logger.Debug("CreateMultipartUpload  error. Retrying")
		d := bo.NextBackOff()
		if d == backoff.Stop {
			s3.logger.Debug("CreateMultipartUpload error. Retry time out", zap.Error(err))
			break
		}
		s3.logger.Sugar().Info("CreateMultipartUpload error. Retry in ", d)
		time.Sleep(d)

	}
	return nil, err
}

func (s3 *S3) completeMultiPartUpload(mpuOut *awss3.CreateMultipartUploadOutput, parts []s3types.CompletedPart) (*awss3.CompleteMultipartUploadOutput, error) {
	var err error
	var once bool
	bo := backoff.NewExponentialBackOff()
	bo.MaxInterval = maxRetry
	bo.MaxElapsedTime = maxRetry
	for {
		resp, cerr := s3.S3Session.CompleteMultipartUpload(context.Background(), &awss3.CompleteMultipartUploadInput{
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
				time.Sleep(time.Duration(n) * time.Second)
			}
		}
		s3.logger.Debug("CompleteMultipartUpload  error. Retrying")
		d := bo.NextBackOff()
		if d == backoff.Stop {
			s3.logger.Debug("CompleteMultipartUpload error. Retry time out", zap.Error(err))
			break
		}
		s3.logger.Sugar().Info("CompleteMultipartUpload error. Retry in ", d)
		time.Sleep(d)

	}
	return nil, err
}

func (s3 *S3) abortMultiPartUpload(mpuOut *awss3.CreateMultipartUploadOutput) error {
	var err error
	var once bool
	bo := backoff.NewExponentialBackOff()
	bo.MaxInterval = maxRetry
	bo.MaxElapsedTime = maxRetry
	for {
		_, aerr := s3.S3Session.AbortMultipartUpload(context.Background(), &awss3.AbortMultipartUploadInput{
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
				time.Sleep(time.Duration(n) * time.Second)
			}
		}
		s3.logger.Debug("AbortMultipartUpload  error. Retrying")
		d := bo.NextBackOff()
		if d == backoff.Stop {
			s3.logger.Debug("AbortMultipartUpload error. Retry time out", zap.Error(err))
			break
		}
		s3.logger.Sugar().Info("AbortMultipartUpload error. Retry in ", d)
		time.Sleep(d)

	}
	return err
}

func (s3 *S3) uploadPart(resp *awss3.CreateMultipartUploadOutput, fileBytes []byte, partNum int32) (s3types.CompletedPart, error) {
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
		uploadResult, err := s3.S3Session.UploadPart(context.Background(), partInput)
		if err != nil {
			if tryNum == maxRetries {
				return s3types.CompletedPart{}, err
			}
			s3.logger.Sugar().Infof("Retrying to upload part #%v", partNum)
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

	if uploadKb == 0 {
		uploadKb = viper.GetInt("limit_upload")
	}
	if downloadKb == 0 {
		downloadKb = viper.GetInt("limit_download")
	}

	// wrap the transport so that the throughput via HTTP is limited
	lim := limiter.NewStaticLimiter(uploadKb, downloadKb)
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
	s3.S3Session = client
	s3.logger.Info("Refresh credential success")
	return nil
}
