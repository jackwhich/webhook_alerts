// Package plotter 基于 Prometheus/VM query_range 与 go-charts 生成趋势图。
package plotter

import (
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackwhich/webhook_alerts/internal/metrics"
	charts "github.com/vicanso/go-charts/v2"
)

//go:embed fonts/wqy-microhei.ttc
var wqyFont embed.FS

const pngSignature = "\x89PNG\r\n\x1a\n"
const debugLogPath = "/Users/masheilapincamamaril/Desktop/HashiCorp_Vault/.cursor/debug-b01777.log"
const debugSessionID = "b01777"

// themeAlertDark 与 Python Plotly 出图风格一致：深色背景、白字、醒目折线色。
const themeAlertDark = "alert-dark"

func init() {
	// 加载中文字体
	if fontData, err := wqyFont.ReadFile("fonts/wqy-microhei.ttc"); err == nil {
		charts.InstallFont("wqy-microhei", fontData)
	}

	// 注册深色主题：背景 #0a0a0f、白色文字与坐标轴、与 Python 一致的线条配色
	charts.AddTheme(themeAlertDark, charts.ThemeOption{
		IsDarkMode: true,
		AxisStrokeColor:    hexColor("ffffff"),
		AxisSplitLineColor: hexColor("404040"),
		BackgroundColor:    hexColor("0a0a0f"),
		TextColor:          hexColor("ffffff"),
		SeriesColors: []charts.Color{
			hexColor("FF6B6B"), hexColor("4ECDC4"), hexColor("45B7D1"), hexColor("FFA07A"),
			hexColor("98D8C8"), hexColor("F7DC6F"), hexColor("BB8FCE"), hexColor("85C1E2"),
		},
	})
}

func hexColor(hex string) charts.Color {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return charts.Color{R: 255, G: 255, B: 255, A: 255}
	}
	r, _ := strconv.ParseUint(hex[0:2], 16, 8)
	g, _ := strconv.ParseUint(hex[2:4], 16, 8)
	b, _ := strconv.ParseUint(hex[4:6], 16, 8)
	return charts.Color{R: uint8(r), G: uint8(g), B: uint8(b), A: 255}
}

func appendDebugLog(runID, hypothesisID, location, message string, data map[string]any) {
	payload := map[string]any{
		"sessionId":    debugSessionID,
		"runId":        runID,
		"hypothesisId": hypothesisID,
		"location":     location,
		"message":      message,
		"data":         data,
		"timestamp":    time.Now().UnixMilli(),
	}
	line, _ := json.Marshal(payload)
	f, err := os.OpenFile(debugLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	_, _ = f.Write(append(line, '\n'))
	_ = f.Close()
}

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
// 步骤：1) 从告警 Graph URL 取 g0.expr（已 URL decode）；2) 若无则从 annotations 取 expr/query/__expr__；3) 调 VM/Prometheus API 时优先用 config 的 prometheus_url（与 Python 一致，内网可达），否则才用 generatorURL 解析。labels 收窄查询；logExpr 可选，用于打日志；logQueryRangeResult 可选，用于打 query_range 请求/返回日志（resultCount=-1 表示即将请求）。
func (p *PrometheusPlotter) Generate(generatorURL, alertname string, labels map[string]string, annotations map[string]string, logExpr func(expr string), logQueryRangeResult func(apiURL, expr string, resultCount int, status string)) ([]byte, error) {
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
	// query_range 参数：query=PromQL（原始字符串，无多余转义）, start/end=Unix 秒, step=采样间隔
	params := url.Values{}
	params.Set("query", expr)
	params.Set("start", fmt.Sprintf("%d", startTime.Unix()))
	params.Set("end", fmt.Sprintf("%d", now.Unix()))
	params.Set("step", step)

	if logQueryRangeResult != nil {
		logQueryRangeResult(apiURL, expr, -1, "request")
	}
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
		if logQueryRangeResult != nil {
			logQueryRangeResult(apiURL, expr, 0, "error: "+err.Error())
		}
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		metrics.PrometheusRequestsTotal.WithLabelValues("error").Inc()
		errMsg := fmt.Sprintf("status %d", resp.StatusCode)
		if logQueryRangeResult != nil {
			logQueryRangeResult(apiURL, expr, 0, errMsg)
		}
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
		if logQueryRangeResult != nil {
			logQueryRangeResult(apiURL, expr, 0, "decode_error: "+decErr.Error())
		}
		return nil, decErr
	}
	if logQueryRangeResult != nil {
		logQueryRangeResult(apiURL, expr, len(payload.Data.Result), payload.Status)
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
		xLabels = append(xLabels, time.Unix(int64(t), 0).Format("15:04:05"))
	}
	// 使用第一条 series 的时间作为 X 轴。Prometheus/VM query_range 返回的 value 为 JSON 字符串（如 "123.45"），需同时支持 float64 与 string。
	for _, s := range result {
		if len(s.Values) == 0 {
			continue
		}
		var ys []float64
		for _, pair := range s.Values {
			if len(pair) < 2 {
				continue
			}
			if _, ok := pair[0].(float64); !ok {
				continue
			}
			var v float64
			switch val := pair[1].(type) {
			case float64:
				v = val
			case string:
				var parseErr error
				v, parseErr = strconv.ParseFloat(val, 64)
				if parseErr != nil {
					continue
				}
			default:
				continue
			}
			ys = append(ys, v)
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
					xLabels = append(xLabels, time.Unix(int64(t), 0).Format("15:04:05"))
				}
			}
		}
	}
	return xLabels, values, legends
}

