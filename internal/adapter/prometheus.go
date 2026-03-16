package adapter

import (
	"github.com/alert-router-go/internal/model"
)

// ParsePrometheus 解析 Prometheus Alertmanager Webhook 为标准告警列表。
func ParsePrometheus(payload map[string]any) []*model.Alert {
	rawAlerts, _ := payload["alerts"].([]any)
	if len(rawAlerts) == 0 {
		return nil
	}
	receiver := getStr(payload, "receiver")
	payloadStatus := getStr(payload, "status")
	if payloadStatus == "" {
		payloadStatus = "firing"
	}
	var out []*model.Alert
	for _, a := range rawAlerts {
		m, _ := a.(map[string]any)
		if m == nil {
			continue
		}
		labels := toStringMap(m["labels"])
		annotations := toStringMap(m["annotations"])
		status := getStr(m, "status")
		if status == "" {
			status = payloadStatus
		}
		alert := &model.Alert{
			Status:       status,
			Labels:       labels,
			Annotations:  annotations,
			StartsAt:     getStr(m, "startsAt"),
			EndsAt:       getStr(m, "endsAt"),
			GeneratorURL: getStr(m, "generatorURL"),
			Receiver:     receiver,
		}
		if alert.GeneratorURL == "" {
			alert.GeneratorURL = getStr(payload, "externalURL")
		}
		out = append(out, alert)
	}
	return out
}

func getStr(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, _ := m[key].(string)
	return v
}

func toStringMap(v any) map[string]string {
	out := make(map[string]string)
	m, ok := v.(map[string]any)
	if !ok {
		return out
	}
	for k, val := range m {
		if s, ok := val.(string); ok {
			out[k] = s
		}
	}
	return out
}
