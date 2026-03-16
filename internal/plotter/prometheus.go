// Package plotter 基于 Prometheus/VM query_range 与 go-charts 生成趋势图。
package plotter

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/alert-router-go/internal/metrics"
	charts "github.com/vicanso/go-charts/v2"
)

const pngSignature = "\x89PNG\r\n\x1a\n"

// PrometheusPlotter 从 Prometheus/VM query_range 生成趋势图。
type PrometheusPlotter struct {
	BaseURL    string
	Lookback   time.Duration
	Step       string
	Timeout    time.Duration
	MaxSeries  int
	HTTPClient *http.Client
}

// Generate 请求 query_range 并渲染 PNG，失败或无数据时返回 nil。
func (p *PrometheusPlotter) Generate(generatorURL, alertname string) ([]byte, error) {
	expr, err := parseExprFromGeneratorURL(generatorURL)
	if err != nil || expr == "" {
		return nil, err
	}
	base := p.BaseURL
	if base == "" {
		base, _ = baseFromURL(generatorURL)
	}
	if base == "" {
		return nil, fmt.Errorf("no prometheus base url")
	}
	apiURL := strings.TrimSuffix(base, "/") + "/api/v1/query_range"
	now := time.Now().UTC()
	start := now.Add(-p.Lookback)
	if p.Lookback < time.Minute {
		p.Lookback = 15 * time.Minute
		start = now.Add(-p.Lookback)
	}
	step := p.Step
	if step == "" {
		step = "30s"
	}
	params := url.Values{}
	params.Set("query", expr)
	params.Set("start", start.Format(time.RFC3339))
	params.Set("end", now.Format(time.RFC3339))
	params.Set("step", step)
	fullURL := apiURL + "?" + params.Encode()

	req, _ := http.NewRequest(http.MethodGet, fullURL, nil)
	client := p.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	if client.Timeout == 0 {
		client = &http.Client{Timeout: p.Timeout, Transport: client.Transport}
		if client.Timeout == 0 {
			client.Timeout = 8 * time.Second
		}
	}
	t0 := time.Now()
	resp, err := client.Do(req)
	metrics.PrometheusRequestDuration.Observe(time.Since(t0).Seconds())
	if err != nil {
		metrics.PrometheusRequestsTotal.WithLabelValues("error").Inc()
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		metrics.PrometheusRequestsTotal.WithLabelValues("error").Inc()
		return nil, fmt.Errorf("query_range status %d", resp.StatusCode)
	}
	metrics.PrometheusRequestsTotal.WithLabelValues("ok").Inc()

	var payload struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Values [][]interface{}  `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if decErr := json.NewDecoder(resp.Body).Decode(&payload); decErr != nil {
		return nil, decErr
	}
	if payload.Status != "success" || len(payload.Data.Result) == 0 {
		return nil, nil
	}
	result := payload.Data.Result
	if p.MaxSeries > 0 && len(result) > p.MaxSeries {
		result = result[:p.MaxSeries]
	}
	xLabels, seriesValues, legendLabels := parseResult(result)
	if len(xLabels) == 0 || len(seriesValues) == 0 {
		return nil, nil
	}
	png, err := renderLineChart(alertname, xLabels, seriesValues, legendLabels)
	if err != nil {
		metrics.ImageGeneratedTotal.WithLabelValues("prometheus", "fail").Inc()
		return nil, err
	}
	if len(png) >= len(pngSignature) && string(png[:len(pngSignature)]) != pngSignature {
		metrics.ImageGeneratedTotal.WithLabelValues("prometheus", "fail").Inc()
		return nil, fmt.Errorf("chart output is not PNG")
	}
	metrics.ImageGeneratedTotal.WithLabelValues("prometheus", "ok").Inc()
	return png, nil
}

func parseExprFromGeneratorURL(generatorURL string) (string, error) {
	u, err := url.Parse(generatorURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	expr := q.Get("g0.expr")
	return expr, nil
}

func baseFromURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	u.Path = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func parseResult(result []struct {
	Metric map[string]string `json:"metric"`
	Values [][]interface{}   `json:"values"`
}) (xLabels []string, values [][]float64, legends []string) {
	var allTimes []float64
	for _, s := range result {
		for _, pair := range s.Values {
			if len(pair) >= 2 {
				if t, ok := pair[0].(float64); ok {
					allTimes = append(allTimes, t)
				}
			}
		}
	}
	if len(allTimes) == 0 {
		return nil, nil, nil
	}
	timeSet := make(map[float64]struct{})
	for _, t := range allTimes {
		timeSet[t] = struct{}{}
	}
	for t := range timeSet {
		xLabels = append(xLabels, time.Unix(int64(t), 0).Format("15:04"))
	}
	// 使用第一条 series 的时间作为 X 轴
	for _, s := range result {
		if len(s.Values) == 0 {
			continue
		}
		var ys []float64
		for _, pair := range s.Values {
			if len(pair) >= 2 {
				if _, ok := pair[0].(float64); ok {
					if v, ok := pair[1].(float64); ok {
						ys = append(ys, v)
					}
				}
			}
		}
		if len(ys) > 0 {
			values = append(values, ys)
			legends = append(legends, buildLegend(s.Metric))
		}
	}
	if len(values) > 0 && len(values[0]) > 0 {
		xLabels = nil
		for _, pair := range result[0].Values {
			if len(pair) >= 1 {
				if t, ok := pair[0].(float64); ok {
					xLabels = append(xLabels, time.Unix(int64(t), 0).Format("15:04"))
				}
			}
		}
	}
	return xLabels, values, legends
}

func buildLegend(metric map[string]string) string {
	parts := []string{}
	for k, v := range metric {
		if k != "__name__" {
			parts = append(parts, k+"="+v)
		}
	}
	if len(parts) == 0 {
		return metric["__name__"]
	}
	return strings.Join(parts, ",")
}

func renderLineChart(title string, xLabels []string, seriesValues [][]float64, legendLabels []string) ([]byte, error) {
	if len(seriesValues) == 0 || len(seriesValues[0]) == 0 {
		return nil, fmt.Errorf("no data")
	}
	if len(xLabels) == 0 {
		xLabels = make([]string, len(seriesValues[0]))
		for i := range xLabels {
			xLabels[i] = fmt.Sprintf("%d", i)
		}
	}
	opts := []charts.OptionFunc{
		charts.TitleTextOptionFunc(title),
		charts.XAxisDataOptionFunc(xLabels),
		charts.LegendLabelsOptionFunc(legendLabels, charts.PositionRight),
		charts.ThemeOptionFunc(charts.ThemeDark),
	}
	painter, err := charts.LineRender(seriesValues, opts...)
	if err != nil {
		return nil, err
	}
	return painter.Bytes()
}
