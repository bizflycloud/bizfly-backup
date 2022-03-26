package backupapi

import (
	"os"
	"reflect"
	"testing"

	"github.com/bizflycloud/bizfly-backup/pkg/support"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

func Test_logWriter(t *testing.T) {
	logPath, _, _ := support.CheckPath()

	tests := []struct {
		name    string
		want    zapcore.WriteSyncer
		wantErr bool
	}{
		{
			name: "test log error writer",
			want: zapcore.NewMultiWriteSyncer(
				zapcore.AddSync(&lumberjack.Logger{
					Filename: logPath,
					MaxSize:  500,
					MaxAge:   30,
				}),
				zapcore.AddSync(os.Stdout)),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := logWriter()
			if (err != nil) != tt.wantErr {
				t.Errorf("logErrorWriter() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("logErrorWriter() = %v, want %v", got, tt.want)
			}
		})
	}
}
