package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/alert-router-go/internal/metrics"
	"github.com/alert-router-go/internal/service"
)

// Webhook 处理 POST /webhook（Alertmanager / Grafana 告警入口）。
func Webhook(alertSvc *service.AlertService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			metrics.WebhookRequestsTotal.WithLabelValues("error").Inc()
			http.Error(w, "仅支持 POST", http.StatusMethodNotAllowed)
			return
		}
		t0 := time.Now()
		defer func() {
			metrics.WebhookRequestDuration.Observe(time.Since(t0).Seconds())
		}()

		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			metrics.WebhookRequestsTotal.WithLabelValues("invalid_json").Inc()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "JSON 格式无效"})
			return
		}
		result := alertSvc.ProcessWebhook(payload)
		status := "ok"
		if !result.OK {
			status = "error"
		}
		metrics.WebhookRequestsTotal.WithLabelValues(status).Inc()
		w.Header().Set("Content-Type", "application/json")
		if !result.OK {
			w.WriteHeader(http.StatusBadRequest)
		}
		_ = json.NewEncoder(w).Encode(result)
	}
}
