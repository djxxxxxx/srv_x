package main

import (
	"encoding/csv"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ScheduleConfig 定时任务配置
var ScheduleConfig = struct {
	Enabled   bool   // 是否启用
	Interval  int    // 间隔小时数
	ExportDir string // 导出目录
	LastRun   time.Time
	NextRun   time.Time
	isRunning bool
	stopChan  chan bool
	configMu  sync.RWMutex
}{
	Enabled:   true,
	Interval:  1, // 默认每小时
	ExportDir: "export",
}

// startScheduledTask 启动定时任务 - 根据配置自动全量更新并导出CSV（整点触发）
func startScheduledTask() {
	// 创建 export 目录
	if err := os.MkdirAll(ScheduleConfig.ExportDir, 0755); err != nil {
		log.Printf("创建导出目录失败: %v", err)
		return
	}

	ScheduleConfig.configMu.Lock()
	ScheduleConfig.stopChan = make(chan bool)
	ScheduleConfig.isRunning = true
	ScheduleConfig.configMu.Unlock()

	log.Printf("定时任务已启动，间隔: %d小时，整点触发", ScheduleConfig.Interval)

	// 计算下一个整点时间
	now := time.Now()
	nextHour := now.Truncate(time.Hour).Add(time.Hour)
	if ScheduleConfig.Interval > 1 {
		// 如果间隔大于1小时，计算下一个符合间隔的整点
		currentHour := now.Hour()
		nextIntervalHour := ((currentHour / ScheduleConfig.Interval) + 1) * ScheduleConfig.Interval
		if nextIntervalHour >= 24 {
			nextIntervalHour = 0
			nextHour = now.Add(24 * time.Hour).Truncate(24 * time.Hour)
		} else {
			nextHour = time.Date(now.Year(), now.Month(), now.Day(), nextIntervalHour, 0, 0, 0, now.Location())
		}
	}

	waitDuration := nextHour.Sub(now)
	log.Printf("等待到下一个整点: %s (还有 %v)", nextHour.Format("2006-01-02 15:04:05"), waitDuration)

	// 更新下次运行时间
	ScheduleConfig.configMu.Lock()
	ScheduleConfig.NextRun = nextHour
	ScheduleConfig.configMu.Unlock()

	// 等待到下一个整点
	select {
	case <-time.After(waitDuration):
		// 到达整点，执行第一次
		ScheduleConfig.configMu.Lock()
		ScheduleConfig.LastRun = time.Now()
		ScheduleConfig.configMu.Unlock()
		go performScheduledTask(ScheduleConfig.ExportDir)
	case <-ScheduleConfig.stopChan:
		log.Println("定时任务已停止")
		return
	}

	// 之后每隔指定小时执行（整点）
	ticker := time.NewTicker(time.Duration(ScheduleConfig.Interval) * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// 更新运行时间
			ScheduleConfig.configMu.Lock()
			ScheduleConfig.LastRun = time.Now()
			ScheduleConfig.NextRun = time.Now().Add(time.Duration(ScheduleConfig.Interval) * time.Hour)
			ScheduleConfig.configMu.Unlock()
			go performScheduledTask(ScheduleConfig.ExportDir)
		case <-ScheduleConfig.stopChan:
			log.Println("定时任务已停止")
			return
		}
	}
}

// StopScheduledTask 停止定时任务
func StopScheduledTask() {
	ScheduleConfig.configMu.Lock()
	defer ScheduleConfig.configMu.Unlock()
	if ScheduleConfig.isRunning && ScheduleConfig.stopChan != nil {
		close(ScheduleConfig.stopChan)
		ScheduleConfig.isRunning = false
	}
}

// RestartScheduledTask 重启定时任务（配置变更后调用）
func RestartScheduledTask() {
	StopScheduledTask()
	// 等待一小段时间确保旧任务停止
	time.Sleep(100 * time.Millisecond)
	go startScheduledTask()
}

// UpdateScheduleConfig 更新定时任务配置
func UpdateScheduleConfig(enabled bool, interval int) {
	ScheduleConfig.configMu.Lock()
	ScheduleConfig.Enabled = enabled
	if interval > 0 {
		ScheduleConfig.Interval = interval
	}
	ScheduleConfig.configMu.Unlock()

	// 如果正在运行，重启以应用新配置
	if ScheduleConfig.isRunning {
		RestartScheduledTask()
	} else if enabled {
		go startScheduledTask()
	}
}

