package sender

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackwhich/webhook_alerts/internal/config"
	"github.com/jackwhich/webhook_alerts/internal/metrics"
)

// SendWebhook 向渠道 Webhook URL（如 Slack）发送 JSON 或原始 body，并记录 metrics。
func SendWebhook(ch *config.Channel, body string) SendResult {
	channelName := ch.Name
	client := defaultClient
	if ch.Proxy != nil {
		proxyURL := ch.Proxy["https"]
		if proxyURL == "" {
			proxyURL = ch.Proxy["http"]
		}
		if proxyURL != "" {
			u, err := url.Parse(proxyURL)
			if err == nil {
				client = &http.Client{
					Timeout: 10 * time.Second,
					Transport: &http.Transport{
						Proxy:               http.ProxyURL(u),
						MaxIdleConnsPerHost: 20,
					},
				}
			}
		}
	}
	body = strings.TrimSpace(body)
	var reqBody []byte
	var contentType string
	if body == "" {
		reqBody, _ = json.Marshal(map[string]string{})
		contentType = "application/json; charset=utf-8"
	} else {
		var js json.RawMessage
		if err := json.Unmarshal([]byte(body), &js); err == nil {
			reqBody = []byte(body)
			contentType = "application/json; charset=utf-8"
		} else {
			reqBody = []byte(body)
			contentType = "application/json; charset=utf-8"
		}
	}
	req, _ := http.NewRequest(http.MethodPost, ch.WebhookURL, bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", contentType)
	resp, err := client.Do(req)
	if err != nil {
		reason := classifyError(err)
		metrics.IncAlertsSent(channelName, "failure")
		metrics.IncAlertsSendFailure(channelName, reason)
		return SendResult{Channel: channelName, Success: false, Reason: reason}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		metrics.IncAlertsSent(channelName, "failure")
		metrics.IncAlertsSendFailure(channelName, "http_error")
		return SendResult{Channel: channelName, Success: false, Reason: "http_error"}
	}
	metrics.IncAlertsSent(channelName, "success")
	return SendResult{Channel: channelName, Success: true}
}
