package handler

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics 提供 GET /metrics，Prometheus 文本格式。
func Metrics() http.Handler {
	return promhttp.Handler()
}
