package backupapi

import (
	"os"
	"reflect"
	"testing"

	"github.com/bizflycloud/bizfly-backup/pkg/support"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

func Test_logDebugWriter(t *testing.T) {
	_, logDebugPath, _, _, _ := support.CheckPath()

	tests := []struct {
		name    string
		want    zapcore.WriteSyncer
		wantErr bool
	}{
		{
			name: "test log debug writer",
			want: zapcore.NewMultiWriteSyncer(
				zapcore.AddSync(&lumberjack.Logger{
					Filename: logDebugPath,
					MaxSize:  500,
					MaxAge:   30,
				}),
				zapcore.AddSync(os.Stdout)),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := logDebugWriter()
			if (err != nil) != tt.wantErr {
				t.Errorf("logDebugWriter() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("logDebugWriter() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_logInfoWriter(t *testing.T) {
	_, _, logInfoPath, _, _ := support.CheckPath()

	tests := []struct {
		name    string
		want    zapcore.WriteSyncer
		wantErr bool
	}{
		{
			name: "test log info writer",
			want: zapcore.NewMultiWriteSyncer(
				zapcore.AddSync(&lumberjack.Logger{
					Filename: logInfoPath,
					MaxSize:  500,
					MaxAge:   30,
				}),
				zapcore.AddSync(os.Stdout)),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := logInfoWriter()
			if (err != nil) != tt.wantErr {
				t.Errorf("logInfoWriter() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("logInfoWriter() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_logErrorWriter(t *testing.T) {
	logErrorPath, _, _, _, _ := support.CheckPath()

	tests := []struct {
		name    string
		want    zapcore.WriteSyncer
		wantErr bool
	}{
		{
			name: "test log error writer",
			want: zapcore.NewMultiWriteSyncer(
				zapcore.AddSync(&lumberjack.Logger{
					Filename: logErrorPath,
					MaxSize:  500,
					MaxAge:   30,
				}),
				zapcore.AddSync(os.Stdout)),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := logErrorWriter()
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
