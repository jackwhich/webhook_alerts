package routing

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"github.com/alert-router-go/internal/model"
)

var (
	grafanaCache   = make(map[string]time.Time)
	grafanaCacheMu sync.RWMutex
)

// ShouldSkipGrafanaDuplicate 判断是否应跳过（同一 fingerprint+status 在窗口内已发过）。
func ShouldSkipGrafanaDuplicate(alert *model.Alert, config map[string]any) bool {
	dedup, _ := config["grafana_dedup"].(map[string]any)
	if dedup == nil {
		return false
	}
	enabled, _ := dedup["enabled"].(bool)
	if !enabled {
		return false
	}
	key := buildGrafanaKey(alert)
	if key == "" {
		return false
	}
	ttlSec := 90
	if v, ok := dedup["ttl_seconds"].(int); ok && v > 0 {
		ttlSec = v
	}
	clearOnResolved := true
	if v, ok := dedup["clear_on_resolved"].(bool); ok {
		clearOnResolved = v
	}
	now := time.Now()

	grafanaCacheMu.Lock()
	defer grafanaCacheMu.Unlock()
	for k, exp := range grafanaCache {
		if exp.Before(now) {
			delete(grafanaCache, k)
		}
	}
	status := alert.Status
	if status == "resolved" || status == "ok" {
		if clearOnResolved {
			delete(grafanaCache, key)
		}
		return false
	}
	exp, ok := grafanaCache[key]
	if ok && exp.After(now) {
		return true
	}
	grafanaCache[key] = now.Add(time.Duration(ttlSec) * time.Second)
	return false
}

func buildGrafanaKey(alert *model.Alert) string {
	if alert.Fingerprint != "" {
		return "grafana|" + alert.Fingerprint + "|" + alert.Status
	}
	labels := alert.Labels
	alertname := labels["alertname"]
	if alertname == "" {
		return ""
	}
	parts := []string{alertname, alert.Status}
	for _, k := range []string{"grafana_folder", "nginx-alert", "service_name.keyword", "uri.keyword", "status"} {
		if v := labels[k]; v != "" {
			parts = append(parts, k+"="+v)
		}
	}
	raw := ""
	for i, p := range parts {
		if i > 0 {
			raw += "|"
		}
		raw += p
	}
	h := sha256.Sum256([]byte(raw))
	return "grafana|no_fp|" + hex.EncodeToString(h[:])[:16] + "|" + alert.Status
}
