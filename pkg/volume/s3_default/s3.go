package s3_default

import "github.com/bizflycloud/bizfly-backup/pkg/volume"

type S3Default struct {
	PreSignURL string
}

func (s3 *S3Default) PutObject(name string, data []byte) error {
	panic("implement me")
}

func (s3 *S3Default) TestObject(name string) (bool, error) {
	panic("implement me")
}

func (s3 *S3Default) GetObject(name string) ([]byte, error) {
	panic("implement me")
}

var _ volume.Volume = (*S3Default)(nil)

func NewS3Default(preSignUrl string) *S3Default {
	return &S3Default{PreSignURL: preSignUrl}
}
