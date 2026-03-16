package sender

import (
	"github.com/jackwhich/webhook_alerts/internal/config"
)

// SendResult 单次发送结果。
type SendResult struct {
	Channel string
	Success bool
	Reason  string // 失败原因：空 或 timeout / http_error / invalid_response / network
	Detail  string // 可选，失败时的详细信息（如 HTTP 状态码、Telegram 返回内容）
}

// Sender 向渠道发送告警内容（Telegram 或 Webhook）。
type Sender interface {
	Send(ch *config.Channel, body string, photoBytes []byte) SendResult
}
