package plotter

// GenerateGrafana 从 Grafana 告警生成趋势图（从规则/面板取查询）。
// 占位：未实现；后续可由 ImageService 提取查询后调用 PrometheusPlotter。
func GenerateGrafana(generatorURL, alertname string) ([]byte, error) {
	return nil, nil
}
