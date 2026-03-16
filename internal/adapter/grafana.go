package adapter

import (
	"github.com/jackwhich/webhook_alerts/internal/model"
)

// ParseGrafana 解析 Grafana 统一告警 Webhook 为标准告警列表。
func ParseGrafana(payload map[string]any) []*model.Alert {
	rawAlerts, _ := payload["alerts"].([]any)
	if len(rawAlerts) == 0 {
		return nil
	}
	receiver := getStr(payload, "receiver")
	if receiver == "" {
		receiver = getStr(payload, "Receiver")
	}
	var out []*model.Alert
	for _, a := range rawAlerts {
		m, _ := a.(map[string]any)
		if m == nil {
			continue
		}
		labels := toStringMap(m["labels"])
		annotations := toStringMap(m["annotations"])
		if v := parseGrafanaCurrentValue(m); v != "" {
			if annotations == nil {
				annotations = make(map[string]string)
			}
			annotations["当前值"] = v
		}
		alert := &model.Alert{
			Status:       getStr(m, "status"),
			Labels:       labels,
			Annotations:  annotations,
			StartsAt:     getStr(m, "startsAt"),
			EndsAt:       getStr(m, "endsAt"),
			GeneratorURL: getStr(m, "generatorURL"),
			Fingerprint:  getStr(m, "fingerprint"),
			Receiver:     receiver,
		}
		if alert.Status == "" {
			alert.Status = "firing"
		}
		out = append(out, alert)
	}
	return out
}

func parseGrafanaCurrentValue(alert map[string]any) string {
	if values, ok := alert["values"].(map[string]any); ok {
		if b, ok := values["B"]; ok {
			if s, ok := b.(string); ok {
				return s
			}
		}
	}
	// 回退：从 valueString 正则提取
	valueStr := getStr(alert, "valueString")
	if valueStr != "" {
		// 简单匹配 value=数字
		for i := 0; i < len(valueStr)-1; i++ {
			if valueStr[i] == 'v' && i+7 <= len(valueStr) && valueStr[i:i+7] == "value=" {
				j := i + 7
				for j < len(valueStr) && (valueStr[j] >= '0' && valueStr[j] <= '9') {
					j++
				}
				if j > i+7 {
					return valueStr[i+7 : j]
				}
				break
			}
		}
	}
	return ""
}
