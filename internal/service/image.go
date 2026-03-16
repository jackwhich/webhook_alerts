package service

import (
	"net/http"
	"time"

	"github.com/alert-router-go/internal/config"
	"github.com/alert-router-go/internal/metrics"
	"github.com/alert-router-go/internal/model"
	"github.com/alert-router-go/internal/plotter"
)

// ImageService 告警趋势图生成服务。
type ImageService struct {
	Config   *config.Config
	Filter   *ChannelFilter
	Plotter  *plotter.PrometheusPlotter
}

// GenerateImage 若来源为 prometheus/grafana 且存在需要出图的渠道，则返回 PNG 字节。
func (s *ImageService) GenerateImage(source string, alert *model.Alert, alertStatus string, targetChannels []string, alertname string) []byte {
	if source == "prometheus" {
		return s.generatePrometheusImage(alert, alertStatus, targetChannels, alertname)
	}
	if source == "grafana" {
		return s.generateGrafanaImage(alert, alertStatus, targetChannels, alertname)
	}
	return nil
}

func (s *ImageService) generatePrometheusImage(alert *model.Alert, alertStatus string, targetChannels []string, alertname string) []byte {
	raw := s.Config.Raw
	imgCfg, _ := raw["prometheus_image"].(map[string]any)
	if imgCfg == nil {
		return nil
	}
	enabled, _ := imgCfg["enabled"].(bool)
	if !enabled {
		return nil
	}
	imageChannels := s.Filter.FilterImageChannels(targetChannels, alertStatus)
	if len(imageChannels) == 0 {
		return nil
	}
	promURL, _ := imgCfg["prometheus_url"].(string)
	lookback := 15
	if v, ok := imgCfg["lookback_minutes"].(int); ok && v > 0 {
		lookback = v
	}
	step := "30s"
	if v, ok := imgCfg["step"].(string); ok {
		step = v
	}
	timeoutSec := 8
	if v, ok := imgCfg["timeout_seconds"].(int); ok && v > 0 {
		timeoutSec = v
	}
	maxSeries := 8
	if v, ok := imgCfg["max_series"].(int); ok && v > 0 {
		maxSeries = v
	}
	p := s.Plotter
	if p == nil {
		p = &plotter.PrometheusPlotter{
			BaseURL:   promURL,
			Lookback:  time.Duration(lookback) * time.Minute,
			Step:      step,
			Timeout:   time.Duration(timeoutSec) * time.Second,
			MaxSeries: maxSeries,
			HTTPClient: &http.Client{
				Timeout: time.Duration(timeoutSec) * time.Second,
				Transport: &http.Transport{MaxIdleConnsPerHost: 10},
			},
		}
	}
	png, err := p.Generate(alert.GeneratorURL, alertname)
	if err != nil {
		metrics.ImageGeneratedTotal.WithLabelValues("prometheus", "fail").Inc()
		return nil
	}
	if png != nil {
		metrics.ImageGeneratedTotal.WithLabelValues("prometheus", "ok").Inc()
	}
	return png
}

func (s *ImageService) generateGrafanaImage(alert *model.Alert, alertStatus string, targetChannels []string, alertname string) []byte {
	imageChannels := s.Filter.FilterImageChannels(targetChannels, alertStatus)
	if len(imageChannels) == 0 {
		return nil
	}
	// 占位：Grafana 出图尚未实现
	_ = alert
	_ = alertname
	return nil
}
