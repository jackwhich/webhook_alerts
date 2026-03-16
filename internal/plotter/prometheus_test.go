package plotter

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestRenderLineChartOutput 生成示例趋势图并写入文件，便于查看出图效果。
// 运行: go test -v -run TestRenderLineChartOutput ./internal/plotter/
// 图片输出: testdata/alert-chart-sample.png
func TestRenderLineChartOutput(t *testing.T) {
	title := "nginx uri POST请求大于3000"
	xLabels := []string{
		"2026-03-17 00:41:00", "2026-03-17 00:42:00", "2026-03-17 00:43:00",
		"2026-03-17 00:44:00", "2026-03-17 00:45:00", "2026-03-17 00:48:24",
	}
	// 模拟 3 条 series，数值略不同
	seriesValues := [][]float64{
		{1200, 3500, 4200, 3800, 5100, 4800},
		{800, 1200, 3100, 2900, 3200, 3500},
		{100, 200, 150, 400, 600, 500},
	}
	legendLabels := []string{
		"server_name=gateway.xx.com,uri=/api/asset/v1",
		"server_name=admin.ebpay.net,uri=/api/otcOrder/appeal/list",
		"server_name=admin.ebpay.net,uri=/api/auto/lock/audit",
	}

	png, err := renderLineChart(title, xLabels, seriesValues, legendLabels)
	if err != nil {
		t.Fatal(err)
	}

	outDir := "testdata"
	outPath := filepath.Join(outDir, "alert-chart-sample.png")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outPath, png, 0644); err != nil {
		t.Fatal(err)
	}

	t.Logf("图片已写入: %s（可打开查看出图效果）", outPath)
}

// TestRenderLineChartDenseLegend 生成高密度图例样例，验证右侧自适应布局。
func TestRenderLineChartDenseLegend(t *testing.T) {
	title := "nginx 高频告警压测样例"
	xLabels := []string{
		"2026-03-17 00:41:00", "2026-03-17 00:42:00", "2026-03-17 00:43:00",
		"2026-03-17 00:44:00", "2026-03-17 00:45:00", "2026-03-17 00:48:24",
	}
	seriesValues := make([][]float64, 12)
	legendLabels := make([]string, 12)
	for i := 0; i < 12; i++ {
		base := float64(800 + i*300)
		seriesValues[i] = []float64{
			base + 100, base + 500, base + 800, base + 600, base + 900, base + 700,
		}
		legendLabels[i] = "server_name=very-long-domain-" + strconv.Itoa(i) +
			".example.com,uri=/api/v1/very/long/path/for/layout/adaptive/" + strconv.Itoa(i)
	}

	png, err := renderLineChart(title, xLabels, seriesValues, legendLabels)
	if err != nil {
		t.Fatal(err)
	}

	outDir := "testdata"
	outPath := filepath.Join(outDir, "alert-chart-dense-legend.png")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outPath, png, 0644); err != nil {
		t.Fatal(err)
	}
	t.Logf("高密度图例图片已写入: %s", outPath)
}
