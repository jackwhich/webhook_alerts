package template

import (
	"regexp"
	"strings"
	"time"
)

var invalidTimeStrings = map[string]bool{
	"未知时间": true, "未知恢复时间": true, "0001-01-01T00:00:00Z": true,
}

var cstFixed = time.FixedZone("CST", 8*3600)

// ConvertToCST 将时间字符串转为北京时间并格式化为 "YYYY-MM-DD HH:MM:SS"。
func ConvertToCST(timeStr string) string {
	timeStr = strings.TrimSpace(timeStr)
	if timeStr == "" || invalidTimeStrings[timeStr] {
		return timeStr
	}
	// 已是本地格式
	if matched, _ := regexp.MatchString(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$`, timeStr); matched {
		return timeStr
	}
	// ISO 8601 带 Z 或时区
	formats := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999Z",
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05.999 +0000 UTC",
	}
	for _, layout := range formats {
		t, err := time.Parse(layout, timeStr)
		if err == nil {
			cst := t.UTC().In(cstFixed)
			return cst.Format("2006-01-02 15:04:05")
		}
	}
	if strings.HasSuffix(timeStr, "Z") {
		t, err := time.Parse("2006-01-02T15:04:05Z", timeStr)
		if err == nil {
			return t.UTC().In(cstFixed).Format("2006-01-02 15:04:05")
		}
		t, err = time.Parse("2006-01-02T15:04:05.999999999Z", timeStr)
		if err == nil {
			return t.UTC().In(cstFixed).Format("2006-01-02 15:04:05")
		}
	}
	return timeStr
}

// ReplaceTimesInDescription 将 description 中的 UTC 时间替换为北京时间。
func ReplaceTimesInDescription(description string) string {
	if description == "" {
		return description
	}
	re := regexp.MustCompile(`(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3} \+0000 UTC)`)
	return re.ReplaceAllStringFunc(description, func(m string) string {
		return ConvertToCST(m)
	})
}

// URLToLink 将文本中的 URL 包成 HTML <a> 标签，供 Telegram HTML 模式使用。
func URLToLink(text string) string {
	if text == "" {
		return text
	}
	re := regexp.MustCompile(`(https?://[^\s\)]+)`)
	return re.ReplaceAllStringFunc(text, func(url string) string {
		url = strings.TrimRight(url, ".,;:!?)")
		return "<a href=\"" + url + "\">" + url + "</a>"
	})
}
