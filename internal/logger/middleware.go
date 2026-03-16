package logger

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"
)

// Middleware 为请求注入 trace_id 并记录请求日志。
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
			next.ServeHTTP(w, r.WithContext(ctx))
			// 请求结束后写日志
			logger.WithContext(ctx).Info().
				Str("method", r.Method).
				Str("path", r.URL.Path).
				Dur("duration_ms", time.Since(start)).
				Msg("请求")
		})
	}
}
