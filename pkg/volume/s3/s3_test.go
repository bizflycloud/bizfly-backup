package s3

import (
	"reflect"
	"testing"

	storage "github.com/aws/aws-sdk-go/service/s3"
	"github.com/bizflycloud/bizfly-backup/pkg/volume"
	"go.uber.org/zap"
)

func TestS3_Type(t *testing.T) {
	type fields struct {
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
		logger        *zap.Logger
	}
	tests := []struct {
		name   string
		fields fields
		want   volume.Type
	}{
		{
			name: "test type volume",
			want: volume.Type{
				VolumeType:  "",
				StorageType: "",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s3 := &S3{
				Id:            tt.fields.Id,
				ActionID:      tt.fields.ActionID,
				Name:          tt.fields.Name,
				StorageBucket: tt.fields.StorageBucket,
				SecretRef:     tt.fields.SecretRef,
				PresignURL:    tt.fields.PresignURL,
				StorageType:   tt.fields.StorageType,
				VolumeType:    tt.fields.VolumeType,
				Location:      tt.fields.Location,
				Region:        tt.fields.Region,
				S3Session:     tt.fields.S3Session,
				logger:        tt.fields.logger,
			}
			if got := s3.Type(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("S3.Type() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestS3_ID(t *testing.T) {
	type fields struct {
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
		logger        *zap.Logger
	}
	tests := []struct {
		name   string
		fields fields
		want   string
		want1  string
	}{
		{
			name:  "test id of s3",
			want:  "",
			want1: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s3 := &S3{
				Id:            tt.fields.Id,
				ActionID:      tt.fields.ActionID,
				Name:          tt.fields.Name,
				StorageBucket: tt.fields.StorageBucket,
				SecretRef:     tt.fields.SecretRef,
				PresignURL:    tt.fields.PresignURL,
				StorageType:   tt.fields.StorageType,
				VolumeType:    tt.fields.VolumeType,
				Location:      tt.fields.Location,
				Region:        tt.fields.Region,
				S3Session:     tt.fields.S3Session,
				logger:        tt.fields.logger,
			}
			got, got1 := s3.ID()
			if got != tt.want {
				t.Errorf("S3.ID() got = %v, want %v", got, tt.want)
			}
			if got1 != tt.want1 {
				t.Errorf("S3.ID() got1 = %v, want %v", got1, tt.want1)
			}
		})
	}
}
