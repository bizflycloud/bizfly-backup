package s3

import (
	"bytes"
	"io/ioutil"
	"math/rand"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	storage "github.com/aws/aws-sdk-go/service/s3"
	"github.com/cenkalti/backoff"

	"github.com/bizflycloud/bizfly-backup/pkg/backupapi"
	"github.com/bizflycloud/bizfly-backup/pkg/storage_vault"
)

type S3 struct {
	Id               string
	ActionID         string
	Name             string
	StorageBucket    string
	SecretRef        string
	PresignURL       string
	CredentialType   string
	StorageVaultType string
	Location         string
	Region           string
	S3Session        *storage.S3

	logger *zap.Logger
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

func NewS3Default(vault backupapi.StorageVault, actionID string) *S3 {

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
	}

	if s3.logger == nil {
		l, err := backupapi.WriteLog()
		if err != nil {
			return nil
		}
		s3.logger = l
	}

	cred := credentials.NewStaticCredentials(vault.Credential.AwsAccessKeyId, vault.Credential.AwsSecretAccessKey, vault.Credential.Token)
	_, err := cred.Get()
	if err != nil {
		s3.logger.Error("Bad credentials", zap.Error(err))
	}
	sess := storage.New(session.Must(session.NewSession(&aws.Config{
		DisableSSL:       aws.Bool(false),
		Credentials:      cred,
		Endpoint:         aws.String(vault.Credential.AwsLocation),
		Region:           aws.String(vault.Credential.Region),
		S3ForcePathStyle: aws.Bool(true),
	})))
	s3.S3Session = sess
	return s3

}

type HTTPClient struct{}

var (
	HttpClient = HTTPClient{}
)

const (
	maxRetry = 3 * time.Minute
)

func (s3 *S3) VerifyObject(key string) (bool, bool, string) {
	var integrity bool
	isExist, etag, _ := s3.HeadObject(key)
	if isExist {
		integrity = strings.Contains(etag, key)
	}
	return isExist, integrity, etag
}

func (s3 *S3) PutObject(key string, data []byte) error {
	var err error
	var once bool
	bo := backoff.NewExponentialBackOff()
	bo.MaxInterval = maxRetry
	for {
		isExist, integrity, etag := s3.VerifyObject(key)
		if isExist && !integrity {
			s3.logger.Info("Exist, not integrity, put object ", zap.String("key", key))
			_, err = s3.S3Session.PutObject(&storage.PutObjectInput{
				Bucket: aws.String(s3.StorageBucket),
				Key:    aws.String(key),
				Body:   bytes.NewReader(data),
			})
			if err == nil {
				break
			}
		} else if isExist && integrity {
			s3.logger.Info("Exist and integrity ", zap.String("etag", etag), zap.String("key", key))
			break
		} else if !isExist {
			s3.logger.Info("Not exist, put ", zap.String("key", key))
			_, err = s3.S3Session.PutObject(&storage.PutObjectInput{
				Bucket: aws.String(s3.StorageBucket),
				Key:    aws.String(key),
				Body:   bytes.NewReader(data),
			})

			if err == nil {
				break
			}
		}
		if aerr, ok := err.(awserr.Error); ok {
			if aerr.Error() == "Forbidden" {
				if once {
					s3.logger.Error("Return false cause in put object: ", zap.Error(err), zap.String("code", aerr.Code()), zap.String("key", key))
					return err
				}
				s3.logger.Info("Put object one more time")
				once = true
				rand.Seed(time.Now().UnixNano())
				n := rand.Intn(3) // n will be between 0 and 10
				time.Sleep(time.Duration(n) * time.Second)
			}
		}
		s3.logger.Debug("BackOff retry")
		d := bo.NextBackOff()
		if d == backoff.Stop {
			s3.logger.Debug("Retry time out")
			break
		}
		s3.logger.Sugar().Info("Retry in ", d)
		time.Sleep(d)
	}

	return err
}

func (s3 *S3) GetObject(key string) ([]byte, error) {
	var err error
	var once bool
	bo := backoff.NewExponentialBackOff()
	bo.MaxInterval = maxRetry
	var obj *storage.GetObjectOutput
	for {
		obj, err = s3.S3Session.GetObject(&storage.GetObjectInput{
			Bucket: aws.String(s3.StorageBucket),
			Key:    aws.String(key),
		})
		if err == nil {
			break
		}

		if aerr, ok := err.(awserr.Error); ok {
			if aerr.Error() == "Forbidden" {
				if once {
					s3.logger.Error("Return false cause in get object: ", zap.Error(err), zap.String("code", aerr.Code()), zap.String("key", key))
					return nil, err
				}
				s3.logger.Info("Get object one more time")
				once = true
				rand.Seed(time.Now().UnixNano())
				n := rand.Intn(3) // n will be between 0 and 10
				time.Sleep(time.Duration(n) * time.Second)
			} else {
				return nil, err
			}
		}
		s3.logger.Debug("BackOff retry")
		d := bo.NextBackOff()
		if d == backoff.Stop {
			s3.logger.Debug("Retry time out")
			break
		}
		s3.logger.Sugar().Info("Retry in ", d)
		time.Sleep(d)
	}

	body, err := ioutil.ReadAll(obj.Body)

	return body, err
}

func (s3 *S3) HeadObject(key string) (bool, string, error) {
	var err error
	var headObject *storage.HeadObjectOutput
	var once bool
	bo := backoff.NewExponentialBackOff()
	bo.MaxInterval = maxRetry
	for {
		headObject, err = s3.S3Session.HeadObject(&storage.HeadObjectInput{
			Bucket: aws.String(s3.StorageBucket),
			Key:    aws.String(key),
		})
		if err == nil {
			return true, *headObject.ETag, nil
		}

		if aerr, ok := err.(awserr.Error); ok {
			if aerr.Code() == "NotFound" {
				return false, "", err
			}

			if aerr.Code() == "Forbidden" {
				if once {
					s3.logger.Error("Return false cause in head object: ", zap.Error(err), zap.String("code", aerr.Code()), zap.String("key", key))
					return false, "", err
				}
				s3.logger.Sugar().Info("Head object one more time", key)
				once = true
				rand.Seed(time.Now().UnixNano())
				n := rand.Intn(3) // n will be between 0 and 10
				time.Sleep(time.Duration(n) * time.Second)
			}
		}
		s3.logger.Debug("BackOff retry")
		d := bo.NextBackOff()
		if d == backoff.Stop {
			s3.logger.Debug("Retry time out")
			break
		}
		s3.logger.Sugar().Info("Retry in ", d)
		time.Sleep(d)

	}
	return false, "", err
}

func (s3 *S3) RefreshCredential(credential storage_vault.Credential) error {
	cred := credentials.NewStaticCredentials(credential.AwsAccessKeyId, credential.AwsSecretAccessKey, credential.Token)
	_, err := cred.Get()
	if err != nil {
		s3.logger.Error("err ", zap.Error(err))
		return err
	}
	sess := storage.New(session.Must(session.NewSession(&aws.Config{
		DisableSSL:       aws.Bool(false),
		Credentials:      cred,
		Endpoint:         aws.String(s3.Location),
		Region:           aws.String(s3.Region),
		S3ForcePathStyle: aws.Bool(true),
	})))
	s3.S3Session = sess
	return nil
}
