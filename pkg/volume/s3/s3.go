package s3

import (
	"bytes"
	"io/ioutil"
	"net/http"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/bizflycloud/bizfly-backup/pkg/volume"
)

type S3 struct {
	Name          string
	StorageBucket string
	SecretRef     string
	PresignURL    string
}

var _ volume.StorageVolume = (*S3)(nil)

func NewS3Default(name string, storageBucket string, secretRef string) *S3 {
	return &S3{
		Name:          name,
		StorageBucket: storageBucket,
		SecretRef:     secretRef,
	}
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
	//log.Printf("PUT %s -> %d", req.URL, resp.StatusCode)

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
	var err error
	var etag string
	for _, backoff := range backoffSchedule {
		etag, err = putRequest(key, data)
		if err == nil {
			break
		}
		// log.Printf("retrying in %v\n", backoff)
		time.Sleep(backoff)
	}

	if err != nil {
		return "", err
	}

	return etag, nil
}

func (s3 *S3) GetObject(key string) ([]byte, error) {
	var resp []byte
	var err error
	for _, backoff := range backoffSchedule {
		resp, err = getRequest(key)
		if err == nil {
			break
		}
		// log.Printf("retrying in %v\n", backoff)
		time.Sleep(backoff)
	}

	if err != nil {
		return nil, err
	}

	return resp, nil
}

func (s3 *S3) HeadObject(key string) (*http.Response, error) {
	var resp *http.Response
	var err error
	for _, backoff := range backoffSchedule {
		resp, err = http.Head(key)
		if err == nil {
			break
		}
		log.Printf("retrying in %v\n", backoff)
		time.Sleep(backoff)
	}

	if err != nil {
		return nil, err
	}

	return resp, nil
}

func (s3 *S3) SetCredential(preSign string) {
	panic("implement")
}
