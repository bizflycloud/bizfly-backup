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

func logErrorWriter() zapcore.WriteSyncer {
	errFileLog, _ := os.OpenFile("./error.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)

	return zapcore.NewMultiWriteSyncer(
		zapcore.AddSync(&lumberjack.Logger{
			Filename: errFileLog.Name(),
			MaxSize:  500, // megabytes
			MaxAge:   30,  // days
		}),
		zapcore.AddSync(os.Stdout))
}

func logInfoWriter() zapcore.WriteSyncer {
	errFileLog, _ := os.OpenFile("./info.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)

	return zapcore.NewMultiWriteSyncer(
		zapcore.AddSync(&lumberjack.Logger{
			Filename: errFileLog.Name(),
			MaxSize:  500, // megabytes
			MaxAge:   30,  // days
		}),
		zapcore.AddSync(os.Stdout))
}

func logDebugWriter() zapcore.WriteSyncer {
	errFileLog, _ := os.OpenFile("./debug.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)

	return zapcore.NewMultiWriteSyncer(
		zapcore.AddSync(&lumberjack.Logger{
			Filename: errFileLog.Name(),
			MaxSize:  500, // megabytes
			MaxAge:   30,  // days
		}),
		zapcore.AddSync(os.Stdout))
}

// Write log to file by level log and console
func WriteLog() *zap.Logger {
	highWriteSyncer := logErrorWriter()
	averageWriteSyncer := logDebugWriter()
	lowWriteSyncer := logInfoWriter()

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
	return logger
}
