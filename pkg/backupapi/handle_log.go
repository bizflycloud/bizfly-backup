package backupapi

import (
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/bizflycloud/bizfly-backup/pkg/support"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"gopkg.in/natefinch/lumberjack.v2"
)

func getEncoder() zapcore.Encoder {
	return zapcore.NewConsoleEncoder(zapcore.EncoderConfig{
		MessageKey:   "message",
		TimeKey:      "time",
		LevelKey:     "level",
		CallerKey:    "caller",
		EncodeLevel:  CustomLevelEncoder,         //Format cách hiển thị level log
		EncodeTime:   SyslogTimeEncoder,          //Format hiển thị thời điểm log
		EncodeCaller: zapcore.ShortCallerEncoder, //Format dòng code bắt đầu log
	})
}

func SyslogTimeEncoder(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(t.Format("2006-01-02 15:04:05"))
}

func CustomLevelEncoder(level zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString("[" + level.CapitalString() + "]")
}

func logWriter() (zapcore.WriteSyncer, error) {
	// get path of log file for current os
	path, _, err := support.CheckPath()
	if err != nil {
		return nil, err
	}

	// check if log file exist or not to create
	logPath, err := createLogFile(path, 0700)
	if err != nil {
		return nil, err
	}

	return zapcore.NewMultiWriteSyncer(
		zapcore.AddSync(&lumberjack.Logger{
			Filename: logPath.Name(),
			MaxSize:  500,
			MaxAge:   30,
		}),
		zapcore.AddSync(os.Stdout)), nil
}

// Write log to file
func WriteLog() (*zap.Logger, error) {
	writeSyncer, errorWriter := logWriter()
	if errorWriter != nil {
		return nil, errorWriter
	}

	encoder := getEncoder()

	// enable log sync for all level so we return true
	logPriority := zap.LevelEnablerFunc(func(lev zapcore.Level) bool {
		return true
	})

	logCore := zapcore.NewCore(encoder, writeSyncer, logPriority)
	logger := zap.New(zapcore.NewTee(logCore), zap.AddCaller())
	return logger, nil
}

func createLogFile(path string, mode fs.FileMode) (*os.File, error) {
	// check if folder log exist or not to create
	dirName := filepath.Dir(path)
	if _, err := os.Stat(dirName); os.IsNotExist(err) {
		if err := os.MkdirAll(dirName, mode); err != nil {
			return nil, err
		}
	}

	// check if file log exist or not to create
	var file *os.File
	if _, err := os.Stat(path); os.IsNotExist(err) {
		file, err = os.Create(path)
		if err != nil {
			return nil, err
		}
	} else {
		file, err = os.Open(path)
		if err != nil {
			return nil, err
		}
	}

	err := os.Chmod(path, mode)
	if err != nil {
		return nil, err
	}

	return file, nil
}
