package sender

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackwhich/webhook_alerts/internal/config"
	"github.com/jackwhich/webhook_alerts/internal/metrics"
	"github.com/jackwhich/webhook_alerts/internal/template"
)

const pngSignature = "\x89PNG\r\n\x1a\n"

var defaultClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		MaxIdleConnsPerHost: 20,
		MaxIdleConns:        50,
	},
}

// SendTelegram 向 Telegram 发送消息（可选带图），并记录 metrics。
func SendTelegram(ch *config.Channel, body string, photoBytes []byte) SendResult {
	channelName := ch.Name
	parseMode := template.DetectParseMode(ch.Template)
	// Telegram HTML 不支持 <br>，统一换成换行，避免 400 Unsupported start tag "br"
	body = strings.ReplaceAll(body, "<br>", "\n")
	body = strings.ReplaceAll(body, "<br/>", "\n")
	body = strings.ReplaceAll(body, "<br />", "\n")
	text := strings.TrimSpace(body)
	if text == "" {
		text = " "
	}
	caption := text
	if len(caption) > 1024 {
		caption = caption[:1024]
	}
	messageText := text
	if len(messageText) > 4096 {
		messageText = messageText[:4096]
	}

	photoOK := len(photoBytes) >= 100 && len(photoBytes) >= len(pngSignature) && string(photoBytes[:len(pngSignature)]) == pngSignature

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
					Timeout: 15 * time.Second,
					Transport: &http.Transport{
						Proxy:             http.ProxyURL(u),
						MaxIdleConnsPerHost: 20,
					},
				}
			}
		}
	}

	var resp *http.Response
	var err error
	if photoOK {
		resp, err = sendTelegramPhoto(client, ch, caption, parseMode, photoBytes)
	} else {
		resp, err = sendTelegramMessage(client, ch, messageText, parseMode)
	}
	if err != nil {
		reason := classifyError(err)
		metrics.IncAlertsSent(channelName, "failure")
		metrics.IncAlertsSendFailure(channelName, reason)
		return SendResult{Channel: channelName, Success: false, Reason: reason, Detail: err.Error()}
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		metrics.IncAlertsSent(channelName, "failure")
		metrics.IncAlertsSendFailure(channelName, "http_error")
		detail := fmt.Sprintf("status=%d", resp.StatusCode)
		if len(bodyBytes) > 0 {
			if len(bodyBytes) > 200 {
				detail += " body=" + string(bodyBytes[:200]) + "..."
			} else {
				detail += " body=" + string(bodyBytes)
			}
		}
		return SendResult{Channel: channelName, Success: false, Reason: "http_error", Detail: detail}
	}
	// Telegram 可能返回 200 但 body 里 ok:false
	var tgResp struct {
		OK bool `json:"ok"`
	}
	_ = json.Unmarshal(bodyBytes, &tgResp)
	if !tgResp.OK {
		metrics.IncAlertsSent(channelName, "failure")
		metrics.IncAlertsSendFailure(channelName, "invalid_response")
		return SendResult{Channel: channelName, Success: false, Reason: "invalid_response"}
	}
	metrics.IncAlertsSent(channelName, "success")
	return SendResult{Channel: channelName, Success: true}
}

func sendTelegramPhoto(client *http.Client, ch *config.Channel, caption, parseMode string, photoBytes []byte) (*http.Response, error) {
	apiURL := "https://api.telegram.org/bot" + ch.BotToken + "/sendPhoto"
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("chat_id", ch.ChatID)
	_ = w.WriteField("caption", caption)
	if parseMode != "" {
		_ = w.WriteField("parse_mode", parseMode)
	}
	part, _ := w.CreateFormFile("photo", "alert.png")
	_, _ = part.Write(photoBytes)
	_ = w.Close()
	req, _ := http.NewRequest(http.MethodPost, apiURL, &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return client.Do(req)
}

func sendTelegramMessage(client *http.Client, ch *config.Channel, text, parseMode string) (*http.Response, error) {
	apiURL := "https://api.telegram.org/bot" + ch.BotToken + "/sendMessage"
	payload := map[string]any{
		"chat_id":               ch.ChatID,
		"text":                  text,
		"disable_web_page_preview": true,
	}
	if parseMode != "" {
		payload["parse_mode"] = parseMode
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, apiURL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return client.Do(req)
}

func classifyError(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if strings.Contains(s, "timeout") || strings.Contains(s, "Timeout") {
		return "timeout"
	}
	if strings.Contains(s, "connection refused") || strings.Contains(s, "dial") {
		return "network"
	}
	return "http_error"
}
