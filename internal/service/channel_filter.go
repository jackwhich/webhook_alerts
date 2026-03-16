package service

import (
	"github.com/jackwhich/webhook_alerts/internal/config"
)

// ChannelFilter 按 image_enabled、send_resolved 等过滤渠道。
type ChannelFilter struct {
	Channels map[string]*config.Channel
}

// FilterImageChannels 筛出需要发图的 Telegram 渠道（已启用、需图、且会接收该状态）。
func (f *ChannelFilter) FilterImageChannels(targetChannels []string, alertStatus string) []*config.Channel {
	var out []*config.Channel
	for _, name := range targetChannels {
		ch := f.Channels[name]
		if ch == nil || ch.Type != "telegram" || !ch.Enabled {
			continue
		}
		if alertStatus == "resolved" && !ch.SendResolved {
			continue
		}
		if !ch.ImageEnabled {
			continue
		}
		out = append(out, ch)
	}
	return out
}

// FilterEnabledChannels 筛出已启用且会接收该告警状态的渠道。
func (f *ChannelFilter) FilterEnabledChannels(targetChannels []string, alertStatus string) []*config.Channel {
	var out []*config.Channel
	for _, name := range targetChannels {
		ch := f.Channels[name]
		if ch == nil || !ch.Enabled {
			continue
		}
		if alertStatus == "resolved" && !ch.SendResolved {
			continue
		}
		out = append(out, ch)
	}
	return out
}
