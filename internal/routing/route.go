package routing

import (
	"regexp"
	"sync"

	"github.com/jackwhich/webhook_alerts/internal/config"
	"github.com/jackwhich/webhook_alerts/internal/model"
)

var regexCache = make(map[string]*regexp.Regexp)
var regexCacheMu sync.RWMutex

// Match 判断 labels 是否满足规则条件（精确或正则）。
func Match(labels map[string]string, cond map[string]string) bool {
	for k, v := range cond {
		val := labels[k]
		if !matchValue(val, v) {
			return false
		}
	}
	return true
}

func matchValue(labelVal, condVal string) bool {
	// 是否像正则：含 * ^ $ | ( ) [ ] + ? { }
	regexLike := false
	for _, c := range condVal {
		switch c {
		case '*', '^', '$', '|', '(', ')', '[', ']', '+', '?', '{', '}':
			regexLike = true
			break
		}
	}
	if regexLike || len(condVal) >= 2 && (condVal[:2] == ".*" || condVal[len(condVal)-2:] == ".*") {
		pattern := condVal
		if len(condVal) >= 4 && condVal[:2] == ".*" && condVal[len(condVal)-2:] == ".*" {
			pattern = condVal[2 : len(condVal)-2]
		} else if len(condVal) >= 2 && condVal[:2] == ".*" {
			pattern = condVal[2:] + "$"
		} else if len(condVal) >= 2 && condVal[len(condVal)-2:] == ".*" {
			pattern = "^" + condVal[:len(condVal)-2]
		}
		re := getRegex(pattern)
		if re != nil {
			return re.MatchString(labelVal)
		}
	}
	return labelVal == condVal
}

func getRegex(pattern string) *regexp.Regexp {
	regexCacheMu.RLock()
	re := regexCache[pattern]
	regexCacheMu.RUnlock()
	if re != nil {
		return re
	}
	regexCacheMu.Lock()
	defer regexCacheMu.Unlock()
	re = regexCache[pattern]
	if re != nil {
		return re
	}
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return nil
	}
	regexCache[pattern] = compiled
	return compiled
}

// Route 根据 labels 与配置返回要发送的渠道名（去重、排序）。
func Route(labels map[string]string, cfg *config.Config) []string {
	rules := cfg.Routing
	if len(rules) == 0 {
		return nil
	}
	channelSet := make(map[string]struct{})
	var defaultChannels []string
	for i, r := range rules {
		if len(r.Match) > 0 {
			if Match(labels, r.Match) {
				for _, ch := range r.SendTo {
					channelSet[ch] = struct{}{}
				}
			}
		} else if r.Default {
			if defaultChannels == nil {
				defaultChannels = r.SendTo
			}
		}
		_ = i
	}
	if len(channelSet) == 0 && defaultChannels != nil {
		for _, ch := range defaultChannels {
			channelSet[ch] = struct{}{}
		}
	}
	var out []string
	for ch := range channelSet {
		if cfg.Channels[ch] != nil && cfg.Channels[ch].Enabled {
			out = append(out, ch)
		}
	}
	// 排序保证输出稳定
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// MatchLabels 构造用于路由的标签集（labels + _source + _receiver）。
func MatchLabels(alert *model.Alert) map[string]string {
	m := make(map[string]string)
	for k, v := range alert.Labels {
		m[k] = v
	}
	if alert.Source != "" {
		m["_source"] = alert.Source
	}
	if alert.Receiver != "" {
		m["_receiver"] = alert.Receiver
	}
	return m
}
