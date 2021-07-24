package backupapi

import (
	"os"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func NewLog() *zap.Logger {
	writerSyncer := getLogWriter()
	encoder := getEncoder()

	core := zapcore.NewCore(encoder, writerSyncer, zapcore.DebugLevel)

	logger := zap.New(core, zap.AddCaller())
	return logger
}

func getEncoder() zapcore.Encoder {
	return zapcore.NewJSONEncoder(zapcore.EncoderConfig{
		MessageKey:   "message",
		TimeKey:      "time",
		LevelKey:     "level",
		CallerKey:    "caller",
		EncodeLevel:  CustomLevelEncoder,         //Format cách hiển thị level log
		EncodeTime:   SyslogTimeEncoder,          //Format hiển thị thời điểm log
		EncodeCaller: zapcore.ShortCallerEncoder, //Format caller
	})
}

func SyslogTimeEncoder(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(t.Format("2006-01-02 15:04:05"))
}

func CustomLevelEncoder(level zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString("[" + level.CapitalString() + "]")
}

func getLogWriter() zapcore.WriteSyncer {
	file, _ := os.Create("./bizfly-backup.log")
	return zapcore.AddSync(file)
}
