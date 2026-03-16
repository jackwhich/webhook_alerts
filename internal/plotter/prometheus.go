// Package plotter 基于 Prometheus/VM query_range 与 go-charts 生成趋势图。
package plotter

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackwhich/webhook_alerts/internal/metrics"
	charts "github.com/vicanso/go-charts/v2"
)

const pngSignature = "\x89PNG\r\n\x1a\n"

// 数据源类型：config 中 datasource 可选值，用于推断是否注入 label。
const (
	DatasourceAuto           = "auto"
	DatasourcePrometheus     = "prometheus"
	DatasourceVictoriaMetrics = "victoriametrics"
)

// 仅用于路由/告警的 label，不注入到查询表达式。
var alertOnlyLabels = map[string]struct{}{
	"alertname": {}, "severity": {}, "cluster": {}, "_source": {}, "_receiver": {},
}

// normalizeQueryForPlot 将告警表达式转换为更适合出图的查询：剥离末尾的标量比较（如 > 30、>= 0.8），
// 否则 query_range 返回 0/1 布尔值，图上只显示 0-1 而非真实指标值。与 Python _normalize_query_for_plot 一致。
func normalizeQueryForPlot(expr string) string {
	if expr == "" {
		return expr
	}
	normalized := strings.TrimSpace(expr)
	// 从字符串末尾匹配：可选的空白 + 比较符 + 可选的 bool + 数字 + 结尾空白
	suffixRe := regexp.MustCompile(`\s*(?:>=|<=|==|!=|>|<)\s*(?:bool\s+)?(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)\s*$`)
	if loc := suffixRe.FindStringIndex(normalized); loc != nil {
		if base := strings.TrimSpace(normalized[:loc[0]]); base != "" {
			return base
		}
	}
	return normalized
}

// isDatasourceVictoriaMetrics 根据 generatorURL 判断是否来自 VictoriaMetrics（vmalert / vmselect / 带 /select/ 的 VM 集群）。
func isDatasourceVictoriaMetrics(generatorURL string) bool {
	if generatorURL == "" {
		return false
	}
	lower := strings.ToLower(generatorURL)
	return strings.Contains(lower, "victoriametrics") ||
		strings.Contains(lower, "vmselect") ||
		strings.Contains(lower, "vmalert") ||
		strings.Contains(generatorURL, "/select/")
}

// injectAlertLabelsIntoExpr 将告警的 label 注入到查询表达式的第一个 selector 中，收窄查询（仅标量、且未在 selector 中存在的 key）。
func injectAlertLabelsIntoExpr(expr string, labels map[string]string) string {
	if expr == "" || len(labels) == 0 {
		return expr
	}
	matchLabels := make(map[string]string)
	for k, v := range labels {
		if _, skip := alertOnlyLabels[k]; skip || v == "" || strings.Contains(v, ",") {
			continue
		}
		matchLabels[k] = v
	}
	if len(matchLabels) == 0 {
		return expr
	}
	start := strings.Index(expr, "{")
	if start == -1 {
		return expr
	}
	depth := 1
	i := start + 1
	for i < len(expr) && depth > 0 {
		if expr[i] == '{' {
			depth++
		} else if expr[i] == '}' {
			depth--
		}
		i++
	}
	if depth != 0 {
		return expr
	}
	end := i - 1
	inner := expr[start+1 : end]
	existingKeys := regexp.MustCompile(`(\w+)\s*[=~]`).FindAllStringSubmatch(inner, -1)
	existingSet := make(map[string]struct{})
	for _, m := range existingKeys {
		if len(m) >= 2 {
			existingSet[m[1]] = struct{}{}
		}
	}
	var toAdd []string
	for k, v := range matchLabels {
		if _, exists := existingSet[k]; exists {
			continue
		}
		escaped := strings.ReplaceAll(v, "\\", "\\\\")
		escaped = strings.ReplaceAll(escaped, "\"", "\\\"")
		toAdd = append(toAdd, k+"=\""+escaped+"\"")
	}
	if len(toAdd) == 0 {
		return expr
	}
	sort.Strings(toAdd)
	return expr[:end] + "," + strings.Join(toAdd, ",") + expr[end:]
}

