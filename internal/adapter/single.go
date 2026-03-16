package adapter

import (
	"github.com/jackwhich/webhook_alerts/internal/model"
)

// ParseSingleAlert 解析单条告警格式（无 version/alerts 数组）。
func ParseSingleAlert(payload map[string]any) []*model.Alert {
	labels := toStringMap(payload["labels"])
	annotations := toStringMap(payload["annotations"])
	status := getStr(payload, "status")
	if status == "" {
		status = "firing"
	}
	return []*model.Alert{{
		Status:       status,
		Labels:       labels,
		Annotations:  annotations,
		StartsAt:     getStr(payload, "startsAt"),
		EndsAt:       getStr(payload, "endsAt"),
		GeneratorURL: getStr(payload, "generatorURL"),
		Source:       "unknown",
	}}
}
