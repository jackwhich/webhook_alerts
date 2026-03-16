package adapter

import (
	"github.com/jackwhich/webhook_alerts/internal/model"
)

// DataSource 表示 Webhook 数据源类型。
type DataSource int

const (
	DataSourceUnknown DataSource = iota
	DataSourcePrometheus
	DataSourceGrafana
	DataSourceSingleAlert
)

// IdentifyDataSource 根据 payload 判断来源（version "1"=Grafana，"4"=Prometheus）。
func IdentifyDataSource(payload map[string]any) DataSource {
	if payload == nil {
		return DataSourceUnknown
	}
	_, hasAlerts := payload["alerts"].([]any)
	if !hasAlerts {
		if _, hasLabels := payload["labels"]; hasLabels {
			return DataSourceSingleAlert
		}
		if _, hasAnnotations := payload["annotations"]; hasAnnotations {
			return DataSourceSingleAlert
		}
		return DataSourceUnknown
	}
	switch payload["version"] {
	case "1":
		return DataSourceGrafana
	case "4":
		return DataSourcePrometheus
	}
	return DataSourceUnknown
}

// Normalize 解析 Webhook 负载，返回标准化告警列表（已设置 Source）。
func Normalize(payload map[string]any) []*model.Alert {
	switch IdentifyDataSource(payload) {
	case DataSourcePrometheus:
		alerts := ParsePrometheus(payload)
		for _, a := range alerts {
			a.Source = "prometheus"
		}
		return alerts
	case DataSourceGrafana:
		alerts := ParseGrafana(payload)
		for _, a := range alerts {
			a.Source = "grafana"
		}
		return alerts
	case DataSourceSingleAlert:
		return ParseSingleAlert(payload)
	default:
		return nil
	}
}
