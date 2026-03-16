package service

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackwhich/webhook_alerts/internal/config"
	"github.com/jackwhich/webhook_alerts/internal/logger"
	"github.com/jackwhich/webhook_alerts/internal/metrics"
	"github.com/jackwhich/webhook_alerts/internal/model"
	"github.com/jackwhich/webhook_alerts/internal/plotter"
)

func useProxyStr(use bool) string {
	if use {
		return "是"
	}
	return "否"
}

// ImageService 告警趋势图生成服务。
type ImageService struct {
	Config   *config.Config
	Filter   *ChannelFilter
	Plotter  *plotter.PrometheusPlotter
}

// GenerateImage 若来源为 prometheus/grafana 且存在需要出图的渠道，则返回 PNG 字节；否则返回 nil 并打日志说明原因。
func (s *ImageService) GenerateImage(ctx context.Context, logObj *logger.Logger, source string, alert *model.Alert, alertStatus string, targetChannels []string, alertname string) []byte {
	if source == "prometheus" {
		return s.generatePrometheusImage(ctx, logObj, alert, alertStatus, targetChannels, alertname)
	}
	if source == "grafana" {
		return s.generateGrafanaImage(alert, alertStatus, targetChannels, alertname)
	}
	return nil
}

func (s *ImageService) generatePrometheusImage(ctx context.Context, logObj *logger.Logger, alert *model.Alert, alertStatus string, targetChannels []string, alertname string) []byte {
	raw := s.Config.Raw
	imgCfg, _ := raw["prometheus_image"].(map[string]any)
	if imgCfg == nil {
		logObj.WithContext(ctx).Info().
			Str("event", "image_skip").
			Str("reason", "prometheus_image 未配置").
			Bool("prometheus_image_enabled", false).
			Bool("use_proxy", false).
			Msg("未生成图片：prometheus_image 未配置或未启用")
		return nil
	}
	enabled, _ := imgCfg["enabled"].(bool)
	useProxy := false
	if v, ok := imgCfg["use_proxy"].(bool); ok {
		useProxy = v
	}
	if !enabled {
		logObj.WithContext(ctx).Info().
			Str("event", "image_skip").
			Str("reason", "prometheus_image 未启用").
			Bool("prometheus_image_enabled", false).
			Bool("use_proxy", useProxy).
			Msg("未生成图片：prometheus_image 未配置或未启用")
		return nil
	}
	imageChannels := s.Filter.FilterImageChannels(targetChannels, alertStatus)
	if len(imageChannels) == 0 {
		logObj.WithContext(ctx).Info().
			Str("event", "image_skip").
			Str("reason", "无开启出图的渠道").
			Bool("prometheus_image_enabled", true).
			Bool("use_proxy", useProxy).
			Int("image_channels_count", 0).
			Strs("target_channels", targetChannels).
			Msg("未生成图片：当前路由渠道均未开启 image_enabled，或非 Telegram 渠道")
		return nil
	}
	datasource, _ := imgCfg["datasource"].(string)
	if datasource == "" {
		datasource = plotter.DatasourceAuto
	}
	promURL, _ := imgCfg["prometheus_url"].(string)
	channelNames := make([]string, 0, len(imageChannels))
	for _, ch := range imageChannels {
		if ch != nil && ch.Name != "" {
			channelNames = append(channelNames, ch.Name)
		}
	}
	logObj.WithContext(ctx).Info().
		Str("event", "image_try").
		Bool("prometheus_image_enabled", true).
		Bool("use_proxy", useProxy).
		Str("datasource", datasource).
		Int("image_channels_count", len(imageChannels)).
		Strs("image_channels", channelNames).
		Str("generator_url", alert.GeneratorURL).
		Str("prometheus_url", promURL).
		Msg("尝试生成趋势图（出图已开启，datasource=" + datasource + "，请求 VM/Prometheus 使用代理=" + useProxyStr(useProxy) + "）")
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
	injectLabels := false
	if v, ok := imgCfg["inject_labels"].(bool); ok {
		injectLabels = v
	}
	p := s.Plotter
	if p == nil {
		p = &plotter.PrometheusPlotter{
			BaseURL:      promURL,
			Lookback:     time.Duration(lookback) * time.Minute,
			Step:         step,
			Timeout:      time.Duration(timeoutSec) * time.Second,
			MaxSeries:    maxSeries,
			Datasource:   datasource,
			InjectLabels: injectLabels,
			HTTPClient: &http.Client{
				Timeout:   time.Duration(timeoutSec) * time.Second,
				Transport: &http.Transport{MaxIdleConnsPerHost: 10},
			},
		}
	}
	// 解析 generatorURL：先 URI 再 URL decode，便于排查「无数据」时是否拿到表达式
	var decodedExpr string
	if alert.GeneratorURL != "" {
		if u, parseErr := url.Parse(alert.GeneratorURL); parseErr == nil {
			decodedExpr = strings.TrimSpace(u.Query().Get("g0.expr"))
		}
	}
	logObj.WithContext(ctx).Info().
		Str("event", "generator_url_parsed").
		Str("generator_url", alert.GeneratorURL).
		Str("g0_expr_decoded", decodedExpr).
		Msg("解析 generatorURL：原始 URI → g0.expr URL 解码后（空表示 URL 无 g0.expr 或需从 annotations 取）")

	logExpr := func(expr string) {
		logObj.WithContext(ctx).Info().
			Str("event", "expr").
			Str("expr", expr).
			Msg("出图使用的表达式")
	}
	// 注意：日志里 expr 的 \" 是 JSON 序列化转义，实际发给 query_range 的 query 参数是原始 PromQL（无反斜杠）
	logQueryRangeResult := func(apiURL, expr string, resultCount int, status string) {
		ev := logObj.WithContext(ctx).Info().
			Str("event", "query_range").
			Str("api_url", apiURL).
			Str("expr", expr).
			Int("result_count", resultCount).
			Str("status", status)
		if resultCount < 0 {
			ev.Str("phase", "request").Msg("即将用解码后的表达式请求 query_range")
		} else {
			ev.Str("phase", "response").Msg("query_range 返回：result_count 与 status 见上")
		}
	}
	png, err := p.Generate(alert.GeneratorURL, alertname, alert.Labels, alert.Annotations, logExpr, logQueryRangeResult)
	if err != nil {
		metrics.ImageGeneratedTotal.WithLabelValues("prometheus", "fail").Inc()
		logObj.WithContext(ctx).Warn().
			Str("event", "image_fail").
			Str("alertname", alertname).
			Bool("prometheus_image_enabled", true).
			Bool("use_proxy", useProxy).
			Err(err).
			Msgf("未生成图片：%v", err)
		return nil
	}
	if png == nil {
		logObj.WithContext(ctx).Info().
			Str("event", "image_skip").
			Str("reason", "无数据或无表达式").
			Bool("prometheus_image_enabled", true).
			Bool("use_proxy", useProxy).
			Str("generator_url", alert.GeneratorURL).
			Msg("未生成图片：generatorURL 无 g0.expr 或 query_range 无数据（vmalert 需配 -external.alert.source 写入 expr）")
		return nil
	}
	metrics.ImageGeneratedTotal.WithLabelValues("prometheus", "ok").Inc()
	logObj.WithContext(ctx).Info().
		Str("event", "image_ok").
		Str("alertname", alertname).
		Bool("prometheus_image_enabled", true).
		Bool("use_proxy", useProxy).
		Str("generator_url", alert.GeneratorURL).
		Msg("趋势图生成成功，将随告警一并发送")
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
