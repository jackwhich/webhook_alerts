package service

import (
	"github.com/alert-router-go/internal/adapter"
	"github.com/alert-router-go/internal/config"
	"github.com/alert-router-go/internal/metrics"
	"github.com/alert-router-go/internal/model"
	"github.com/alert-router-go/internal/routing"
	"github.com/alert-router-go/internal/sender"
	"github.com/alert-router-go/internal/template"
)

// AlertService 处理 Webhook 请求并路由告警到各渠道。
type AlertService struct {
	Config        *config.Config
	ChannelFilter *ChannelFilter
	ImageService  *ImageService
}

// ProcessWebhookResult 返回给 HTTP 处理器的结果。
type ProcessWebhookResult struct {
	OK   bool                 `json:"ok"`
	Sent []sender.SendResult  `json:"sent,omitempty"`
	Error string              `json:"error,omitempty"`
}

// ProcessWebhook 解析负载、路由每条告警、按需出图并发送。
func (s *AlertService) ProcessWebhook(payload map[string]any) ProcessWebhookResult {
	alerts := adapter.Normalize(payload)
	if len(alerts) == 0 {
		return ProcessWebhookResult{OK: false, Error: "无法解析告警数据格式"}
	}
	for _, a := range alerts {
		metrics.AlertsReceivedTotal.WithLabelValues(a.Source, a.Status).Inc()
	}
	var results []sender.SendResult
	for _, alert := range alerts {
		results = append(results, s.processSingleAlert(alert)...)
	}
	return ProcessWebhookResult{OK: true, Sent: results}
}

func (s *AlertService) processSingleAlert(alert *model.Alert) (out []sender.SendResult) {
	alertname := alert.GetLabel("alertname")
	if alertname == "" {
		alertname = "Unknown"
	}
	alertStatus := alert.Status

		// Jenkins 去重
		if routing.ShouldSkipJenkinsFiring(alert, s.Config.Raw) {
			metrics.AlertsDedupSkippedTotal.WithLabelValues("jenkins").Inc()
			return []sender.SendResult{{Channel: "", Success: false}}
		}
		// Grafana 去重
		if alert.Source == "grafana" && routing.ShouldSkipGrafanaDuplicate(alert, s.Config.Raw) {
			metrics.AlertsDedupSkippedTotal.WithLabelValues("grafana").Inc()
			return []sender.SendResult{{Channel: "", Success: false}}
		}

		matchLabels := routing.MatchLabels(alert)
		targetChannels := routing.Route(matchLabels, s.Config)
		for _, ch := range targetChannels {
			metrics.AlertsRoutedTotal.WithLabelValues(ch).Inc()
		}

		// 生成趋势图
	var imageBytes []byte
	if s.ImageService != nil {
		imageBytes = s.ImageService.GenerateImage(alert.Source, alert, alertStatus, targetChannels, alertname)
	}

		// 按渠道发送
	var results []sender.SendResult
	for _, channelName := range targetChannels {
		ch := s.Config.Channels[channelName]
		if ch == nil || !ch.Enabled {
			continue
		}
		if alertStatus == "resolved" && !ch.SendResolved {
			continue
		}
		body, err := template.Render(ch.Template, alert)
		if err != nil {
			results = append(results, sender.SendResult{Channel: channelName, Success: false, Reason: "invalid_response"}) // 渲染失败
			continue
		}
		var res sender.SendResult
		if ch.Type == "telegram" {
			useImage := ch.ImageEnabled && len(imageBytes) > 0
			if useImage {
				res = sender.SendTelegram(ch, body, imageBytes)
			} else {
				res = sender.SendTelegram(ch, body, nil)
			}
		} else {
			res = sender.SendWebhook(ch, body)
		}
		results = append(results, res)
	}
	return results
}

// buildContext 构建模板上下文（标题前缀等）。
func (s *AlertService) buildContext(alert *model.Alert) map[string]any {
	defaults, _ := s.Config.Raw["defaults"].(map[string]any)
	titlePrefix := "[ALERT]"
	if defaults != nil {
		if v, ok := defaults["title_prefix"].(string); ok {
			titlePrefix = v
		}
	}
	alertname := alert.GetLabel("alertname")
	if alertname == "" {
		alertname = "Unknown"
	}
	return map[string]any{
		"title":       titlePrefix + " " + alertname,
		"status":      alert.Status,
		"labels":      alert.Labels,
		"annotations": alert.Annotations,
		"startsAt":    alert.StartsAt,
		"endsAt":      alert.EndsAt,
		"generatorURL": alert.GeneratorURL,
	}
}
