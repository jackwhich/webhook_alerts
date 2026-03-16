package template

import (
	"bytes"
	"fmt"
	"html"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/jackwhich/webhook_alerts/internal/model"
)

// TemplateDir 存放 .tmpl 的目录（由 main 设置或默认 "templates"）。
var TemplateDir = "templates"

// Render 用告警上下文渲染指定模板，时间会转为 CST。
func Render(templateName string, alert *model.Alert) (string, error) {
	startsAt := alert.StartsAt
	endsAt := alert.EndsAt
	if startsAt != "" {
		startsAt = ConvertToCST(startsAt)
	}
	if endsAt != "" {
		endsAt = ConvertToCST(endsAt)
	}
	annotations := alert.Annotations
	if annotations == nil {
		annotations = make(map[string]string)
	}
	if strings.Contains(templateName, ".json") {
		if desc := annotations["description"]; desc != "" {
			copyAnnot := make(map[string]string)
			for k, v := range annotations {
				copyAnnot[k] = v
			}
			copyAnnot["description"] = ReplaceTimesInDescription(desc)
			annotations = copyAnnot
		}
	}
	ctx := map[string]any{
		"status":      alert.Status,
		"labels":      alert.Labels,
		"annotations": annotations,
		"startsAt":    startsAt,
		"endsAt":      endsAt,
	}
	name := templateName
	if strings.HasSuffix(name, ".j2") {
		name = strings.TrimSuffix(name, ".j2") + ".tmpl"
	} else if !strings.HasSuffix(name, ".tmpl") {
		name = name + ".tmpl"
	}
	path := filepath.Join(TemplateDir, name)
	tmpl, err := template.New(filepath.Base(path)).Funcs(funcMap()).ParseFiles(path)
	if err != nil {
		return "", fmt.Errorf("parse template %s: %w", path, err)
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, filepath.Base(path), ctx); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}
	return buf.String(), nil
}

func funcMap() template.FuncMap {
	return template.FuncMap{
		"e":    html.EscapeString,
		"cst":  ConvertToCST,
		"link": URLToLink,
	}
}

// DetectParseMode 根据模板路径返回 Telegram 解析模式："HTML" 或 ""。
func DetectParseMode(templatePath string) string {
	if templatePath == "" {
		return ""
	}
	if strings.HasSuffix(templatePath, ".html.tmpl") || strings.HasSuffix(templatePath, ".html") {
		return "HTML"
	}
	if strings.HasSuffix(templatePath, ".md.tmpl") || strings.HasSuffix(templatePath, ".md") {
		return "Markdown"
	}
	return ""
}
