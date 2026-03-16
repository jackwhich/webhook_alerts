package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/jackwhich/webhook_alerts/internal/config"
	"github.com/jackwhich/webhook_alerts/internal/handler"
	"github.com/jackwhich/webhook_alerts/internal/logger"
	"github.com/jackwhich/webhook_alerts/internal/plotter"
	"github.com/jackwhich/webhook_alerts/internal/service"
	"github.com/jackwhich/webhook_alerts/internal/template"
	"gopkg.in/natefinch/lumberjack.v2"
)

func main() {
	cfgPath, err := config.ConfigPath()
	if err != nil {
		panic("配置文件路径错误: " + err.Error())
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		panic("加载配置失败: " + err.Error())
	}

	// 日志写入文件并轮转（log_dir/log_file）
	logDir := cfg.Logging.LogDir
	if err := os.MkdirAll(logDir, 0755); err != nil {
		panic("创建日志目录失败: " + err.Error())
	}
	logPath := filepath.Join(logDir, cfg.Logging.LogFile)
	maxSizeMB := cfg.Logging.MaxBytes / 1048576
	if maxSizeMB < 1 {
		maxSizeMB = 1
	}
	writer := &lumberjack.Logger{
		Filename:   logPath,
		MaxSize:    maxSizeMB,
		MaxBackups: cfg.Logging.BackupCount,
	}
	logObj := logger.New(writer, cfg.Logging.Level)
	enabledChannels := 0
	for _, ch := range cfg.Channels {
		if ch.Enabled {
			enabledChannels++
		}
	}
	logObj.C().Info().
		Str("config", cfgPath).
		Str("level", cfg.Logging.Level).
		Str("log_file", logPath).
		Int("channels", len(cfg.Channels)).
		Int("channels_enabled", enabledChannels).
		Int("routing_rules", len(cfg.Routing)).
		Msg("配置加载完成")

	// 端口仅从 config.yaml 的 server.port 读取，不在代码中写死
	addr := cfg.Server.Host + ":" + strconv.Itoa(cfg.Server.Port)
	// 模板目录：与 config 同目录下的 templates，否则使用当前目录的 templates
	template.TemplateDir = filepath.Join(filepath.Dir(cfgPath), "templates")
	if _, err := os.Stat(template.TemplateDir); err != nil {
		template.TemplateDir = "templates"
	}

	// Prometheus 趋势图绘图器
	var promPlotter *plotter.PrometheusPlotter
	if imgCfg, _ := cfg.Raw["prometheus_image"].(map[string]any); imgCfg != nil {
		promURL, _ := imgCfg["prometheus_url"].(string)
		lookback := 15
		if v, ok := imgCfg["lookback_minutes"].(int); ok && v > 0 {
			lookback = v
		}
		timeoutSec := 8
		if v, ok := imgCfg["timeout_seconds"].(int); ok && v > 0 {
			timeoutSec = v
		}
		maxSeries := 8
		if v, ok := imgCfg["max_series"].(int); ok && v > 0 {
			maxSeries = v
		}
		step, _ := imgCfg["step"].(string)
		if step == "" {
			step = "30s"
		}
		promPlotter = &plotter.PrometheusPlotter{
			BaseURL:   promURL,
			Lookback:  time.Duration(lookback) * time.Minute,
			Step:      step,
			Timeout:   time.Duration(timeoutSec) * time.Second,
			MaxSeries: maxSeries,
			HTTPClient: &http.Client{
				Timeout:   time.Duration(timeoutSec) * time.Second,
				Transport: &http.Transport{MaxIdleConnsPerHost: 10},
			},
		}
	}

	channelFilter := &service.ChannelFilter{Channels: cfg.Channels}
	imageSvc := &service.ImageService{Config: cfg, Filter: channelFilter, Plotter: promPlotter}
	alertSvc := &service.AlertService{
		Config:        cfg,
		ChannelFilter: channelFilter,
		ImageService:  imageSvc,
	}

	logObj.C().Info().Str("listen", addr).Msg("HTTP 服务启动")

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/webhook", handler.Webhook(alertSvc))
	mux.Handle("/metrics", handler.Metrics())
	h := logger.Middleware(logObj)(mux)

	srv := &http.Server{Addr: addr, Handler: h}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logObj.C().Fatal().Err(err).Msg("HTTP 服务异常退出")
		}
	}()

	// 优雅退出：监听 SIGTERM/SIGINT，关闭 HTTP 服务并等待请求处理完毕
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	<-quit
	logObj.C().Info().Msg("收到退出信号，正在优雅关闭...")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logObj.C().Warn().Err(err).Msg("Shutdown 超时或异常")
	}
	logObj.C().Info().Msg("服务已优雅退出")
}
