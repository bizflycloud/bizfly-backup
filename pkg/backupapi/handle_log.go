package backupapi

import (
	"os"
	"time"

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
	homeDirectory, err := getCurrentDirectory()
	if err != nil {
		return nil, err
	}
	logErrorPath := homeDirectory + "/var/log/bizflycloud-backup/error.log"
	_, _, _ = zap.Open(logErrorPath)

	return zapcore.NewMultiWriteSyncer(
		zapcore.AddSync(&lumberjack.Logger{
			Filename: logErrorPath,
			MaxSize:  500, // megabytes
			MaxAge:   30,  // days
		}),
		zapcore.AddSync(os.Stdout)), nil
}

func logInfoWriter() (zapcore.WriteSyncer, error) {
	homeDirectory, err := getCurrentDirectory()
	if err != nil {
		return nil, err
	}
	logInfoPath := homeDirectory + "/var/log/bizflycloud-backup/info.log"
	_, _, _ = zap.Open(logInfoPath)

	return zapcore.NewMultiWriteSyncer(
		zapcore.AddSync(&lumberjack.Logger{
			Filename: logInfoPath,
			MaxSize:  500,
			MaxAge:   30,
		}),
		zapcore.AddSync(os.Stdout)), nil
}

func logDebugWriter() (zapcore.WriteSyncer, error) {
	homeDirectory, err := getCurrentDirectory()
	if err != nil {
		return nil, err
	}
	logDebugPath := homeDirectory + "/var/log/bizflycloud-backup/debug.log"
	_, _, _ = zap.Open(logDebugPath)

	return zapcore.NewMultiWriteSyncer(
		zapcore.AddSync(&lumberjack.Logger{
			Filename: logDebugPath,
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

func getCurrentDirectory() (string, error) {
	//user, err := user.Current()
	//if err != nil {
	//	return "", err
	//}
	//homeDirectory := user.HomeDir
	//return homeDirectory, nil
	return ".", nil
}