// performScheduledTask 执行定时任务：全量更新，只在17点导出CSV
func performScheduledTask(exportDir string) {
	currentHour := time.Now().Hour()
	shouldExport := currentHour == 17 // 只在17点导出CSV

	if shouldExport {
		log.Println("开始执行定时任务：自动全量更新并导出CSV（17点任务）")
	} else {
		log.Println("开始执行定时任务：自动全量更新（每小时巡检）")
	}

	// 1. 获取所有服务器
	servers, err := GetAllServers()
	if err != nil {
		log.Printf("定时任务：获取服务器列表失败: %v", err)
		return
	}

	// 2. 全量更新
	log.Printf("定时任务：开始检查 %d 个服务器", len(servers))
	for _, server := range servers {
		go checkServer(server.ID)
	}

	// 3. 等待检查完成（最多等待120秒）
	time.Sleep(10 * time.Second) // 先等待10秒让检查开始
	maxWait := 120
	for i := 0; i < maxWait; i++ {
		checkingMu.RLock()
		count := checkingCount
		checkingMu.RUnlock()
		if count == 0 {
			break
		}
		time.Sleep(1 * time.Second)
	}

	// 4. 只在17点导出CSV
	if shouldExport {
		log.Println("定时任务：开始导出CSV")
		exportPath := filepath.Join(exportDir, fmt.Sprintf("servers_%s.csv", time.Now().Format("20060102_150405")))
		if err := exportCSVToFile(exportPath); err != nil {
			log.Printf("定时任务：导出CSV失败: %v", err)
			return
		}
		log.Printf("定时任务完成，CSV已导出到: %s", exportPath)
	} else {
		log.Println("定时任务完成（未导出CSV，仅在17点导出）")
	}
}

// exportCSVToFile 导出CSV到指定文件
func exportCSVToFile(filename string) error {
	servers, err := GetAllServers()
	if err != nil {
		return err
	}

	// 优先从内存缓存获取状态
	statusMap := make(map[int64]*ServerStatusDetail)
	statusCacheMu.RLock()
	for _, server := range servers {
		if status, ok := statusCache[server.ID]; ok {
			statusMap[server.ID] = status
		}
	}
	statusCacheMu.RUnlock()

	// 如果内存缓存为空，从数据库读取
	if len(statusMap) == 0 {
		statusMap, _ = GetAllServerStatusOptimized()
	}

	// 创建文件
	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	// BOM for Excel
	file.Write([]byte("\xEF\xBB\xBF"))

	writer := csv.NewWriter(file)
	defer writer.Flush()

	// 表头
	writer.Write([]string{"IP地址", "用户名", "CPU使用率(%)", "内存使用", "内存使用率(%)", "磁盘挂载", "网络RX速度", "网络TX速度", "服务状态", "检查时间"})

	// 数据
	for _, s := range servers {
		status, ok := statusMap[s.ID]
		if !ok {
			status = &ServerStatusDetail{}
		}

		checkedAt := ""
		if !status.CheckedAt.IsZero() {
			checkedAt = status.CheckedAt.Format("2006-01-02 15:04:05")
		}

		// 构建磁盘信息字符串（每项换行）
		diskInfo := ""
		for _, d := range status.Disks {
			if diskInfo != "" {
				diskInfo += "\n"
			}
			diskInfo += fmt.Sprintf("%s: %.1f%% (%.1fGB/%.1fGB)", d.MountedOn, d.UsagePercent, d.TotalGB-d.UsedGB, d.TotalGB)
		}

		// 构建服务状态字符串（每项换行）
		svcInfo := ""
		for _, svc := range status.Services {
			if svcInfo != "" {
				svcInfo += "\n"
			}
			svcInfo += fmt.Sprintf("%s: %s", svc.Name, svc.Status)
		}

		writer.Write([]string{
			s.Host,
			s.Username,
			fmt.Sprintf("%.1f", status.CPUUsage),
			fmt.Sprintf("%d/%d MB", status.MemUsed, status.MemTotal),
			fmt.Sprintf("%.1f", status.MemUsage),
			diskInfo,
			fmt.Sprintf("%.2f KB/s", status.NetRXSpeed),
			fmt.Sprintf("%.2f KB/s", status.NetTXSpeed),
			svcInfo,
			checkedAt,
		})
	}

	return nil
}
