package logger

import (
	"context"
	"io"
	"strings"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type contextKey string

const traceIDKey contextKey = "trace_id"

// SetTraceID 将 trace_id 写入 context。
func SetTraceID(ctx context.Context, traceID string) context.Context {
	if traceID == "" {
		traceID = "-"
	}
	return context.WithValue(ctx, traceIDKey, traceID)
}

// GetTraceID 从 context 读取 trace_id，无则返回 "-"。
func GetTraceID(ctx context.Context) string {
	if v, ok := ctx.Value(traceIDKey).(string); ok && v != "" {
		return v
	}
	return "-"
}

// Logger 封装 zerolog，支持从 context 注入 trace_id。
type Logger struct {
	zl zerolog.Logger
}

// New 创建 Logger，输出到 w（如文件或 lumberjack）。
func New(w io.Writer, level string) *Logger {
	zl := zerolog.New(w).With().Timestamp().Logger()
	lvl := parseLevel(level)
	zerolog.SetGlobalLevel(lvl)
	return &Logger{zl: zl}
}

func parseLevel(s string) zerolog.Level {
	switch strings.ToUpper(s) {
	case "DEBUG":
		return zerolog.DebugLevel
	case "INFO":
		return zerolog.InfoLevel
	case "WARN", "WARNING":
		return zerolog.WarnLevel
	case "ERROR":
		return zerolog.ErrorLevel
	default:
		return zerolog.InfoLevel
	}
}

// WithContext 返回带当前请求 trace_id 的 zerolog.Logger。
func (l *Logger) WithContext(ctx context.Context) *zerolog.Logger {
	zl := l.zl.With().Str("trace_id", GetTraceID(ctx)).Logger()
	return &zl
}

// C 返回底层 zerolog，用于无 context 场景（如启动时）。
func (l *Logger) C() *zerolog.Logger {
	return &l.zl
}

// Global 返回无 trace_id 的全局 logger。
func Global() zerolog.Logger {
	return log.Logger
}
