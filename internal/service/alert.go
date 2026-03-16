package service

import (
	"context"
	"strings"

	"github.com/jackwhich/webhook_alerts/internal/adapter"
	"github.com/jackwhich/webhook_alerts/internal/config"
	"github.com/jackwhich/webhook_alerts/internal/logger"
	"github.com/jackwhich/webhook_alerts/internal/metrics"
	"github.com/jackwhich/webhook_alerts/internal/model"
	"github.com/jackwhich/webhook_alerts/internal/routing"
	"github.com/jackwhich/webhook_alerts/internal/sender"
	"github.com/jackwhich/webhook_alerts/internal/template"
)

// AlertService 处理 Webhook 请求并路由告警到各渠道。
type AlertService struct {
	Config        *config.Config
	ChannelFilter *ChannelFilter
	ImageService  *ImageService
}

// reasonForLog 将发送失败原因码转为中文，用于日志展示（指标仍用英文 reason）。
func reasonForLog(reason string) string {
	switch reason {
	case "timeout":
		return "超时"
	case "http_error":
		return "HTTP 错误"
	case "invalid_response":
		return "响应无效"
	case "network":
		return "网络异常"
	case "template_render":
		return "模板渲染失败"
	default:
		if reason == "" {
			return "未知原因"
		}
		return reason
	}
}

// ProcessWebhookResult 返回给 HTTP 处理器的结果。
type ProcessWebhookResult struct {
	OK          bool                `json:"ok"`
	AlertsCount int                 `json:"-"` // 解析出的告警条数，供日志使用
	Sent        []sender.SendResult  `json:"sent,omitempty"`
	Error       string               `json:"error,omitempty"`
}

// ProcessWebhook 解析负载、路由每条告警、按需出图并发送。ctx/logObj 用于打 Py 风格日志（便于排查请求来源、路由、各渠道发送结果）。
func (s *AlertService) ProcessWebhook(ctx context.Context, logObj *logger.Logger, payload map[string]any) ProcessWebhookResult {
	alerts := adapter.Normalize(payload)
	if len(alerts) == 0 {
		return ProcessWebhookResult{OK: false, AlertsCount: 0, Error: "无法解析告警数据格式"}
	}
	for _, a := range alerts {
		metrics.AlertsReceivedTotal.WithLabelValues(a.Source, a.Status).Inc()
	}
	summary := make([]string, 0, len(alerts))
	for _, a := range alerts {
		name := a.GetLabel("alertname")
		if name == "" {
			name = "?"
		}
		summary = append(summary, name)
	}
	logObj.WithContext(ctx).Info().
		Str("event", "alerts_parsed").
		Int("alerts", len(alerts)).
		Str("alert_summary", strings.Join(summary, ", ")).
		Msgf("[Webhook] 解析告警完成: 共 %d 条 [%s]", len(alerts), strings.Join(summary, ", "))

	var results []sender.SendResult
	for _, alert := range alerts {
		results = append(results, s.processSingleAlert(ctx, logObj, alert)...)
	}
	return ProcessWebhookResult{OK: true, AlertsCount: len(alerts), Sent: results}
}

func (s *AlertService) processSingleAlert(ctx context.Context, logObj *logger.Logger, alert *model.Alert) (out []sender.SendResult) {
	alertname := alert.GetLabel("alertname")
	if alertname == "" {
		alertname = "未知"
	}
	alertStatus := alert.Status

	// Jenkins 去重
	if routing.ShouldSkipJenkinsFiring(alert, s.Config.Raw) {
		metrics.AlertsDedupSkippedTotal.WithLabelValues("jenkins").Inc()
		logObj.WithContext(ctx).Info().
			Str("event", "dedup_jenkins").
			Str("alertname", alertname).
			Msgf("[Webhook] 告警 %s 命中 Jenkins 去重窗口，跳过重复触发通知", alertname)
		return []sender.SendResult{{Channel: "", Success: false}}
	}
	// Grafana 去重
	if alert.Source == "grafana" && routing.ShouldSkipGrafanaDuplicate(alert, s.Config.Raw) {
		metrics.AlertsDedupSkippedTotal.WithLabelValues("grafana").Inc()
		logObj.WithContext(ctx).Info().
			Str("event", "dedup_grafana").
			Str("alertname", alertname).
			Msgf("[Webhook] 告警 %s 命中 Grafana 去重窗口，跳过重复通知", alertname)
		return []sender.SendResult{{Channel: "", Success: false}}
	}

	matchLabels := routing.MatchLabels(alert)
	targetChannels := routing.Route(matchLabels, s.Config)
	for _, ch := range targetChannels {
		metrics.AlertsRoutedTotal.WithLabelValues(ch).Inc()
	}
	logObj.WithContext(ctx).Info().
		Str("event", "alert_route").
		Str("alertname", alertname).
		Strs("channels", targetChannels).
		Msgf("[Webhook] 告警 %s 路由到渠道: %v", alertname, targetChannels)

	// 生成趋势图
	var imageBytes []byte
	if s.ImageService != nil {
		imageBytes = s.ImageService.GenerateImage(ctx, logObj, alert.Source, alert, alertStatus, targetChannels, alertname)
	}
	sendMode := "图片+文本"
	if len(imageBytes) == 0 {
		sendMode = "纯文本"
	}
	logObj.WithContext(ctx).Info().
		Str("event", "alert_send_start").
		Str("alertname", alertname).
		Int("channel_count", len(targetChannels)).
		Str("send_mode", sendMode).
		Msgf("[Webhook] 告警 %s 将向 %d 个渠道发送 (方式: %s)", alertname, len(targetChannels), sendMode)

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
			results = append(results, sender.SendResult{Channel: channelName, Success: false, Reason: "invalid_response"})
			logObj.WithContext(ctx).Warn().
				Str("event", "send_fail").
				Str("alertname", alertname).
				Str("channel", channelName).
				Str("reason", "template_render").
				Err(err).
				Msgf("[Webhook] 告警 %s 渠道 %s 模板渲染失败，跳过", alertname, channelName)
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
		if res.Success {
			logObj.WithContext(ctx).Info().
				Str("event", "send_ok").
				Str("alertname", alertname).
				Str("channel", channelName).
				Str("channel_type", ch.Type).
				Msgf("[Webhook] 告警 %s 渠道 %s (%s) 发送成功", alertname, channelName, ch.Type)
		} else {
			ev := logObj.WithContext(ctx).Warn().
				Str("event", "send_fail").
				Str("alertname", alertname).
				Str("channel", channelName).
				Str("channel_type", ch.Type).
				Str("reason", res.Reason)
			if res.Detail != "" {
				ev = ev.Str("detail", res.Detail)
			}
			msg := reasonForLog(res.Reason)
			if res.Detail != "" {
				msg = msg + " (" + res.Detail + ")"
			}
			ev.Msgf("[Webhook] 告警 %s 渠道 %s (%s) 发送失败: %s", alertname, channelName, ch.Type, msg)
		}
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
		alertname = "未知"
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