// PrometheusPlotter 从 Prometheus/VM query_range 生成趋势图。
type PrometheusPlotter struct {
	BaseURL      string        // Prometheus/VM API 根地址
	Lookback     time.Duration
	Step         string
	Timeout      time.Duration
	MaxSeries    int
	HTTPClient   *http.Client
	Datasource   string // DatasourceAuto | DatasourcePrometheus | DatasourceVictoriaMetrics；auto 时按 generatorURL 推断
	InjectLabels bool   // 仅当 Datasource 为 prometheus 时生效：是否向表达式注入 label 收窄查询
}

// annotationKeysForExpr 当 generatorURL 无 g0.expr 时，尝试从 annotations 取表达式的 key 顺序（vmalert 等可能把 expr 放在这里）。
var annotationKeysForExpr = []string{"expr", "query", "__expr__"}

// Generate 请求 query_range 并渲染 PNG，失败或无数据时返回 nil。
// 步骤：1) 从告警 Graph URL 取 g0.expr（已 URL decode）；2) 若无则从 annotations 取 expr/query/__expr__；3) 调 VM/Prometheus API 时优先用 config 的 prometheus_url（与 Python 一致，内网可达），否则才用 generatorURL 解析。labels 收窄查询；logExpr 可选，用于打日志。
func (p *PrometheusPlotter) Generate(generatorURL, alertname string, labels map[string]string, annotations map[string]string, logExpr func(expr string)) ([]byte, error) {
	expr, err := parseExprFromGeneratorURL(generatorURL)
	if (err != nil || expr == "") && len(annotations) > 0 {
		for _, key := range annotationKeysForExpr {
			if v := annotations[key]; v != "" {
				expr = strings.TrimSpace(v)
				err = nil
				break
			}
		}
	}
	if err != nil || expr == "" {
		return nil, err
	}
	// 与 Python 一致：先做「阈值比较剥离」，再按数据源决定是否注入 label
	plotExpr := normalizeQueryForPlot(expr)
	// 数据源：auto 时按 generatorURL 推断 Prometheus / VictoriaMetrics
	effectiveDS := strings.TrimSpace(strings.ToLower(p.Datasource))
	if effectiveDS == "" {
		effectiveDS = DatasourceAuto
	}
	if effectiveDS == DatasourceAuto {
		if isDatasourceVictoriaMetrics(generatorURL) {
			effectiveDS = DatasourceVictoriaMetrics
		} else {
			effectiveDS = DatasourcePrometheus
		}
	}
	shouldInject := len(labels) > 0 &&
		((effectiveDS == DatasourceVictoriaMetrics) || (effectiveDS == DatasourcePrometheus && p.InjectLabels))
	if shouldInject {
		plotExpr = injectAlertLabelsIntoExpr(plotExpr, labels)
	}
	expr = plotExpr
	if logExpr != nil {
		logExpr(expr)
	}

	// 出图只使用配置的 prometheus_url 拉取数据，不做 generatorURL 兜底
	base := p.BaseURL
	if base == "" {
		return nil, fmt.Errorf("未配置 Prometheus/VM 根地址，请配置 prometheus_image.prometheus_url")
	}
	// VM/Prometheus 查询一段时间数据：/api/v1/query_range，与 curl --data-urlencode 用法一致（POST + form）
	apiURL := strings.TrimSuffix(base, "/") + "/api/v1/query_range"
	now := time.Now().UTC()
	startTime := now.Add(-p.Lookback)
	if p.Lookback < time.Minute {
		p.Lookback = 15 * time.Minute
		startTime = now.Add(-p.Lookback)
	}
	step := p.Step
	if step == "" {
		step = "30s"
	}
	params := url.Values{}
	params.Set("query", expr)
	params.Set("start", fmt.Sprintf("%d", startTime.Unix()))   // Unix 秒，与 VM 示例一致
	params.Set("end", fmt.Sprintf("%d", now.Unix()))
	params.Set("step", step)

	req, _ := http.NewRequest(http.MethodPost, apiURL, strings.NewReader(params.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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
		return nil, fmt.Errorf("query_range 请求返回状态码 %d", resp.StatusCode)
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
		return nil, fmt.Errorf("图表输出不是有效 PNG")
	}
	metrics.ImageGeneratedTotal.WithLabelValues("prometheus", "ok").Inc()
	return png, nil
}

// parseExprFromGeneratorURL 从告警里的 Graph URL 取 g0.expr；Go 的 url.Query().Get 已做一次 URL decode，直接返回。
func parseExprFromGeneratorURL(generatorURL string) (string, error) {
	u, err := url.Parse(generatorURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	expr := q.Get("g0.expr")
	return strings.TrimSpace(expr), nil
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
		return nil, fmt.Errorf("无数据")
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
