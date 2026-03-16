package model

// Alert 标准化告警结构，在 adapter、routing、service、template 间通用。
type Alert struct {
	Status       string            `json:"status"`        // firing / resolved
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     string            `json:"startsAt"`
	EndsAt       string            `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Fingerprint  string            `json:"fingerprint,omitempty"`
	Source       string            `json:"_source"`   // prometheus / grafana / unknown
	Receiver     string            `json:"_receiver"` // 来自 payload.receiver
}

// GetLabel 取 label 值，无则返回空字符串。
func (a *Alert) GetLabel(key string) string {
	if a.Labels == nil {
		return ""
	}
	return a.Labels[key]
}
