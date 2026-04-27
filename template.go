package main

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"
)

// templateFuncMap 全局模板函数映射
var templateFuncMap = template.FuncMap{
	"safeHTML": func(s string) template.HTML {
		return template.HTML(s)
	},
	"sub": func(a, b float64) float64 {
		return a - b
	},
	"mul": func(a, b float64) float64 {
		return a * b
	},
	"split": func(s, sep string) []string {
		return strings.Split(s, sep)
	},
	"progressBar": func(percent float64) template.HTML {
		class := "low"
		if percent >= 80 {
			class = "high"
		} else if percent >= 60 {
			class = "medium"
		}
		return template.HTML(fmt.Sprintf(`<span class="progress-bar"><span class="progress-fill %s" style="width:%.1f%%"></span></span>`, class, percent))
	},
	"serviceTag": func(status string) template.HTML {
		class := "stopped"
		if status == "running" {
			class = "running"
		}
		return template.HTML(fmt.Sprintf(`<span class="service-tag %s">%s</span>`, class, status))
	},
	"formatBytes": func(bytes int64) string {
		const (
			KB = 1024
			MB = KB * 1024
			GB = MB * 1024
			TB = GB * 1024
		)
		switch {
		case bytes >= TB:
			return fmt.Sprintf("%.2f TB", float64(bytes)/TB)
		case bytes >= GB:
			return fmt.Sprintf("%.2f GB", float64(bytes)/GB)
		case bytes >= MB:
			return fmt.Sprintf("%.2f MB", float64(bytes)/MB)
		case bytes >= KB:
			return fmt.Sprintf("%.2f KB", float64(bytes)/KB)
		default:
			return fmt.Sprintf("%d B", bytes)
		}
	},
}

// templateCache 预编译的模板缓存
var templateCache = make(map[string]*template.Template)

// initTemplates 预编译所有模板（在 main 中调用）
func initTemplates() error {
	templates := []string{"dashboard.html", "login.html", "server_detail.html", "users.html"}
	for _, name := range templates {
		tmpl, err := template.New(name).Funcs(templateFuncMap).ParseFiles("templates/" + name)
		if err != nil {
			return fmt.Errorf("parse template %s error: %w", name, err)
		}
		templateCache[name] = tmpl
	}
	return nil
}

// renderTemplate 从缓存渲染模板
func renderTemplate(w http.ResponseWriter, tmpl string, data interface{}) {
	t, ok := templateCache[tmpl]
	if !ok {
		http.Error(w, "template not found: "+tmpl, http.StatusInternalServerError)
		return
	}
	if err := t.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
