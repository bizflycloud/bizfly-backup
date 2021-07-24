package s3

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"math/rand"
	"time"

	"go.uber.org/zap"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	storage "github.com/aws/aws-sdk-go/service/s3"
	log "github.com/sirupsen/logrus"

	"github.com/bizflycloud/bizfly-backup/pkg/backupapi"
	"github.com/bizflycloud/bizfly-backup/pkg/volume"
)

type S3 struct {
	Id            string
	ActionID      string
	Name          string
	StorageBucket string
	SecretRef     string
	PresignURL    string
	StorageType   string
	VolumeType    string
	Location      string
	Region        string
	S3Session     *storage.S3

	logger *zap.Logger
}

func (s3 *S3) Type() volume.Type {
	tpe := volume.Type{
		VolumeType:  s3.VolumeType,
		StorageType: s3.StorageType,
	}
	return tpe
}

func (s3 *S3) ID() (string, string) {
	return s3.Id, s3.ActionID
}

var _ volume.StorageVolume = (*S3)(nil)

func NewS3Default(vol backupapi.Volume, actionID string) *S3 {

	s3 := &S3{
		Id:            vol.ID,
		ActionID:      actionID,
		Name:          vol.Name,
		StorageBucket: vol.StorageBucket,
		SecretRef:     vol.SecretRef,
		StorageType:   vol.StorageType,
		VolumeType:    vol.VolumeType,
		Location:      vol.Credential.AwsLocation,
		Region:        vol.Credential.Region,
	}

	if s3.logger == nil {
		l := backupapi.NewLog()
		s3.logger = l
	}

	cred := credentials.NewStaticCredentials(vol.Credential.AwsAccessKeyId, vol.Credential.AwsSecretAccessKey, vol.Credential.Token)
	_, err := cred.Get()
	if err != nil {
		s3.logger.Sugar().Info("Bad credentials", err)
	}
	sess := storage.New(session.Must(session.NewSession(&aws.Config{
		DisableSSL:       aws.Bool(false),
		Credentials:      cred,
		Endpoint:         aws.String(vol.Credential.AwsLocation),
		Region:           aws.String(vol.Credential.Region),
		S3ForcePathStyle: aws.Bool(true),
	})))
	s3.S3Session = sess
	return s3

}

type HTTPClient struct{}

var (
	HttpClient = HTTPClient{}
)

var backoffSchedule = []time.Duration{
	1 * time.Second,
	3 * time.Second,
	5 * time.Second,
}

func (s3 *S3) PutObject(key string, data []byte) error {
	var err error
	var once bool
	for _, backoff := range backoffSchedule {
		_, err = s3.S3Session.PutObject(&storage.PutObjectInput{
			Bucket: aws.String(s3.StorageBucket),
			Key:    aws.String(key),
			Body:   bytes.NewReader(data),
		})
		if err == nil {
			break
		}

		if aerr, ok := err.(awserr.Error); ok {
			if aerr.Error() == "Forbidden" {
				if once {
					s3.logger.Sugar().Info("Return false cause in put object: ", aerr.Code(), key)
					return err
				}
				s3.logger.Info("Put object one more time")
				once = true
				rand.Seed(time.Now().UnixNano())
				n := rand.Intn(3) // n will be between 0 and 10
				time.Sleep(time.Duration(n) * time.Second)
			}
		}
		time.Sleep(backoff)
	}

	return err
}

func (s3 *S3) GetObject(key string) ([]byte, error) {
	var err error
	var once bool
	var obj *storage.GetObjectOutput
	for _, backoff := range backoffSchedule {
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
					s3.logger.Sugar().Info("Return false cause in get object: ", aerr.Code(), key)
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
		log.Error(err)
		time.Sleep(backoff)
	}

	body, err := ioutil.ReadAll(obj.Body)

	return body, err
}

func (s3 *S3) HeadObject(key string) (bool, string, error) {
	var err error
	var headObject *storage.HeadObjectOutput
	var once bool
	for _, backoff := range backoffSchedule {
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
					s3.logger.Sugar().Info(fmt.Sprintf("Return false cause in head object: %s %s", aerr.Code(), key))
					return false, "", err
				}
				s3.logger.Sugar().Info("Head object one more time", key)
				once = true
				rand.Seed(time.Now().UnixNano())
				n := rand.Intn(3) // n will be between 0 and 10
				time.Sleep(time.Duration(n) * time.Second)
			}
		}
		time.Sleep(backoff)

	}
	return false, "", err
}

func (s3 *S3) RefreshCredential(credential volume.Credential) error {
	cred := credentials.NewStaticCredentials(credential.AwsAccessKeyId, credential.AwsSecretAccessKey, credential.Token)
	_, err := cred.Get()
	if err != nil {
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
