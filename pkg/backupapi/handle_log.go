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

func logErrorWriter() (zapcore.WriteSyncer, error) {
	logErrorPath, err := createLogFile(support.LOG_ERROR_PATH, 0700)
	if err != nil {
		return nil, err
	}

	return zapcore.NewMultiWriteSyncer(
		zapcore.AddSync(&lumberjack.Logger{
			Filename: logErrorPath.Name(),
			MaxSize:  500, // megabytes
			MaxAge:   30,  // days
		}),
		zapcore.AddSync(os.Stdout)), nil
}

func logInfoWriter() (zapcore.WriteSyncer, error) {
	logInfoPath, err := createLogFile(support.LOG_INFO_PATH, 0700)
	if err != nil {
		return nil, err
	}

	return zapcore.NewMultiWriteSyncer(
		zapcore.AddSync(&lumberjack.Logger{
			Filename: logInfoPath.Name(),
			MaxSize:  500,
			MaxAge:   30,
		}),
		zapcore.AddSync(os.Stdout)), nil
}

func logDebugWriter() (zapcore.WriteSyncer, error) {
	logDebugPath, err := createLogFile(support.LOG_DEBUG_PATH, 0700)
	if err != nil {
		return nil, err
	}

	return zapcore.NewMultiWriteSyncer(
		zapcore.AddSync(&lumberjack.Logger{
			Filename: logDebugPath.Name(),
			MaxSize:  500,
			MaxAge:   30,
		}),
		zapcore.AddSync(os.Stdout)), nil
}

// Write log to file by level log and console
func WriteLog() (*zap.Logger, error) {
	highWriteSyncer, errorWriter := logErrorWriter()
	if errorWriter != nil {
		return nil, errorWriter
	}
	averageWriteSyncer, errorDebugWriter := logDebugWriter()
	if errorDebugWriter != nil {
		return nil, errorDebugWriter
	}
	lowWriteSyncer, errorInfoWriter := logInfoWriter()
	if errorInfoWriter != nil {
		return nil, errorInfoWriter
	}

	encoder := getEncoder()

	highPriority := zap.LevelEnablerFunc(func(lev zapcore.Level) bool {
		return lev >= zap.ErrorLevel
	})

	lowPriority := zap.LevelEnablerFunc(func(lev zapcore.Level) bool {
		return lev < zap.ErrorLevel && lev > zap.DebugLevel
	})

	averagePriority := zap.LevelEnablerFunc(func(lev zapcore.Level) bool {
		return lev < zap.ErrorLevel && lev < zap.InfoLevel
	})

	lowCore := zapcore.NewCore(encoder, lowWriteSyncer, lowPriority)
	averageCore := zapcore.NewCore(encoder, averageWriteSyncer, averagePriority)
	highCore := zapcore.NewCore(encoder, highWriteSyncer, highPriority)

	logger := zap.New(zapcore.NewTee(lowCore, averageCore, highCore), zap.AddCaller())
	return logger, nil
}

func createLogFile(path string, mode fs.FileMode) (*os.File, error) {
	dirName := filepath.Dir(path)
	if _, err := os.Stat(dirName); os.IsNotExist(err) {
		if err := os.MkdirAll(dirName, mode); err != nil {
			return nil, err
		}
	}
	var file *os.File
	file, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	err = os.Chmod(path, mode)
	if err != nil {
		return nil, err
	}
	return file, nil
}
