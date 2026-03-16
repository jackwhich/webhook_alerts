package plotter

// Plotter 为告警生成图表图片（PNG）。
type Plotter interface {
	Generate(generatorURL, alertname string) ([]byte, error)
}
