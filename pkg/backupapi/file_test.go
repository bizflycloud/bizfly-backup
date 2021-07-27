package backupapi

import (
	"io/fs"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestClient_saveFileInfoPath(t *testing.T) {
	setUp()
	defer tearDown()

	recoveryPointID := "recovery-point-id"
	sfip := client.saveFileInfoPath(recoveryPointID)
	assert.Equal(t, "/agent/recovery-points/recovery-point-id/file", sfip)
}

func TestClient_getItemLatestPath(t *testing.T) {
	setUp()
	defer tearDown()

	latestRecoveryPointID := "latest-recovery-point-id"
	gilp := client.getItemLatestPath(latestRecoveryPointID)
	assert.Equal(t, "/agent/recovery-points/latest-recovery-point-id/path", gilp)
}

func Test_createDir(t *testing.T) {
	type args struct {
		path  string
		mode  fs.FileMode
		uid   int
		gid   int
		atime time.Time
		mtime time.Time
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "test create directory",
			args: args{
				path:  "/tmp/restore",
				mode:  0775,
				uid:   1000,
				gid:   1000,
				atime: time.Now(),
				mtime: time.Now(),
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := client.createDir(tt.args.path, tt.args.mode, tt.args.uid, tt.args.gid, tt.args.atime, tt.args.mtime); (err != nil) != tt.wantErr {
				t.Errorf("createDir() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_createFile(t *testing.T) {
	type args struct {
		path string
		mode fs.FileMode
		uid  int
		gid  int
	}
	tests := []struct {
		name    string
		args    args
		want    *os.File
		wantErr bool
	}{
		{
			name: "test create file",
			args: args{
				path: "/tmp/file.txt",
				mode: 0775,
				uid:  1000,
				gid:  1000,
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := client.createFile(tt.args.path, tt.args.mode, tt.args.uid, tt.args.gid)
			if (err != nil) != tt.wantErr {
				t.Errorf("createFile() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
		})
	}
}
