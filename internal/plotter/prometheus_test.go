package plotter

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRenderLineChartOutput 生成示例趋势图并写入文件，便于查看出图效果。
// 运行: go test -v -run TestRenderLineChartOutput ./internal/plotter/
// 图片输出: testdata/alert-chart-sample.png
func TestRenderLineChartOutput(t *testing.T) {
	title := "nginx uri POST请求大于3000"
	xLabels := []string{
		"00:00:00", "00:05:00", "00:10:00", "00:15:00", "00:20:00", "00:25:00",
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
