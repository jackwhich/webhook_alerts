package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config 根配置，与 YAML 结构兼容。
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Logging  LoggingConfig  `yaml:"logging"`
	Channels map[string]*Channel `yaml:"-"` // Load 时解析
	Routing  []RoutingRule  `yaml:"-"` // Load 时解析
	Raw      map[string]any `yaml:"-"` // 原始配置供后续使用
}

// RoutingRule 单条路由规则：匹配条件 -> 发送到渠道列表。
type RoutingRule struct {
	Match   map[string]string `yaml:"match"`
	SendTo  []string          `yaml:"send_to"`
	Default bool              `yaml:"default"`
}

// ServerConfig 服务监听配置，端口必须在 config.yaml 中配置。
type ServerConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

// LoggingConfig 日志配置（启动时必填）。
type LoggingConfig struct {
	LogDir      string `yaml:"log_dir"`
	LogFile     string `yaml:"log_file"`
	Level       string `yaml:"level"`
	MaxBytes    int    `yaml:"max_bytes"`
	BackupCount int    `yaml:"backup_count"`
}

// ConfigPath 返回配置文件路径：优先环境变量 CONFIG_FILE，否则当前目录 config.yaml。
func ConfigPath() (string, error) {
	if p := os.Getenv("CONFIG_FILE"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	// 默认：当前工作目录下的 config.yaml
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(wd, "config.yaml"), nil
}

// ValidateServer 校验 server 配置，端口必须在 config.yaml 中配置，不得在代码中写死。
func ValidateServer(c *ServerConfig) error {
	if c.Host == "" {
		return fmt.Errorf("config.yaml 中 server.host 不能为空")
	}
	if c.Port <= 0 {
		return fmt.Errorf("config.yaml 中必须在 server.port 配置监听端口，不得在代码中写死")
	}
	return nil
}

// ValidateLogging 校验 logging 必填项。
func ValidateLogging(c *LoggingConfig) error {
	if c.LogDir == "" || c.LogFile == "" || c.Level == "" {
		return fmt.Errorf("logging 必须配置 log_dir、log_file、level")
	}
	if c.MaxBytes <= 0 {
		return fmt.Errorf("logging.max_bytes 必须为正数")
	}
	if c.BackupCount < 0 {
		return fmt.Errorf("logging.backup_count 不能为负数")
	}
	return nil
}

// normalizeProxyURL 将 socks5:// 转为 socks5h://，由代理端解析 DNS。
func normalizeProxyURL(url string) string {
	if url != "" && strings.HasPrefix(url, "socks5://") && !strings.HasPrefix(url, "socks5h://") {
		return "socks5h://" + url[len("socks5://"):]
	}
	return url
}

// Load 从路径读取配置，填充 Server、Logging、Channels、Routing，并做校验。
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置: %w", err)
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("解析配置: %w", err)
	}
	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("解码配置: %w", err)
	}
	cfg.Raw = raw
	if err := ValidateLogging(&cfg.Logging); err != nil {
		return nil, fmt.Errorf("配置文件: %w", err)
	}
	if err := ValidateServer(&cfg.Server); err != nil {
		return nil, fmt.Errorf("配置文件: %w", err)
	}
	// 解析渠道
	channelsRaw, ok := raw["channels"].(map[string]any)
	if !ok || len(channelsRaw) == 0 {
		return nil, fmt.Errorf("config.yaml 中必须配置 channels 节点")
	}
	globalProxy, _ := raw["proxy"]
	globalProxyEnabled := true
	if v, ok := raw["proxy_enabled"].(bool); ok {
		globalProxyEnabled = v
	}
	cfg.Channels = make(map[string]*Channel)
	for name, v := range channelsRaw {
		chMap, ok := v.(map[string]any)
		if !ok {
			continue
		}
		ch := channelFromMap(name, chMap, globalProxy, globalProxyEnabled)
		cfg.Channels[name] = ch
	}
	// 解析路由规则
	if routingSlice, ok := raw["routing"].([]any); ok {
		for _, r := range routingSlice {
			ruleMap, ok := r.(map[string]any)
			if !ok {
				continue
			}
			rule := routingRuleFromMap(ruleMap)
			cfg.Routing = append(cfg.Routing, rule)
		}
	}
	return cfg, nil
}

func channelFromMap(name string, m map[string]any, globalProxy any, globalProxyEnabled bool) *Channel {
	enabled := true
	if v, ok := m["enabled"].(bool); ok {
		enabled = v
	}
	proxyEnabled := globalProxyEnabled
	if v, ok := m["proxy_enabled"].(bool); ok {
		proxyEnabled = v
	}
	sendResolved := true
	if v, ok := m["send_resolved"].(bool); ok {
		sendResolved = v
	}
	imageEnabled := false
	if v, ok := m["image_enabled"].(bool); ok {
		imageEnabled = v
	}
	proxy := resolveProxy(m["proxy"], globalProxy, proxyEnabled)
	getStr := func(key string) string {
		if v, ok := m[key].(string); ok {
			return v
		}
		return ""
	}
	return &Channel{
		Name:         name,
		Type:         getStr("type"),
		Enabled:      enabled,
		WebhookURL:   getStr("webhook_url"),
		Template:     getStr("template"),
		BotToken:     getStr("bot_token"),
		ChatID:       getStr("chat_id"),
		Proxy:        proxy,
		ProxyEnabled: proxyEnabled,
		SendResolved: sendResolved,
		ImageEnabled: imageEnabled,
	}
}

func resolveProxy(chProxy, globalProxy any, proxyEnabled bool) map[string]string {
	if !proxyEnabled {
		return nil
	}
	p := chProxy
	if p == nil {
		p = globalProxy
	}
	if p == nil {
		return nil
	}
	if s, ok := p.(string); ok {
		u := normalizeProxyURL(s)
		if u == "" {
			return nil
		}
		return map[string]string{"http": u, "https": u}
	}
	if m, ok := p.(map[string]any); ok {
		out := make(map[string]string)
		for k, v := range m {
			if s, ok := v.(string); ok {
				out[k] = normalizeProxyURL(s)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	return nil
}

func routingRuleFromMap(m map[string]any) RoutingRule {
	var rule RoutingRule
	if match, ok := m["match"].(map[string]any); ok {
		rule.Match = make(map[string]string)
		for k, v := range match {
			if s, ok := v.(string); ok {
				rule.Match[k] = s
			}
		}
	}
	if sendTo, ok := m["send_to"].([]any); ok {
		for _, v := range sendTo {
			if s, ok := v.(string); ok {
				rule.SendTo = append(rule.SendTo, s)
			}
		}
	}
	if v, ok := m["default"].(bool); ok {
		rule.Default = v
	}
	return rule
}
