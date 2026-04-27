package main

import (
	"log"
	"strings"
	"time"
)

// checkServerSync 同步检查服务器状态，返回结果（带锁，防止并发检查同一服务器）
func checkServerSync(serverID int64) *ServerStatusDetail {
	// 获取该服务器的检查锁
	lock := getServerCheckLock(serverID)

	// 尝试获取锁，如果正在被检查则直接返回缓存状态
	if !lock.TryLock() {
		log.Printf("服务器 %d 正在被检查，跳过本次请求", serverID)
		statusCacheMu.RLock()
		status, ok := statusCache[serverID]
		statusCacheMu.RUnlock()
		if ok {
			status.Error = "服务器正在被检查，请稍后再试"
			return status
		}
		return &ServerStatusDetail{
			ServerStatus: ServerStatus{
				ServerID:  serverID,
				CheckedAt: time.Now(),
				Error:     "服务器正在被检查，请稍后再试",
			},
		}
	}
	defer lock.Unlock()

	// 增加检查计数
	checkingMu.Lock()
	checkingCount++
	checkingServers[serverID] = true
	checkingMu.Unlock()
	defer func() {
		checkingMu.Lock()
		checkingCount--
		delete(checkingServers, serverID)
		checkingMu.Unlock()
		log.Printf("服务器 %d 检查完成，剩余 %d 个", serverID, checkingCount)
	}()

	// 添加 panic 恢复
	defer func() {
		if r := recover(); r != nil {
			log.Printf("服务器 %d 检查时发生 panic: %v", serverID, r)
		}
	}()

	server, err := GetServerByID(serverID)
	if err != nil {
		log.Printf("获取服务器 %d 信息失败: %v", serverID, err)
		return &ServerStatusDetail{
			ServerStatus: ServerStatus{
				ServerID:  serverID,
				CheckedAt: time.Now(),
				Error:     "获取服务器信息失败: " + err.Error(),
			},
		}
	}

	log.Printf("开始检查服务器 %d (%s)", serverID, server.Host)

	client := NewSSHClient(server.Host, server.Port, server.Username, server.Password)
	if err := client.Connect(); err != nil {
		log.Printf("服务器 %d (%s) SSH 连接失败: %v", serverID, server.Host, err)
		// 保存错误状态
		status := &ServerStatusDetail{
			ServerStatus: ServerStatus{
				ServerID:  serverID,
				Host:      server.Host,
				Username:  server.Username,
				CheckedAt: time.Now(),
				Error:     "连接失败: " + err.Error(),
			},
			Disks:    []DiskInfo{},
			Services: []ServiceStatus{},
		}

		// 先写入内存缓存
		statusCacheMu.Lock()
		statusCache[serverID] = status
		statusCacheMu.Unlock()

		// 同步写入数据库，确保数据立即持久化
		if err := SaveServerStatus(status); err != nil {
			log.Printf("服务器 %d 状态写入数据库失败: %v", serverID, err)
		}
		return status
	}
	defer client.Close()

	// 获取系统信息
	sysInfo, err := client.GetAllSystemInfo()
	if err != nil {
		log.Printf("服务器 %d (%s) 获取系统信息失败: %v", serverID, server.Host, err)
		if sysInfo == nil {
			sysInfo = &SystemInfo{}
		}
	}

	// 解析CPU使用率（去掉%符号）
	cpuUsage := parsePercent(sysInfo.CPU.Usage)

	// 解析内存信息
	memTotal, memUsed, memUsage := parseMemoryInfo(sysInfo.Memory)

	// 解析网络IO
	netRXBytes := parseBytesToInt64(sysInfo.NetworkIO.RX)
	netTXBytes := parseBytesToInt64(sysInfo.NetworkIO.TX)
	netRXSpeed := parseSpeed(sysInfo.NetworkIO.RXSpeed)
	netTXSpeed := parseSpeed(sysInfo.NetworkIO.TXSpeed)

	// 构建磁盘信息
	var diskInfos []DiskInfo
	for _, d := range sysInfo.Disks {
		diskInfos = append(diskInfos, DiskInfo{
			Filesystem:   d.Filesystem,
			MountedOn:    d.MountedOn,
			TotalGB:      parseSizeToGB(d.Size),
			UsedGB:       parseSizeToGB(d.Used),
			UsagePercent: parsePercent(d.UsePercent),
		})
	}

	// 检查服务状态
	var serviceStatuses []ServiceStatus
	if server.Services != "" {
		services := strings.Split(server.Services, ",")
		for _, svc := range services {
			svc = strings.TrimSpace(svc)
			if svc != "" {
				svcStatus := client.CheckServiceStatus(svc)
				serviceStatuses = append(serviceStatuses, ServiceStatus{
					Name:   svc,
					Status: normalizeServiceStatus(svcStatus),
					Detail: svcStatus,
				})
			}
		}
	}

	// 保存状态
	status := &ServerStatusDetail{
		ServerStatus: ServerStatus{
			ServerID:   serverID,
			Host:       server.Host,
			Username:   server.Username,
			CheckedAt:  time.Now(),
			CPUUsage:   cpuUsage,
			MemTotal:   memTotal,
			MemUsed:    memUsed,
			MemUsage:   memUsage,
			NetRXBytes: netRXBytes,
			NetTXBytes: netTXBytes,
			NetRXSpeed: netRXSpeed,
			NetTXSpeed: netTXSpeed,
		},
		Disks:    diskInfos,
		Services: serviceStatuses,
	}
	if err != nil {
		status.Error = "采集失败: " + err.Error()
	}

	// 先写入内存缓存（页面会从这里读取）
	statusCacheMu.Lock()
	statusCache[serverID] = status
	statusCacheMu.Unlock()
	log.Printf("服务器 %d 状态已写入内存缓存", serverID)

	// 同步写入数据库，确保数据立即持久化
	if err := SaveServerStatus(status); err != nil {
		log.Printf("服务器 %d 状态写入数据库失败: %v", serverID, err)
	}
	return status
}

// checkServer 异步检查服务器状态（兼容旧调用）
func checkServer(serverID int64) {
	_ = checkServerSync(serverID)
}
