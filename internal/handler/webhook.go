package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/jackwhich/webhook_alerts/internal/logger"
	"github.com/jackwhich/webhook_alerts/internal/metrics"
	"github.com/jackwhich/webhook_alerts/internal/service"
)

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

// Webhook 处理 POST /webhook（Alertmanager / Grafana 告警入口）。
// 入参 logObj 用于打业务日志（请求来源、数据摘要、解析/发送结果），与 Py 版一致便于排查。
func Webhook(logObj *logger.Logger, alertSvc *service.AlertService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			metrics.WebhookRequestsTotal.WithLabelValues("error").Inc()
			http.Error(w, "仅支持 POST", http.StatusMethodNotAllowed)
			return
		}
		t0 := time.Now()
		ctx := r.Context()
		ip := clientIP(r)
		logObj.WithContext(ctx).Info().
			Str("event", "webhook_received").
			Str("remote_addr", ip).
			Msg("[Webhook] 收到告警 Webhook 请求，开始处理")
		defer func() {
			metrics.WebhookRequestDuration.Observe(time.Since(t0).Seconds())
		}()

		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			metrics.WebhookRequestsTotal.WithLabelValues("invalid_json").Inc()
			logObj.WithContext(ctx).Warn().
				Str("event", "webhook_invalid_json").
				Str("remote_addr", ip).
				Err(err).
				Msg("[Webhook] 请求体 JSON 解析失败，无法处理告警")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "JSON 格式无效"})
			return
		}
		// 接收数据摘要（与 Py 一致）：receiver、version、告警数量，便于判断是谁发的、多少条
		payloadStatus, _ := payload["status"].(string)
		alertsSlice, _ := payload["alerts"].([]any)
		alertsCount := len(alertsSlice)
		receiver, _ := payload["receiver"].(string)
		version, _ := payload["version"].(string)
		groupKey, _ := payload["groupKey"].(string)
		logObj.WithContext(ctx).Info().
			Str("event", "webhook_payload").
			Str("receiver", receiver).
			Str("version", version).
			Str("groupKey", groupKey).
			Str("payload_status", payloadStatus).
			Int("alerts_count", alertsCount).
			Msgf("[Webhook] 接收数据摘要: receiver=%s, version=%s, groupKey=%s, 告警数量=%d", receiver, version, groupKey, alertsCount)

		result := alertSvc.ProcessWebhook(ctx, logObj, payload)
		metricStatus := "ok"
		if !result.OK {
			metricStatus = "error"
		}
		metrics.WebhookRequestsTotal.WithLabelValues(metricStatus).Inc()

		// 业务日志：是否成功、解析出几条告警、发送了几次、失败原因，便于排查
		sentOK := 0
		for _, s := range result.Sent {
			if s.Success {
				sentOK++
			}
		}
		sentFail := len(result.Sent) - sentOK
		if result.OK {
			logObj.WithContext(ctx).Info().
				Str("event", "webhook_complete").
				Int("alerts", result.AlertsCount).
				Int("sent_total", len(result.Sent)).
				Int("sent_ok", sentOK).
				Int("sent_fail", sentFail).
				Str("remote_addr", ip).
				Msgf("[Webhook] 本请求处理完成: 解析 %d 条告警, 发送 %d 次 (成功 %d, 失败 %d)", result.AlertsCount, len(result.Sent), sentOK, sentFail)
		} else {
			logObj.WithContext(ctx).Warn().
				Str("event", "webhook_error").
				Int("alerts", result.AlertsCount).
				Str("error", result.Error).
				Str("remote_addr", ip).
				Msgf("[Webhook] 本请求处理失败: %s", result.Error)
		}

		w.Header().Set("Content-Type", "application/json")
		if !result.OK {
			w.WriteHeader(http.StatusBadRequest)
		}
		_ = json.NewEncoder(w).Encode(result)
	}
}
