package s3

import (
	"bytes"
	"fmt"
	"github.com/bizflycloud/bizfly-backup/pkg/backupapi"
	"io/ioutil"
	"net/http"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	storage "github.com/aws/aws-sdk-go/service/s3"
	"github.com/bizflycloud/bizfly-backup/pkg/volume"
)

type S3 struct {
	ID            string
	ActionID      string
	Name          string
	StorageBucket string
	SecretRef     string
	PresignURL    string
	StorageType   string
	VolumeType    string
	S3Session     *storage.S3
}

var _ volume.StorageVolume = (*S3)(nil)

func NewS3Default(vol backupapi.Volume, actionID string) *S3 {
	//RGW_ACCESS_KEY: K36GV67K428NZDNOM1J1
	//RGW_ENDPOINT: https://ss-hn-1.vccloud.vn/
	//RGW_REGION: hn
	//RGW_SECRET_KEY: mgJZBPStXmU5MQuDw4XAVjBkyl4ZZ2sGYlINeYQY
	s3 := &S3{
		ID:            vol.ID,
		ActionID:      actionID,
		Name:          vol.Name,
		StorageBucket: vol.StorageBucket,
		SecretRef:     vol.SecretRef,
		StorageType:   vol.StorageType,
		VolumeType:    vol.VolumeType,
	}

	cred := credentials.NewStaticCredentials(vol.Credential.AwsAccessKeyId, vol.Credential.AwsSecretAccessKey, vol.Credential.Token)
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
	10 * time.Second,
	20 * time.Second,
	30 * time.Second,
}

func putRequest(uri string, data []byte) (string, error) {
	req, err := http.NewRequest("PUT", uri, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	log.Printf("PUT %s -> %d", req.URL, resp.StatusCode)

	defer resp.Body.Close()

	return resp.Header.Get("Etag"), nil
}

func getRequest(uri string) ([]byte, error) {
	req, _ := http.NewRequest("GET", uri, nil)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	log.Printf("GET %s -> %d", req.URL, resp.StatusCode)

	if resp.StatusCode != 200 {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (s3 *S3) PutObject(key string, data []byte) (string, error) {
	//var err error
	//var etag string
	//for _, backoff := range backoffSchedule {
	//	etag, err = putRequest(key, data)
	//	if err == nil {
	//		break
	//	}
	//	// log.Printf("retrying in %v\n", backoff)
	//	time.Sleep(backoff)
	//}
	//
	//if err != nil {
	//	return "", err
	//}
	log.Println("Put object", key)
	_, err := s3.S3Session.PutObject(&storage.PutObjectInput{
		Bucket: aws.String(s3.StorageBucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
	})
	if err != nil {
		log.Println("put object error", key)
		return "", err
	}

	return "", nil
}

func (s3 *S3) GetObject(key string) ([]byte, error) {
	//var resp []byte
	//var err error
	//for _, backoff := range backoffSchedule {
	//	resp, err = getRequest(key)
	//	if err == nil {
	//		break
	//	}
	//	// log.Printf("retrying in %v\n", backoff)
	//	time.Sleep(backoff)
	//}
	//
	//if err != nil {
	//	return nil, err
	//}
	log.Println("Downloading chunk", key)
	obj, err := s3.S3Session.GetObject(&storage.GetObjectInput{
		Bucket: aws.String(s3.StorageBucket),
		Key:    aws.String(key),
	})

	if err != nil {
		fmt.Println("ERROR download", key, err)
		return nil, err
	}
	defer obj.Body.Close()

	body, err := ioutil.ReadAll(obj.Body)

	return body, err
	//return resp, nil
}

func (s3 *S3) HeadObject(key string) (bool, string, error) {
	headObject, err := s3.S3Session.HeadObject(&storage.HeadObjectInput{
		Bucket: aws.String(s3.StorageBucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return false, "", err
	}
	return true, *headObject.ETag, nil
}

func (s3 *S3) SetCredential(preSign string) {
	panic("implement")
}
