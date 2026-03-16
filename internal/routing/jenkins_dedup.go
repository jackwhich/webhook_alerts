package routing

import (
	"sync"
	"time"

	"github.com/alert-router-go/internal/model"
)

var (
	jenkinsCache   = make(map[string]time.Time)
	jenkinsCacheMu sync.RWMutex
)

// ShouldSkipJenkinsFiring 判断是否应跳过本次 firing（去重窗口内重复则跳过）。
func ShouldSkipJenkinsFiring(alert *model.Alert, config map[string]any) bool {
	dedup, _ := config["jenkins_dedup"].(map[string]any)
	if dedup == nil {
		return false
	}
	enabled, _ := dedup["enabled"].(bool)
	if !enabled {
		return false
	}
	key := buildJenkinsKey(alert)
	if key == "" {
		return false
	}
	ttlSec := 900
	if v, ok := dedup["ttl_seconds"].(int); ok && v > 0 {
		ttlSec = v
	}
	clearOnResolved := true
	if v, ok := dedup["clear_on_resolved"].(bool); ok {
		clearOnResolved = v
	}
	now := time.Now()

	jenkinsCacheMu.Lock()
	defer jenkinsCacheMu.Unlock()
	// 清理过期 key
	for k, exp := range jenkinsCache {
		if exp.Before(now) {
			delete(jenkinsCache, k)
		}
	}
	if alert.Status == "resolved" {
		if clearOnResolved {
			delete(jenkinsCache, key)
		}
		return false
	}
	if alert.Status != "firing" {
		return false
	}
	exp, ok := jenkinsCache[key]
	if ok && exp.After(now) {
		return true
	}
	jenkinsCache[key] = now.Add(time.Duration(ttlSec) * time.Second)
	return false
}

func buildJenkinsKey(alert *model.Alert) string {
	labels := alert.Labels
	job := labels["jenkins_job"]
	commit := labels["check_commitID"]
	if job == "" || commit == "" {
		return ""
	}
	alertname := labels["alertname"]
	branch := labels["gitBranch"]
	build := labels["build_number"]
	if build != "" {
		return alertname + "|" + job + "|" + branch + "|build=" + build
	}
	if alert.Fingerprint != "" {
		return alertname + "|" + job + "|" + branch + "|fp=" + alert.Fingerprint
	}
	return alertname + "|" + job + "|" + branch + "|commit=" + commit
}
