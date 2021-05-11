package s3

import (
	"bytes"
	"io/ioutil"
	"os"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
)

// AmazonS3Backend is a storage backend for Amazon S3
type AmazonS3Backend struct {
	Bucket   string
	Client   *s3.S3
	Uploader *s3manager.Uploader
}

// NewAmazonS3Backend creates a new instance of AmazonS3Backend with credentials
func NewAmazonS3Backend(bucket string, region string, endpoint string) *AmazonS3Backend {
	accessKeyId := os.Getenv("AWS_ACCESS_KEY_ID")
	secretAccessKey := os.Getenv("AWS_SECRET_ACCESS_KEY")

	if len(accessKeyId) == 0 {
		panic("AWS_ACCESS_KEY_ID environment variable is not set")
	}

	if len(secretAccessKey) == 0 {
		panic("AWS_SECRET_ACCESS_KEY environment variable is not set")
	}
	credentials := credentials.NewStaticCredentials(accessKeyId, secretAccessKey, "")
	_, err := credentials.Get()
	if err != nil {
		panic("Failed to create AWS client: " + err.Error())
	}

	service := s3.New(session.Must(session.NewSession(&aws.Config{
		Credentials: credentials,
		Region:      aws.String(region),
		Endpoint:    aws.String(endpoint),
	})))

	b := &AmazonS3Backend{
		Bucket:   bucket,
		Client:   service,
		Uploader: s3manager.NewUploaderWithClient(service),
	}
	return b
}

// HeadObject checks for the exist of an object in the Amazon S3 group, at the key
func (b AmazonS3Backend) HeadObject(key string) (bool, error) {
	s3Input := &s3.HeadObjectInput{
		Bucket: aws.String(b.Bucket),
		Key:    aws.String(key),
	}
	_, err := b.Client.HeadObject(s3Input)
	if err != nil {
		return false, err
	}
	return true, err
}

// PutObject uploads an object to Amazon S3 bucket, at key
func (b AmazonS3Backend) PutObject(key string, data []byte) error {
	s3Input := &s3manager.UploadInput{
		Bucket: aws.String(b.Bucket),
		Key:    aws.String(key),
		Body:   bytes.NewBuffer(data),
	}
	_, err := b.Uploader.Upload(s3Input)
	return err
}

// GetObject retrieves an object from Amazon S3 bucket, at key
func (b AmazonS3Backend) GetObject(key string) ([]byte, error) {
	s3Input := &s3.GetObjectInput{
		Bucket: aws.String(b.Bucket),
		Key:    aws.String(key),
	}
	s3Result, err := b.Client.GetObject(s3Input)
	if err != nil {
		return nil, err
	}
	data, err := ioutil.ReadAll(s3Result.Body)
	if err != nil {
		return nil, err
	}
	return data, nil
}