// maxLegendRunes 图例最大字符数（超长截断），与 Python _build_series_label 的 90 字符思路一致，避免被裁剪。
const maxLegendRunes = 48

func buildLegend(metric map[string]string) string {
	// 优先展示 uri、server_name（与 nginx 等告警最相关），其余按 key 排序
	order := []string{"uri", "server_name", "status", "instance", "job"}
	seen := make(map[string]bool)
	var parts []string
	for _, k := range order {
		if v, ok := metric[k]; ok && k != "__name__" {
			parts = append(parts, k+"="+v)
			seen[k] = true
		}
	}
	for k, v := range metric {
		if !seen[k] && k != "__name__" {
			parts = append(parts, k+"="+v)
		}
	}
	if len(parts) == 0 {
		return metric["__name__"]
	}
	s := strings.Join(parts, ",")
	return truncateLegend(s)
}

func truncateLegend(s string) string {
	r := []rune(s)
	if len(r) <= maxLegendRunes {
		return s
	}
	return string(r[:maxLegendRunes-3]) + "..."
}

// legendOptionRightLegend 返回图例选项：靠右、垂直排列，与 Python 一致放在右上角。
func legendOptionRightLegend(labels []string) charts.OptionFunc {
	return func(opt *charts.ChartOption) {
		opt.Legend = charts.LegendOption{
			Data:   labels,
			Left:   charts.PositionRight,
			Top:    "0",
			Orient: charts.OrientVertical,
		}
	}
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
	// 保证 xLabels 与每条 series 长度一致，避免 go-charts 渲染时 xValues[i] 越界（index out of range [2] with length 2）
	nPoints := len(xLabels)
	for _, row := range seriesValues {
		if len(row) < nPoints {
			nPoints = len(row)
		}
	}
	if nPoints == 0 {
		return nil, fmt.Errorf("无数据")
	}
	xLabels = xLabels[:nPoints]
	for i := range seriesValues {
		seriesValues[i] = seriesValues[i][:nPoints]
	}
	// 图例文案对齐 Python：每条为“标签 + 换行 + 告警值”。
	legendRight := make([]string, len(legendLabels))
	for i := range legendLabels {
		if i < len(seriesValues) && len(seriesValues[i]) > 0 {
			last := seriesValues[i][len(seriesValues[i])-1]
			legendRight[i] = legendLabels[i] + "\n告警值 " + fmt.Sprintf("%.1f", last)
		} else {
			legendRight[i] = legendLabels[i]
		}
	}
	// 线性图（折线图）：与 Python 布局一致 1400×700，深色主题，图例右上角。
	const chartWidth = 1400
	const chartHeight = 700
	const padLeft = 60
	const padTop = 80
	// 右侧留大边距（约 300px）给末端标签
	const padRight = 360
	const padBottom = 60
	// #region agent log
	appendDebugLog("post-fix", "H1", "prometheus.go:renderLineChart", "chart and padded drawing area", map[string]any{
		"chartWidth": chartWidth, "chartHeight": chartHeight,
		"padLeft": padLeft, "padTop": padTop, "padRight": padRight, "padBottom": padBottom,
		"drawingWidth": chartWidth - padLeft - padRight,
	})
	// #endregion
	// #region agent log
	appendDebugLog("post-fix", "H2", "prometheus.go:renderLineChart", "right legend labels", map[string]any{
		"legendCount": len(legendRight), "legendFirst": func() string {
			if len(legendRight) == 0 {
				return ""
			}
			return legendRight[0]
		}(),
		"containsLineBreak": func() bool {
			if len(legendRight) == 0 {
				return false
			}
			return strings.Contains(legendRight[0], "\n")
		}(),
	})
	// #endregion
	opts := []charts.OptionFunc{
		charts.WidthOptionFunc(chartWidth),
		charts.HeightOptionFunc(chartHeight),
		charts.PaddingOptionFunc(charts.Box{
			Left: padLeft, Top: padTop, Right: padRight, Bottom: padBottom,
		}),
		func(opt *charts.ChartOption) {
			opt.FontFamily = "wqy-microhei" // 全局应用中文字体
			wqyFontObj, _ := charts.GetFont("wqy-microhei")
			opt.Title = charts.TitleOption{
				Text:      title,
				Left:      charts.PositionCenter,
				FontSize:  24,
				FontColor: hexColor("ffffff"),
				Font:      wqyFontObj,
			}
		},
		charts.XAxisDataOptionFunc(xLabels),
		charts.ThemeOptionFunc(themeAlertDark),
		func(opt *charts.ChartOption) {
			opt.FillArea = false       // 线性图：仅折线，不填充区域
			opt.LineStrokeWidth = 3.0
			f := false
			opt.SymbolShow = &f
			opt.Legend.Show = &f       // 彻底关闭自带图例，因为我们要自己画在右侧
		},
	}
	painter, err := charts.LineRender(seriesValues, opts...)
	if err != nil {
		return nil, err
	}

	// === 手动在右侧留白区绘制图例（解决自带图例强制挤压上方区域的问题） ===
	// 在绘制完毕后，painter.Width() 是 1400，padding 右侧是 360，
	// 所以我们能在 X = 1050 左右的地方开始写字，从图表上方往下排。
	legendX := chartWidth - padRight + 20
	legendY := padTop - 20
	lineSpacing := 25
	
	// 从 go-charts 里获取刚刚注册的字体
	wqyFontObj, _ := charts.GetFont("wqy-microhei")

	painter.SetTextStyle(charts.Style{
		FontSize:  11,
		FontColor: hexColor("ffffff"),
		Font:      wqyFontObj,
	})
	
	theme := charts.NewTheme(themeAlertDark)
	for i, text := range legendRight {
		// 画一个小圆点或者小横线代表颜色
		color := theme.GetSeriesColor(i)
		painter.SetDrawingStyle(charts.Style{
			StrokeColor: color,
			FillColor:   color,
			StrokeWidth: 3,
		})
		
		// 画一条短线和圆点
		lineStartX := legendX
		lineEndX := legendX + 20
		dotX := legendX + 10
		dotY := legendY - 4 // 略微上浮对齐文字中部
		
		// 曲线
		painter.LineStroke([]charts.Point{
			{X: lineStartX, Y: dotY},
			{X: lineEndX, Y: dotY},
		})
		// 曲线上的点
		painter.Dots([]charts.Point{{X: dotX, Y: dotY}})
		
		// 画两行文字（标签一行、告警值一行）
		parts := strings.Split(text, "\n")
		textX := lineEndX + 10
		currentY := legendY
		for _, part := range parts {
			painter.Text(part, textX, currentY)
			currentY += 16 // 行高
		}
		
		legendY += lineSpacing + 16 // 下一个图例项的 Y 起点
	}

	// #region agent log
	pngBytes, _ := painter.Bytes()
	appendDebugLog("post-fix", "H3", "prometheus.go:renderLineChart", "line chart rendered with custom manual legend", map[string]any{
		"seriesCount": len(seriesValues), "pointsPerSeries": nPoints, "pngBytes": len(pngBytes),
	})
	// #endregion
	return pngBytes, nil
}
