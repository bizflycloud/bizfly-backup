package backupapi

import (
	"os"
	"os/user"
	"reflect"
	"testing"

	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

func Test_logDebugWriter(t *testing.T) {
	user, _ := user.Current()
	homeDirectory := user.HomeDir
	logDebugPath := homeDirectory + "/var/log/bizflycloud-backup/debug.log"

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
	user, _ := user.Current()
	homeDirectory := user.HomeDir
	logInfoPath := homeDirectory + "/var/log/bizflycloud-backup/info.log"

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
	user, _ := user.Current()
	homeDirectory := user.HomeDir
	logErrorPath := homeDirectory + "/var/log/bizflycloud-backup/error.log"

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
