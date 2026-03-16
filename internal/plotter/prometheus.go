// Package plotter 基于 Prometheus/VM query_range 与 go-charts 生成趋势图。
package plotter

import (
	"embed"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/golang/freetype/truetype"
	"github.com/jackwhich/webhook_alerts/internal/metrics"
	charts "github.com/vicanso/go-charts/v2"
)

//go:embed fonts/arial-unicode.ttf
var wqyFont embed.FS

const pngSignature = "\x89PNG\r\n\x1a\n"
const debugLogPath = "/Users/masheilapincamamaril/Desktop/HashiCorp_Vault/.cursor/debug-b01777.log"
const debugSessionID = "b01777"

// themeAlertDark 与 Python Plotly 出图风格一致：深色背景、白字、醒目折线色。
const themeAlertDark = "alert-dark"

// 与 alert-router-py Plotly 导出一致：scale=2 时 PNG 为 2800×1400，背景 #0a0a0f。
const (
	chartScale       = 2
	chartBaseWidth   = 1400
	chartBaseHeight  = 700
	chartBgHex       = "0a0a0f"
)

func init() {
	// 加载中文字体
	fontData, err := wqyFont.ReadFile("fonts/arial-unicode.ttf")
	if err == nil {
		errInstall := charts.InstallFont("arial-unicode", fontData)
		if errInstall != nil {
			fmt.Printf("加载字体失败: %v\n", errInstall)
		} else {
			// 将注册好的字体直接设为全局默认，防止 go-charts 内部回退为只支持英文的默认字体
			if f, getErr := charts.GetFont("arial-unicode"); getErr == nil {
				charts.SetDefaultFont(f)
			}
		}
	} else {
		fmt.Printf("读取嵌入字体失败: %v\n", err)
	}

	// 注册霓虹深色主题：高对比、鲜亮线条、白色文本；轴分割线与背景同色以隐藏库内可能绘制的绿/白间虚线（与 Python Plotly #0a0a0f 一致）
	charts.AddTheme(themeAlertDark, charts.ThemeOption{
		IsDarkMode: true,
		AxisStrokeColor:    hexColor("DCE6FF"),
		AxisSplitLineColor: hexColor(chartBgHex),
		BackgroundColor:    hexColor(chartBgHex),
		TextColor:          hexColor("F1F5FF"),
		SeriesColors: []charts.Color{
			hexColor("FF4D6D"), hexColor("00E5FF"), hexColor("7DFF72"), hexColor("FFB703"),
			hexColor("C77DFF"), hexColor("00F5D4"), hexColor("FF8FA3"), hexColor("4CC9F0"),
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

type legendLayoutResult struct {
	fontSize       float64
	lineHeight     int
	itemGap        int
	totalHeight    int
	wrappedLineNum int
	items          [][]string
}

func isPercentMetric(title string, legendLabels []string) bool {
	t := strings.ToLower(title)
	if strings.Contains(t, "cpu") ||
		strings.Contains(t, "mem") ||
		strings.Contains(t, "memory") ||
		strings.Contains(title, "内存") ||
		strings.Contains(title, "使用率") ||
		strings.Contains(t, "percent") {
		return true
	}
	for _, lbl := range legendLabels {
		l := strings.ToLower(lbl)
		if strings.Contains(l, "cpu") ||
			strings.Contains(l, "mem") ||
			strings.Contains(l, "memory") ||
			strings.Contains(lbl, "内存") ||
			strings.Contains(lbl, "使用率") ||
			strings.Contains(l, "percent") {
			return true
		}
	}
	return false
}

func formatYAxisValue(v float64, percent bool) string {
	if percent {
		return fmt.Sprintf("%.1f%%", v)
	}
	abs := v
	if abs < 0 {
		abs = -abs
	}
	if abs >= 1000 {
		return fmt.Sprintf("%.2f K", v/1000)
	}
	if v == float64(int64(v)) {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.1f", v)
}

func maxSeriesValue(seriesValues [][]float64) float64 {
	maxV := 0.0
	for _, row := range seriesValues {
		for _, v := range row {
			if v > maxV {
				maxV = v
			}
		}
	}
	return maxV
}

func drawManualYAxisLabels(
	p *charts.Painter,
	font *truetype.Font,
	left, top, bottom int,
	isPercent bool,
	axisMax float64,
	scale int,
) {
	p.SetTextStyle(charts.Style{
		FontSize:  float64(12 * scale),
		FontColor: hexColor("FFFFFF"),
		Font:      font,
	})
	offsetX := 8 * scale
	if isPercent {
		// 0~100%，每 20% 一档
		for i := 0; i <= 5; i++ {
			v := float64(i * 20)
			y := bottom - int((v/100.0)*float64(bottom-top))
			txt := fmt.Sprintf("%.0f%%", v)
			box := p.MeasureText(txt)
			p.Text(txt, left-box.Width()-offsetX, y+box.Height()/2-1)
		}
		return
	}
	// 非百分比：按 1.00 K 为单位显示到 axisMax-1K，顶部留一档缓冲（贴近图二效果）
	step := 1000.0
	if axisMax < 2000 {
		axisMax = 2000
	}
	for v := 0.0; v <= axisMax-step; v += step {
		y := bottom - int((v/axisMax)*float64(bottom-top))
		txt := "0"
		if v > 0 {
			txt = fmt.Sprintf("%.2f K", v/1000.0)
		}
		box := p.MeasureText(txt)
		p.Text(txt, left-box.Width()-offsetX, y+box.Height()/2-1)
	}
}

func normalizeXAxisLabels(raw []string) ([]string, string) {
	if len(raw) == 0 {
		return raw, ""
	}
	display := make([]string, len(raw))
	centerText := raw[len(raw)-1]
	layouts := []string{
		"2006-01-02 15:04:05",
		time.RFC3339,
	}
	foundFull := false
	for i, v := range raw {
		v = strings.TrimSpace(v)
		short := v
		for _, l := range layouts {
			if ts, err := time.Parse(l, v); err == nil {
				short = ts.Format("15:04:05")
				if i == len(raw)-1 {
					centerText = ts.Format("2006-01-02 15:04:05")
				}
				foundFull = true
				break
			}
		}
		display[i] = short
	}
	if !foundFull {
		centerText = raw[len(raw)-1]
	}
	return display, centerText
}

func wrapTextByWidth(p *charts.Painter, text string, maxWidth int) []string {
	if text == "" {
		return []string{""}
	}
	if maxWidth <= 0 {
		return []string{text}
	}
	runes := []rune(text)
	var lines []string
	start := 0
	for start < len(runes) {
		end := start + 1
		lastFit := start + 1
		for end <= len(runes) {
			seg := string(runes[start:end])
			if p.MeasureText(seg).Width() <= maxWidth {
				lastFit = end
				end++
				continue
			}
			break
		}
		if lastFit == start {
			lastFit = start + 1
		}
		lines = append(lines, string(runes[start:lastFit]))
		start = lastFit
	}
	return lines
}

func layoutLegendAdaptive(p *charts.Painter, legendTexts []string, availTextWidth, availHeight, scale int) legendLayoutResult {
	minLineHeight := 12 * scale
	minItemGap := 6 * scale
	// 默认值，防止极端场景除零
	best := legendLayoutResult{
		fontSize:    float64(8 * scale),
		lineHeight:  minLineHeight,
		itemGap:     minItemGap,
		totalHeight: 0,
		items:       make([][]string, 0, len(legendTexts)),
	}
	for font := float64(11 * scale); font >= float64(8*scale); font -= float64(scale) {
		lineHeight := int(font*1.45 + 0.5)
		if lineHeight < minLineHeight {
			lineHeight = minLineHeight
		}
		itemGap := int(font*0.9 + 0.5)
		if itemGap < minItemGap {
			itemGap = minItemGap
		}
		p.SetTextStyle(charts.Style{FontSize: font})
		totalHeight := 0
		totalLines := 0
		items := make([][]string, 0, len(legendTexts))
		for _, raw := range legendTexts {
			parts := strings.Split(raw, "\n")
			itemLines := make([]string, 0, len(parts))
			for _, part := range parts {
				itemLines = append(itemLines, wrapTextByWidth(p, part, availTextWidth)...)
			}
			if len(itemLines) == 0 {
				itemLines = []string{""}
			}
			items = append(items, itemLines)
			totalLines += len(itemLines)
			totalHeight += len(itemLines)*lineHeight + itemGap
		}
		if len(items) > 0 {
			totalHeight -= itemGap
		}
		best = legendLayoutResult{
			fontSize:       font,
			lineHeight:     lineHeight,
			itemGap:        itemGap,
			totalHeight:    totalHeight,
			wrappedLineNum: totalLines,
			items:          items,
		}
		if totalHeight <= availHeight {
			return best
		}
	}
	return best
}

func drawRightLegendAdaptive(
	p *charts.Painter,
	theme charts.ColorPalette,
	font *truetype.Font,
	legendTexts []string,
	startX, startY, availWidth, availHeight, scale int,
) legendLayoutResult {
	if len(legendTexts) == 0 {
		return legendLayoutResult{}
	}
	iconWidth := 22 * scale
	iconGap := 10 * scale
	textPadding := 10 * scale
	textX := startX + iconWidth + iconGap
	textWidth := availWidth - iconWidth - iconGap - textPadding
	layout := layoutLegendAdaptive(p, legendTexts, textWidth, availHeight, scale)
	p.SetTextStyle(charts.Style{
		FontSize:  layout.fontSize,
		FontColor: hexColor("EAF2FF"),
		Font:      font,
	})
	y := startY
	for i, lines := range layout.items {
		color := theme.GetSeriesColor(i)
		iconY := y + layout.lineHeight/2
		p.SetDrawingStyle(charts.Style{
			StrokeColor: color,
			FillColor:   color.WithAlpha(220),
			StrokeWidth: 3.2 * float64(scale),
		})
		p.LineStroke([]charts.Point{
			{X: startX, Y: iconY},
			{X: startX + iconWidth, Y: iconY},
		})
		p.Dots([]charts.Point{{X: startX + iconWidth/2, Y: iconY}})
		lineY := y + layout.lineHeight
		for _, line := range lines {
			p.Text(line, textX, lineY)
			lineY += layout.lineHeight
		}
		y += len(lines)*layout.lineHeight + layout.itemGap
	}
	return layout
}

// maskLibraryAxisSplitLines 用背景色实线覆盖库可能画出的 Y 轴分割线，移除绿/白间残留虚线
func maskLibraryAxisSplitLines(p *charts.Painter, left, top, right, bottom int) {
	bg := hexColor(chartBgHex)
	p.SetDrawingStyle(charts.Style{
		StrokeColor:     bg,
		StrokeWidth:     float64(2 * chartScale),
		StrokeDashArray: nil, // 实线
	})
	for i := 1; i <= 5; i++ {
		y := top + (bottom-top)*i/6
		p.LineStroke([]charts.Point{{X: left, Y: y}, {X: right, Y: y}})
	}
}

func drawDashedGrid(p *charts.Painter, left, top, right, bottom, scale int) {
	// 仅绘制纵向虚线网格，不绘制横向虚线，避免与告警值或视觉上的“多余横线”混淆
	gridColor := charts.Color{R: 220, G: 230, B: 255, A: 55}
	p.SetDrawingStyle(charts.Style{
		StrokeColor:     gridColor,
		StrokeWidth:     float64(1 * scale),
		StrokeDashArray: []float64{3 * float64(scale), 6 * float64(scale)},
	})
	vCount := 8
	for i := 0; i <= vCount; i++ {
		x := left + (right-left)*i/vCount
		p.LineStroke([]charts.Point{{X: x, Y: top}, {X: x, Y: bottom}})
	}
}

func drawPlotBorder(p *charts.Painter, left, top, right, bottom, scale int) {
	// 外框保持较弱，避免抢主轴视觉
	borderColor := charts.Color{R: 210, G: 220, B: 240, A: 95}
	p.SetDrawingStyle(charts.Style{
		StrokeColor: borderColor,
		StrokeWidth: 1.0 * float64(scale),
	})
	p.LineStroke([]charts.Point{{X: left, Y: top}, {X: right, Y: top}})
	p.LineStroke([]charts.Point{{X: right, Y: top}, {X: right, Y: bottom}})
	p.LineStroke([]charts.Point{{X: left, Y: bottom}, {X: right, Y: bottom}})
	p.LineStroke([]charts.Point{{X: left, Y: top}, {X: left, Y: bottom}})

	// 主轴做同位叠加（辉光 + 主白线），保持你标注的干净 L 形白线
	p.SetDrawingStyle(charts.Style{
		StrokeColor: charts.Color{R: 225, G: 236, B: 255, A: 110},
		StrokeWidth: 5.0 * float64(scale),
	})
	p.LineStroke([]charts.Point{{X: left, Y: top}, {X: left, Y: bottom}})
	p.LineStroke([]charts.Point{{X: left, Y: bottom}, {X: right, Y: bottom}})

	p.SetDrawingStyle(charts.Style{
		StrokeColor: charts.Color{R: 255, G: 255, B: 255, A: 255},
		StrokeWidth: 2.6 * float64(scale),
	})
	p.LineStroke([]charts.Point{{X: left, Y: bottom}, {X: right, Y: bottom}})
	p.LineStroke([]charts.Point{{X: left, Y: top}, {X: left, Y: bottom}})
}

func drawAxisTicks(p *charts.Painter, left, top, right, bottom, xCount, scale int) {
	tickColor := charts.Color{R: 255, G: 255, B: 255, A: 255}
	p.SetDrawingStyle(charts.Style{
		StrokeColor: tickColor,
		StrokeWidth: 1.8 * float64(scale),
	})
	// Y ticks: 6 段（7个刻度点）
	yDiv := 6
	tickLen := 11 * scale
	for i := 0; i <= yDiv; i++ {
		y := top + (bottom-top)*i/yDiv
		p.LineStroke([]charts.Point{{X: left - tickLen, Y: y}, {X: left, Y: y}})
	}
	// X 轴刻度由库内置渲染，避免与手工 tick 叠加产生“双刻度线”
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
	xAxisLabels, xCenterText := normalizeXAxisLabels(xLabels)
	// 保证 x 轴标签与每条 series 长度一致，避免 go-charts 渲染越界
	nPoints := len(xLabels)
	for _, row := range seriesValues {
		if len(row) < nPoints {
			nPoints = len(row)
		}
	}
	if nPoints == 0 {
		return nil, fmt.Errorf("无数据")
	}
	xAxisLabels = xAxisLabels[:nPoints]
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
	// 线性图（折线图）：与 Python Plotly 一致 2800×1400（scale=2），深色主题，图例右上角。
	chartWidth := chartBaseWidth * chartScale
	chartHeight := chartBaseHeight * chartScale
	padLeft := 82 * chartScale
	padTop := 80 * chartScale
	padRight := 300 * chartScale
	padBottom := 88 * chartScale
	isPct := isPercentMetric(title, legendLabels)
	maxVal := maxSeriesValue(seriesValues)
	axisMin := 0.0
	axisMax := 100.0
	if !isPct {
		axisMax = math.Ceil(maxVal/1000.0) * 1000.0
		if axisMax < 6000 {
			axisMax = 6000 // 保证可见 1.00K~5.00K
		}
		if maxVal >= axisMax {
			axisMax += 1000
		}
	}
	// #region agent log
	appendDebugLog("post-fix", "H1", "prometheus.go:renderLineChart", "chart and padded drawing area", map[string]any{
		"chartWidth": chartWidth, "chartHeight": chartHeight,
		"padLeft": padLeft, "padTop": padTop, "padRight": padRight, "padBottom": padBottom,
		"drawingWidth": chartWidth - padLeft - padRight,
		"isPercentMetric": isPct,
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
		charts.XAxisOptionFunc(charts.XAxisOption{
			Data:        xAxisLabels,
			FontColor:   hexColor("FFFFFF"),
			StrokeColor: hexColor("FFFFFF"),
			FontSize:    12 * chartScale,
			BoundaryGap: charts.FalseFlag(), // 禁用留白，确保刻度精准对齐垂直线
			TextRotation: -math.Pi / 4,      // 与 Python/matplotlib 一致：45° 倾斜
			LabelOffset: charts.Box{
				Top:  14 * chartScale, // 时间刻度贴近下轴外侧（与参考图一致）
				Left: -2 * chartScale,
			},
		}),
		charts.YAxisOptionFunc(charts.YAxisOption{
			FontColor:     hexColor("FFFFFF"),
			Color:         hexColor("FFFFFF"),
			FontSize:      12 * chartScale,
			SplitLineShow: charts.FalseFlag(),
			Min:           &axisMin,
			Max:           &axisMax,
		}),
		func(opt *charts.ChartOption) {
			opt.FontFamily = "arial-unicode" // 全局应用中文字体
			// 标题我们将在后面手动绘制，确保绝对物理居中
			opt.ValueFormatter = func(v float64) string {
				// Y 轴标签改为手绘，确保位置与图二一致
				return ""
			}
		},
		charts.ThemeOptionFunc(themeAlertDark),
		func(opt *charts.ChartOption) {
			opt.FillArea = false // 线性图：仅折线，不填充区域
			opt.LineStrokeWidth = 3.0 * float64(chartScale)
			f := false
			opt.SymbolShow = &f
			opt.Legend.Show = &f // 彻底关闭自带图例，因为我们要自己画在右侧
		},
		// 防御性再清一次 MarkLine/MarkPoint，避免库内部某处给 Series 填默认值导致仍画虚线
		func(opt *charts.ChartOption) {
			for i := range opt.SeriesList {
				opt.SeriesList[i].MarkLine = charts.SeriesMarkLine{}
				opt.SeriesList[i].MarkPoint = charts.SeriesMarkPoint{}
			}
		},
	}
	// 自建 SeriesList 并清空 MarkLine/MarkPoint，再交给 Render，避免库绘制 max/min/average 虚线
	seriesList := charts.NewSeriesListDataFromValues(seriesValues, charts.ChartTypeLine)
	for i := range seriesList {
		seriesList[i].MarkLine = charts.SeriesMarkLine{}
		seriesList[i].MarkPoint = charts.SeriesMarkPoint{}
	}
	painter, err := charts.Render(charts.ChartOption{SeriesList: seriesList}, opts...)
	if err != nil {
		return nil, err
	}
	// 用背景色覆盖库可能画出的 Y 轴分割线，再叠加虚线网格
	plotLeft := padLeft
	plotTop := padTop
	plotRight := chartWidth - padRight
	plotBottom := chartHeight - padBottom
	maskLibraryAxisSplitLines(painter, plotLeft, plotTop, plotRight, plotBottom)
	drawDashedGrid(painter, plotLeft, plotTop, plotRight, plotBottom, chartScale)
	drawPlotBorder(painter, plotLeft, plotTop, plotRight, plotBottom, chartScale)
	drawAxisTicks(painter, plotLeft, plotTop, plotRight, plotBottom, len(xAxisLabels), chartScale)
	wqyFontObj, _ := charts.GetFont("arial-unicode")
	drawManualYAxisLabels(painter, wqyFontObj, plotLeft, plotTop, plotBottom, isPct, axisMax, chartScale)

	// === 手动在顶部物理居中绘制标题 ===
	painter.SetTextStyle(charts.Style{
		FontSize:  25 * chartScale,
		FontColor: hexColor("F3F7FF"),
		Font:      wqyFontObj,
	})
	titleBox := painter.MeasureText(title)
	// 计算物理居中: (整图宽度 - 文字宽度) / 2
	titleX := (chartWidth - titleBox.Width()) / 2
	titleY := 40 * chartScale // 标题与主图更贴合
	painter.Text(title, titleX, titleY)
	// 底部居中完整时间（与参考图一致）
	painter.SetTextStyle(charts.Style{
		FontSize:  12 * chartScale,
		FontColor: hexColor("EAF2FF"),
		Font:      wqyFontObj,
	})
	centerBox := painter.MeasureText(xCenterText)
	centerX := (chartWidth - centerBox.Width()) / 2
	centerY := chartHeight - 16*chartScale // 完整时间上移，避免贴到底边
	painter.Text(xCenterText, centerX, centerY)

	// === 右侧图例自适应绘制：先换行，再按高度缩放字号，确保全部显示 ===
	legendX := chartWidth - padRight + 12*chartScale
	legendY := padTop - 8*chartScale
	legendWidth := padRight - 16*chartScale
	legendHeight := chartHeight - padBottom - legendY
	theme := charts.NewTheme(themeAlertDark)
	legendLayout := drawRightLegendAdaptive(
		painter, theme, wqyFontObj, legendRight,
		legendX, legendY, legendWidth, legendHeight,
		chartScale,
	)

	// #region agent log
	pngBytes, _ := painter.Bytes()
	appendDebugLog("post-fix", "H3", "prometheus.go:renderLineChart", "line chart rendered with custom manual legend", map[string]any{
		"seriesCount": len(seriesValues), "pointsPerSeries": nPoints, "pngBytes": len(pngBytes),
		"legend_item_count": len(legendRight),
		"legend_font_size_final": legendLayout.fontSize,
		"legend_total_height": legendLayout.totalHeight,
		"legend_wrapped_line_count": legendLayout.wrappedLineNum,
	})
	// #endregion
	return pngBytes, nil
}
