// +build linux

package backupapi

import "testing"

func TestSetChownItem(t *testing.T) {
	type args struct {
		name string
		uid  int
		gid  int
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "test set chown item",
			args: args{
				name: "/tmp/test.txt",
				uid:  1000,
				gid:  1000,
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := SetChownItem(tt.args.name, tt.args.uid, tt.args.gid); (err != nil) != tt.wantErr {
				t.Errorf("SetChownItem() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
