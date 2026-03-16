package logger

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// responseWriter 包装 ResponseWriter 以捕获状态码。
type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

// requestEventType 根据 path/method 返回请求类型，便于日志筛选和判断是告警/健康检查/指标。
func requestEventType(path, method string) string {
	switch path {
	case "/webhook":
		return "webhook_finish"
	case "/health":
		return "health_check"
	case "/metrics":
		return "metrics_scrape"
	default:
		return "http_request"
	}
}

// clientIP 从请求中取客户端 IP（支持代理 X-Forwarded-For / X-Real-IP），便于排查请求来源。
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.Index(xff, ","); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	return r.RemoteAddr
}

// Middleware 为请求注入 trace_id 并记录请求日志（来源 IP、方法、路径、状态码、耗时），便于排查。
func Middleware(logger *Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			traceID := r.Header.Get("X-Trace-Id")
			if traceID == "" {
				traceID = r.Header.Get("X-Request-Id")
			}
			if traceID == "" {
				b := make([]byte, 8)
				if _, err := rand.Read(b); err == nil {
					traceID = hex.EncodeToString(b)
				} else {
					traceID = "-"
				}
			}
			ctx := SetTraceID(r.Context(), traceID)
			start := time.Now()
			rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rw, r.WithContext(ctx))
			dur := time.Since(start)
			ip := clientIP(r)
			event := requestEventType(r.URL.Path, r.Method)
			logger.WithContext(ctx).Info().
				Str("event", event).
				Str("remote_addr", ip).
				Str("method", r.Method).
				Str("path", r.URL.Path).
				Int("status", rw.status).
				Dur("duration_ms", dur).
				Msg(fmt.Sprintf("[%s] %s %s %s → %d (%.0fms)", event, ip, r.Method, r.URL.Path, rw.status, dur.Seconds()*1000))
		})
	}
}
