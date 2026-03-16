package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const prefix = "alert_router_"

var (
	AlertsReceivedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{Name: prefix + "alerts_received_total", Help: "按来源与状态统计的接收告警数"},
		[]string{"source", "status"},
	)
	AlertsRoutedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{Name: prefix + "alerts_routed_total", Help: "被路由到各渠道的告警次数"},
		[]string{"channel"},
	)
	AlertsSentTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{Name: prefix + "alerts_sent_total", Help: "按渠道与结果统计的发送次数（success/failure/skipped）"},
		[]string{"channel", "status"},
	)
	AlertsSendFailuresTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{Name: prefix + "alerts_send_failures_total", Help: "按渠道与原因统计的发送失败次数"},
		[]string{"channel", "reason"},
	)
	AlertsDedupSkippedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{Name: prefix + "alerts_dedup_skipped_total", Help: "去重跳过次数（jenkins/grafana）"},
		[]string{"type"},
	)
	ImageGeneratedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{Name: prefix + "image_generated_total", Help: "按来源与状态统计的出图次数"},
		[]string{"source", "status"},
	)
	PrometheusRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{Name: prefix + "prometheus_requests_total", Help: "请求 Prometheus/VM query_range 次数"},
		[]string{"status"},
	)
	PrometheusRequestDuration = promauto.NewHistogram(
		prometheus.HistogramOpts{Name: prefix + "prometheus_request_duration_seconds", Help: "query_range 请求耗时"},
	)
	WebhookRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{Name: prefix + "webhook_requests_total", Help: "Webhook 请求次数"},
		[]string{"status"},
	)
	WebhookRequestDuration = promauto.NewHistogram(
		prometheus.HistogramOpts{Name: prefix + "webhook_request_duration_seconds", Help: "单次 Webhook 处理耗时"},
	)
)

// IncAlertsSent 记录一次发送结果（success/failure/skipped）。
func IncAlertsSent(channel, status string) {
	AlertsSentTotal.WithLabelValues(channel, status).Inc()
}

// IncAlertsSendFailure 记录一次发送失败及原因（timeout/http_error/invalid_response/network）。
func IncAlertsSendFailure(channel, reason string) {
	AlertsSendFailuresTotal.WithLabelValues(channel, reason).Inc()
}
