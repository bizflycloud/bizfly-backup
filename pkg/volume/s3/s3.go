package s3

import (
	"bytes"
	"fmt"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/bizflycloud/bizfly-backup/pkg/backupapi"
	"io/ioutil"
	"math/rand"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	storage "github.com/aws/aws-sdk-go/service/s3"
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
	AccessKey     string
	S3Session     *storage.S3
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
	//RGW_ACCESS_KEY: K36GV67K428NZDNOM1J1
	//RGW_ENDPOINT: https://ss-hn-1.vccloud.vn/
	//RGW_REGION: hn
	//RGW_SECRET_KEY: mgJZBPStXmU5MQuDw4XAVjBkyl4ZZ2sGYlINeYQY

	// time: 9:39 => 9:55
	accessKey := "IrwAjOcIlBP6FSaKXJ0"
	secretKey := "VH17I27WTY5I4HV6Z5DCE709DSXCC3W6PZNKDI6"
	token := "oVlYcd5PxYKa51uFjhH14hEOAcC0snAEciDC5cL9LIi0yvIwucCik5/8i0QgAy06QTB2KjzKJdcDR7hJ22Dexy8uTyApR5Wb+/yF1qaGwpl+vYx5f1lJyw6XUiPM8TFUvffhjpkYQPXUHBZ8XCs8UJk4SqLWJRvWpVg/awl45WM2IcOLWKcwolH7gS5LKomKWxEwFB6a6sZ9eWIbDro+k75XyGlDcB+oAJxi+ZgTra4k1PQNDP1eExB1roOtikOYiMguwywXFZu9G5qfiUcZh6zEilwWWPsZQcnhLeaDJAkbCR8Y5X8oBKAMMFG5N4w/YLo9qlONa6fyF+zWWvUt1b9lym6ib9/wdzFWdetGF0r6qVSS9ac+jyryEunJ/V3WBHu5mpw1ct6jAtjzvTuwE958ziOj+7uQ2jLecBJX6BKHsKp7g10qM+CO0GDoae29bwl1/6FVprshRL9fJNGcGr9Dxpx9zExrZSKZcrOjPR4="

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
		AccessKey:     accessKey,
	}

	//cred := credentials.NewStaticCredentials(vol.Credential.AwsAccessKeyId, vol.Credential.AwsSecretAccessKey, vol.Credential.Token)
	cred := credentials.NewStaticCredentials(accessKey, secretKey, token)
	_, err := cred.Get()
	if err != nil {
		fmt.Println("Bad credentials", err)
	}
	sess := storage.New(session.Must(session.NewSession(&aws.Config{
		DisableSSL:       aws.Bool(true),
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
	log.Println("Put object", key, s3.AccessKey)
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
					log.Info("Return false cause: ", aerr.Code())
					return err
				}
				log.Info("====== Put object one more time =============")
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
	log.Println("Downloading chunk", key, s3.AccessKey)
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
					log.Info("Return false cause: ", aerr.Code())
					return nil, err
				}
				log.Info("====== Get object one more time =============")
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
			if !once {
				once = true
			}
			if aerr.Code() == "Forbidden" {
				if once {
					log.Info("Return false cause: ", aerr.Code())
					return false, "", err
				}
				log.Info("====== head object one more time =============")
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
	s3.AccessKey = credential.AwsAccessKeyId
	cred := credentials.NewStaticCredentials(credential.AwsAccessKeyId, credential.AwsSecretAccessKey, credential.Token)
	_, err := cred.Get()
	if err != nil {
		return err
	}
	sess := storage.New(session.Must(session.NewSession(&aws.Config{
		DisableSSL:       aws.Bool(true),
		Credentials:      cred,
		Endpoint:         aws.String(s3.Location),
		Region:           aws.String(s3.Region),
		S3ForcePathStyle: aws.Bool(true),
	})))
	s3.S3Session = sess
	return nil
}
