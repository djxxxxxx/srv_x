package main

import (
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

// Session 简单会话管理
var (
	sessions       = make(map[string]string)
	sessionsExpiry = make(map[string]time.Time)
	sessionsMu     sync.RWMutex
	// sessionExpiryDuration 会话过期时间
	sessionExpiryDuration = 24 * time.Hour
)

// 检查锁，防止同一台服务器被重复检查
var (
	checkingLocks   = make(map[int64]*sync.Mutex)
	checkingMu      sync.RWMutex
	checkingCount   int64           // 正在检查的服务器数量
	checkingServers = make(map[int64]bool) // 正在检查的服务器ID集合
)

// 内存缓存，存储服务器最新状态
var (
	statusCache   = make(map[int64]*ServerStatusDetail)
	statusCacheMu sync.RWMutex
)

func main() {
	// 初始化数据库
	if err := InitDB(); err != nil {
		log.Fatal("数据库初始化失败:", err)
	}

	// 预编译模板
	if err := initTemplates(); err != nil {
		log.Fatal("模板初始化失败:", err)
	}

	// 启动 session 过期清理 goroutine
	go cleanupExpiredSessions()

	// 静态文件
	fs := http.FileServer(http.Dir("static"))
	http.Handle("/static/", http.StripPrefix("/static/", fs))

	// 路由
	http.HandleFunc("/", loginHandler)
	http.HandleFunc("/login", loginHandler)
	http.HandleFunc("/logout", logoutHandler)
	http.HandleFunc("/dashboard", authMiddleware(dashboardHandler))
	http.HandleFunc("/api/servers", authMiddleware(serversAPIHandler))
	http.HandleFunc("/api/server/status", authMiddleware(serverStatusAPIHandler))
	http.HandleFunc("/api/server/check", authMiddleware(checkServerHandler))
	http.HandleFunc("/api/server/check-sync", authMiddleware(checkServerSyncHandler))
	http.HandleFunc("/api/server/checkall", authMiddleware(checkAllServersHandler))
	http.HandleFunc("/api/server/checking-count", authMiddleware(checkingCountHandler))
	http.HandleFunc("/api/server/export", authMiddleware(exportHandler))

	// 定时任务配置路由（仅管理员）
	http.HandleFunc("/api/schedule/config", authMiddleware(adminMiddleware(scheduleConfigHandler)))
	http.HandleFunc("/api/schedule/trigger", authMiddleware(adminMiddleware(scheduleTriggerHandler)))

	// 用户管理路由
	http.HandleFunc("/users", authMiddleware(usersHandler))
	http.HandleFunc("/api/users", authMiddleware(adminMiddleware(usersAPIHandler)))
	http.HandleFunc("/api/users/password", authMiddleware(changePasswordHandler))

	// 服务器详情路由
	http.HandleFunc("/server/detail", authMiddleware(serverDetailHandler))

	// 启动定时任务 - 每天早上9点自动全量更新并导出CSV
	go startScheduledTask()

	fmt.Println("服务器启动在 http://localhost:18080")
	log.Fatal(http.ListenAndServe(":18080", nil))
}

// getServerCheckLock 获取服务器的检查锁
func getServerCheckLock(serverID int64) *sync.Mutex {
	checkingMu.Lock()
	defer checkingMu.Unlock()

	if lock, exists := checkingLocks[serverID]; exists {
		return lock
	}

	lock := &sync.Mutex{}
	checkingLocks[serverID] = lock
	return lock
}

// cleanupExpiredSessions 定期清理过期的 session
func cleanupExpiredSessions() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for range ticker.C {
		sessionsMu.Lock()
		now := time.Now()
		for sid, expiry := range sessionsExpiry {
			if now.After(expiry) {
				delete(sessions, sid)
				delete(sessionsExpiry, sid)
			}
		}
		sessionsMu.Unlock()
	}
}
