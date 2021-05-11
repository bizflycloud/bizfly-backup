package s3

type S3 struct {
	Name          string
	StorageBucket string
	SecretRef     string
	PresignURL    string
}

func NewS3Default(name string, storageBucket string, secretRef string) *S3 {
	return &S3{
		Name:          name,
		StorageBucket: storageBucket,
		SecretRef:     secretRef,
	}
}

func (s3 *S3) SetCredential(preSign string) {
	panic("implement")
}

func (s3 *S3) GetObject(key string) ([]byte, error) {
	panic("implement")
}

func (s3 *S3) PutObject(key string, data []byte) error {
	panic("implement")
}

func (s3 *S3) HeadObject(key string) (bool, error) {
	panic("implement")
}
