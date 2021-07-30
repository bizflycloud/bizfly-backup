package backupapi

import (
	"os"
	"reflect"
	"testing"

	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

func Test_logDebugWriter(t *testing.T) {
	tests := []struct {
		name string
		want zapcore.WriteSyncer
	}{
		{
			name: "test log debug writer",
			want: zapcore.NewMultiWriteSyncer(
				zapcore.AddSync(&lumberjack.Logger{
					Filename: "./var/log/bizflycloud-backup/debug.log",
					MaxSize:  500,
					MaxAge:   30,
				}),
				zapcore.AddSync(os.Stdout)),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := logDebugWriter(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("logDebugWriter() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_logInfoWriter(t *testing.T) {
	tests := []struct {
		name string
		want zapcore.WriteSyncer
	}{
		{
			name: "test log info writer",
			want: zapcore.NewMultiWriteSyncer(
				zapcore.AddSync(&lumberjack.Logger{
					Filename: "./var/log/bizflycloud-backup/info.log",
					MaxSize:  500,
					MaxAge:   30,
				}),
				zapcore.AddSync(os.Stdout)),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := logInfoWriter(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("logInfoWriter() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_logErrorWriter(t *testing.T) {
	tests := []struct {
		name string
		want zapcore.WriteSyncer
	}{
		{
			name: "test log error writer",
			want: zapcore.NewMultiWriteSyncer(
				zapcore.AddSync(&lumberjack.Logger{
					Filename: "./var/log/bizflycloud-backup/error.log",
					MaxSize:  500,
					MaxAge:   30,
				}),
				zapcore.AddSync(os.Stdout)),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := logErrorWriter(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("logErrorWriter() = %v, want %v", got, tt.want)
			}
		})
	}
}
