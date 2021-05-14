package s3

import (
	"bytes"
	"log"
	"net/http"

	"github.com/bizflycloud/bizfly-backup/pkg/volume"
	"github.com/hashicorp/go-retryablehttp"
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

	req, err := retryablehttp.NewRequest("PUT", key, bytes.NewReader(data))
	if err != nil {
		return err
	}
	resp, err := retryablehttp.NewClient().Do(req)
	if err != nil {
		return err
	}
	log.Printf("PUT %s -> %d", req.URL, resp.StatusCode)
	defer resp.Body.Close()

	return nil
}

func (s3 *S3) GetObject(key string) ([]byte, error) {
	panic("implement")
}

func (s3 *S3) HeadObject(key string) (int, error) {
	resp, err := http.Head(key)
	if err != nil {
		log.Println(err)
	}
	defer resp.Body.Close()

	return resp.StatusCode, nil
}

func (s3 *S3) SetCredential(preSign string) {
	panic("implement")
}
