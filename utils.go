package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// stripHTML 去除HTML标签，保留换行格式
func stripHTML(html string) string {
	result := html
	// 替换 <div>、</div>、<p>、</p>、<br>、<br/> 为换行符
	result = regexp.MustCompile(`</?div[^>]*>|</?p[^>]*>|<br\s*/?>`).ReplaceAllString(result, "\n")
	// 替换其他 HTML 标签为空字符串
	result = regexp.MustCompile("<[^>]*>").ReplaceAllString(result, "")
	// 去除多余空格（但保留换行）
	result = strings.TrimSpace(result)
	// 将多个连续换行替换为单个换行
	result = regexp.MustCompile(`\n\s*\n`).ReplaceAllString(result, "\n")
	return result
}

// generateProgressBar 生成血条HTML
func generateProgressBar(percentStr string) string {
	// 解析百分比字符串，如 "35%" -> 35
	percentStr = strings.TrimSpace(percentStr)
	percentStr = strings.TrimSuffix(percentStr, "%")
	var percent int
	fmt.Sscanf(percentStr, "%d", &percent)

	// 确定颜色级别
	var level string
	if percent < 60 {
		level = "low"
	} else if percent < 85 {
		level = "medium"
	} else {
		level = "high"
	}

	return fmt.Sprintf(
		"<span class='progress-bar-wrapper'><span class='progress-bar'><span class='progress-fill %s' style='width:%d%%'></span></span></span>",
		level, percent,
	)
}

// parsePercent 解析百分比字符串为float64
func parsePercent(s string) float64 {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "%")
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

// parseMemoryInfo 解析内存信息，返回总内存(MB)、已用内存(MB)、使用率
func parseMemoryInfo(m MemoryInfo) (total, used int64, usage float64) {
	total = parseSizeToMB(m.Total)
	used = parseSizeToMB(m.Used)
	usage = parsePercent(m.UsagePercent)
	return
}

// parseSizeToMB 将大小字符串转换为MB
func parseSizeToMB(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "N/A" {
		return 0
	}

	// 提取数值和单位
	var value float64
	var unit string
	fmt.Sscanf(s, "%f%s", &value, &unit)

	unit = strings.ToUpper(unit)
	switch {
	case strings.Contains(unit, "TIB"), strings.Contains(unit, "TB"), unit == "T":
		return int64(value * 1024 * 1024)
	case strings.Contains(unit, "GIB"), strings.Contains(unit, "GB"), unit == "G":
		return int64(value * 1024)
	case strings.Contains(unit, "MIB"), strings.Contains(unit, "MB"), unit == "M":
		return int64(value)
	case strings.Contains(unit, "KIB"), strings.Contains(unit, "KB"), unit == "K":
		return int64(value / 1024)
	default:
		// 尝试直接解析为字节
		if strings.Contains(unit, "B") {
			return int64(value / 1024 / 1024)
		}
		return int64(value)
	}
}

// parseSizeToGB 将大小字符串转换为GB
func parseSizeToGB(s string) float64 {
	mb := parseSizeToMB(s)
	return float64(mb) / 1024
}

// parseBytesToInt64 解析字节数字符串
func parseBytesToInt64(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "N/A" {
		return 0
	}

	// 移除可能的单位并转换
	var value float64
	var unit string
	fmt.Sscanf(s, "%f%s", &value, &unit)

	unit = strings.ToUpper(unit)
	switch {
	case strings.Contains(unit, "TB"):
		return int64(value * 1024 * 1024 * 1024 * 1024)
	case strings.Contains(unit, "GB"):
		return int64(value * 1024 * 1024 * 1024)
	case strings.Contains(unit, "MB"):
		return int64(value * 1024 * 1024)
	case strings.Contains(unit, "KB"):
		return int64(value * 1024)
	default:
		return int64(value)
	}
}

// parseSpeed 解析速度字符串为float64 (KB/s)
func parseSpeed(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "N/A" {
		return 0
	}

	var value float64
	var unit string
	fmt.Sscanf(s, "%f%s", &value, &unit)

	unit = strings.ToUpper(unit)
	switch {
	case strings.Contains(unit, "MB/S"):
		return value * 1024
	case strings.Contains(unit, "KB/S"):
		return value
	default:
		return value
	}
}

// normalizeServiceStatus 标准化服务状态
func normalizeServiceStatus(s string) string {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "运行中") || strings.Contains(s, "active") || strings.Contains(s, "READY") {
		return "running"
	}
	if strings.Contains(s, "已停止") || strings.Contains(s, "inactive") || strings.Contains(s, "stopped") {
		return "stopped"
	}
	if strings.Contains(s, "未安装") || strings.Contains(s, "not_installed") {
		return "not_installed"
	}
	return "unknown"
}
