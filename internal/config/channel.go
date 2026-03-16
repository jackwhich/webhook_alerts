package config

// Channel 单条告警渠道（Telegram、Slack 等）。
type Channel struct {
	Name         string            `yaml:"-"`             // 由配置 key 填入
	Type         string            `yaml:"type"`          // telegram / slack
	Enabled      bool              `yaml:"enabled"`       // 是否启用，默认 true
	WebhookURL   string            `yaml:"webhook_url"`
	Template     string            `yaml:"template"`
	BotToken     string            `yaml:"bot_token"`
	ChatID       string            `yaml:"chat_id"`
	Proxy        map[string]string `yaml:"-"`              // 加载后填入 http/https
	ProxyEnabled bool              `yaml:"proxy_enabled"`  // 是否走代理，默认 true
	SendResolved bool             `yaml:"send_resolved"`  // 是否发送 resolved，默认 true
	ImageEnabled bool             `yaml:"image_enabled"`  // 是否发趋势图，默认 false
}
