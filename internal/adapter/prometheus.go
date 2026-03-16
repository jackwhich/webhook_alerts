package adapter

import (
	"strings"

	"github.com/jackwhich/webhook_alerts/internal/model"
)

// 同组多条告警合并时，这些 label 在不同告警中可能有不同值，需汇总为多值（逗号分隔）。
var collectableLabels = []string{
	"replica", "pod", "instance", "service_name", "consumergroup", "topic",
	"jenkins_job", "device", "container", "build_number", "status",
}

// ParsePrometheus 解析 Prometheus Alertmanager Webhook 为标准告警列表；同组多条告警（有 groupKey）合并为一条，与 Python 逻辑一致。
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
	groupKey := getStr(payload, "groupKey")

	// 同组多条告警合并为一条发送（与 Python prometheus_adapter 一致）
	if len(rawAlerts) > 1 && groupKey != "" {
		merged := mergePrometheusAlerts(payload, rawAlerts, receiver, payloadStatus)
		return []*model.Alert{merged}
	}

	var out []*model.Alert
	for _, a := range rawAlerts {
		m, _ := a.(map[string]any)
		if m == nil {
			continue
		}
		alert := buildOneAlert(m, payload, receiver, payloadStatus)
		if alert != nil {
			out = append(out, alert)
		}
	}
	return out
}

// mergePrometheusAlerts 将同组多条告警合并为一条：共同 label 保留，可汇总 label 收集多值（逗号分隔）。
func mergePrometheusAlerts(payload map[string]any, rawAlerts []any, receiver, payloadStatus string) *model.Alert {
	firstMap, _ := rawAlerts[0].(map[string]any)
	firstLabels := toStringMap(firstMap["labels"])
	collectableSet := make(map[string]struct{})
	for _, k := range collectableLabels {
		collectableSet[k] = struct{}{}
	}

	// 共同 label：不在 collectable 中，且在每条告警中取值相同
	commonLabels := make(map[string]string)
	for k, v := range firstLabels {
		if _, skip := collectableSet[k]; skip {
			continue
		}
		allSame := true
		for i := 1; i < len(rawAlerts); i++ {
			m, _ := rawAlerts[i].(map[string]any)
			lbl := toStringMap(m["labels"])
			if lbl[k] != v {
				allSame = false
				break
			}
		}
		if allSame {
			commonLabels[k] = v
		}
	}

	// 从 payload commonLabels 取基础，再覆盖我们算出的 common + 收集的多值
	mergedLabels := toStringMap(payload["commonLabels"])
	if mergedLabels == nil {
		mergedLabels = make(map[string]string)
	}
	for k, v := range commonLabels {
		mergedLabels[k] = v
	}
	// 收集可汇总 label 的多值（去重，逗号连接）
	for _, labelKey := range collectableLabels {
		var values []string
		seen := make(map[string]struct{})
		for _, a := range rawAlerts {
			m, _ := a.(map[string]any)
			lbl := toStringMap(m["labels"])
			v := lbl[labelKey]
			if v != "" {
				if _, ok := seen[v]; !ok {
					seen[v] = struct{}{}
					values = append(values, v)
				}
			}
		}
		if len(values) > 0 {
			mergedLabels[labelKey] = strings.Join(values, ",")
		}
	}

	// 使用 commonAnnotations，若无 summary 则用第一条的
	mergedAnnotations := toStringMap(payload["commonAnnotations"])
	if mergedAnnotations == nil {
		mergedAnnotations = make(map[string]string)
	}
	firstAnn := toStringMap(firstMap["annotations"])
	if mergedAnnotations["summary"] == "" && firstAnn["summary"] != "" {
		mergedAnnotations["summary"] = firstAnn["summary"]
	}
	if len(mergedAnnotations) == 0 {
		mergedAnnotations = firstAnn
	}

	status := getStr(payload, "status")
	if status == "" {
		status = getStr(firstMap, "status")
	}
	if status == "" {
		status = payloadStatus
	}
	generatorURL := getStr(firstMap, "generatorURL")
	if generatorURL == "" {
		generatorURL = getStr(payload, "externalURL")
	}
	return &model.Alert{
		Status:       status,
		Labels:       mergedLabels,
		Annotations:  mergedAnnotations,
		StartsAt:     getStr(firstMap, "startsAt"),
		EndsAt:       getStr(firstMap, "endsAt"),
		GeneratorURL: generatorURL,
		Receiver:     receiver,
	}
}

func buildOneAlert(m map[string]any, payload map[string]any, receiver, payloadStatus string) *model.Alert {
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
	return alert
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
