// Package logger 提供基于 zerolog 的 JSON 日志，输出时 timestamp 固定为首键。
package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sort"
	"strings"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// jsonTimeFirstWriter 包装 io.Writer，将每行 JSON 的 timestamp 提到第一个键（zerolog 的 time 会重命名为 timestamp），便于日志采集与排序。
type jsonTimeFirstWriter struct {
	w      io.Writer
	remain []byte
}

func (f *jsonTimeFirstWriter) Write(p []byte) (n int, err error) {
	n = len(p)
	f.remain = append(f.remain, p...)
	for {
		idx := bytes.IndexByte(f.remain, '\n')
		if idx < 0 {
			return n, nil
		}
		line := f.remain[:idx]
		f.remain = f.remain[idx+1:]
		if len(line) == 0 {
			if _, err = f.w.Write([]byte{'\n'}); err != nil {
				return n, err
			}
			continue
		}
		var m map[string]any
		if json.Unmarshal(line, &m) != nil {
			_, _ = f.w.Write(line)
			_, _ = f.w.Write([]byte{'\n'})
			continue
		}
		// zerolog 默认字段名为 time，统一改为 timestamp 并置于首键
		if v, ok := m["time"]; ok {
			m["timestamp"] = v
			delete(m, "time")
		}
		out, _ := json.Marshal(orderedObject{Keys: orderedKeys(m), M: m})
		if _, err = f.w.Write(out); err != nil {
			return n, err
		}
		if _, err = f.w.Write([]byte{'\n'}); err != nil {
			return n, err
		}
	}
}

// orderedObject 按指定键序序列化为 JSON，用于保证 time 在首。
type orderedObject struct {
	Keys []string
	M    map[string]any
}

func (o orderedObject) MarshalJSON() ([]byte, error) {
	buf := bytes.NewBuffer(nil)
	buf.WriteByte('{')
	for i, k := range o.Keys {
		if _, ok := o.M[k]; !ok {
			continue
		}
		if i > 0 {
			buf.WriteByte(',')
		}
		keyJSON, _ := json.Marshal(k)
		buf.Write(keyJSON)
		buf.WriteByte(':')
		valJSON, err := json.Marshal(o.M[k])
		if err != nil {
			return nil, err
		}
		buf.Write(valJSON)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// orderedKeys 返回用于 JSON 输出的键顺序：timestamp, level, message，其余按字母序。
func orderedKeys(m map[string]any) []string {
	first := []string{"timestamp", "level", "message"}
	seen := make(map[string]bool)
	for _, k := range first {
		seen[k] = true
	}
	var rest []string
	for k := range m {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	out := make([]string, 0, len(first)+len(rest))
	for _, k := range first {
		if _, ok := m[k]; ok {
			out = append(out, k)
		}
	}
	out = append(out, rest...)
	return out
}

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

// New 创建 Logger，输出到 w（如文件或 lumberjack）。JSON 每行将 timestamp 置于首键。
func New(w io.Writer, level string) *Logger {
	w = &jsonTimeFirstWriter{w: w}
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
