package s3

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"time"

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

const (
	errUnexpectedResponse = "unexpected response: %s"
)

type HTTPClient struct{}

var (
	HttpClient = HTTPClient{}
)

var backoffSchedule = []time.Duration{
	5 * time.Second,
	10 * time.Second,
	20 * time.Second,
	40 * time.Second,
	1 * time.Minute,
	2 * time.Minute,
	3 * time.Minute,
	5 * time.Minute,
	10 * time.Minute,
	20 * time.Minute,
}

func (s3 *S3) PutSingleRequest(uri string, buf []byte) error {
	req, _ := http.NewRequest("PUT", uri, bytes.NewReader(buf))
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	log.Printf("PUT %s -> %d", req.URL, resp.StatusCode)

	if resp.StatusCode != 200 {
		respErr := fmt.Errorf(errUnexpectedResponse, resp.Status)
		_ = fmt.Sprintf("request failed: %v", respErr)
		return respErr
	}
	defer resp.Body.Close()

	return nil
}

func (s3 *S3) PutObject(key string, data []byte) error {
	// sem := semaphore.NewWeighted(int64(runtime.NumCPU()))
	// group, ctx := errgroup.WithContext(context.Background())

	// for _, backoff := range backoffSchedule {
	// 	err := sem.Acquire(ctx, 1)
	// 	if err != nil {
	// 		log.Printf("acquire err = %+v\n", err)
	// 		continue
	// 	}

	// 	group.Go(func() error {
	// 		defer sem.Release(1)

	// 		err := s3.PutSingleRequest(key, data)
	// 		if err != nil {
	// 			fmt.Fprintf(os.Stderr, "request error: %+v\n", err)
	// 			_, _ = fmt.Fprintf(os.Stderr, "retrying in %v\n", backoff)
	// 			time.Sleep(backoff)
	// 		}
	// 		return nil
	// 	})
	// }

	// if err := group.Wait(); err != nil {
	// 	log.Printf("g.Wait() err = %+v\n", err)
	// }

	// return nil

	req, err := http.NewRequest("PUT", key, bytes.NewReader(data))
	if err != nil {
		fmt.Println("error creating request", key)
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Println("failed making request")
		return err
	}
	log.Printf("PUT %s -> %d", req.URL, resp.StatusCode)
	defer resp.Body.Close()

	return nil
}

func (s3 *S3) GetObject(key string) ([]byte, error) {
	panic("implement")
}

func (s3 *S3) HeadObject(key string) (bool, error) {
	panic("implement")
}

func (s3 *S3) SetCredential(preSign string) {
	panic("implement")
}
