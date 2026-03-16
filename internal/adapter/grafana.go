package adapter

import (
	"fmt"
	"regexp"

	"github.com/jackwhich/webhook_alerts/internal/model"
)

// valueString 中 var='B' value=数字 的正则，与 Python _parse_current_value 一致
var reValueStringB = regexp.MustCompile(`var='B' labels=\{.*?\} value=([\d.]+)`)

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

// parseGrafanaCurrentValue 从 Grafana 告警解析「当前值」，与 Python _parse_current_value 一致：优先 values.B，否则从 valueString 正则提取。
func parseGrafanaCurrentValue(alert map[string]any) string {
	if values, ok := alert["values"].(map[string]any); ok {
		if b, ok := values["B"]; ok && b != nil {
			return anyToStr(b)
		}
	}
	valueStr := getStr(alert, "valueString")
	if valueStr != "" {
		if m := reValueStringB.FindStringSubmatch(valueStr); len(m) >= 2 {
			return m[1]
		}
		// 回退：任意 value=数字或小数
		fallback := regexp.MustCompile(`value=([\d.]+)`)
		if m := fallback.FindStringSubmatch(valueStr); len(m) >= 2 {
			return m[1]
		}
	}
	return ""
}

func anyToStr(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return fmt.Sprintf("%v", x)
	case int:
		return fmt.Sprintf("%d", x)
	case int64:
		return fmt.Sprintf("%d", x)
	default:
		return fmt.Sprint(v)
	}
}
