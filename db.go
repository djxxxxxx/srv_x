package main

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

var db *sql.DB

// dbWriteLock 数据库写入锁，防止并发写入冲突
var dbWriteLock sync.Mutex

// User 系统用户
type User struct {
	ID       int64
	Username string
	Password string
	Role     string // admin, user, guest
}

// Server 服务器基础信息
type Server struct {
	ID                int64
	Host              string
	Port              int
	Username          string
	Password          string // 解密后的密码（仅后端使用）
	EncryptedPassword string // 加密后的密码（用于传输给前端）
	Services          string // 逗号分隔的监控服务列表
	CreatedAt         time.Time
}

// ServerStatus 服务器状态（精简版，用于列表展示）
type ServerStatus struct {
	ID        int64
	ServerID  int64
	Host      string
	Username  string
	CheckedAt time.Time

	// CPU信息
	CPUUsage float64 // CPU使用率(0-100)

	// 内存信息 (MB)
	MemTotal int64
	MemUsed  int64
	MemUsage float64 // 内存使用率(0-100)

	// 网络IO
	NetRXBytes int64   // 接收总字节数
	NetTXBytes int64   // 发送总字节数
	NetRXSpeed float64 // 接收速度 KB/s
	NetTXSpeed float64 // 发送速度 KB/s
}

// ServerStatusDetail 服务器状态详情（用于详情页）
type ServerStatusDetail struct {
	ServerStatus

	// 磁盘信息（一对多）
	Disks []DiskInfo

	// 服务状态（一对多）
	Services []ServiceStatus
}

// DiskInfo 磁盘信息
type DiskInfo struct {
	ID           int64
	StatusID     int64   // 关联server_status.id
	Filesystem   string  // 文件系统
	MountedOn    string  // 挂载点
	TotalGB      float64 // 总容量GB
	UsedGB       float64 // 已使用GB
	UsagePercent float64 // 使用率(0-100)
}

// ServiceStatus 服务状态
type ServiceStatus struct {
	ID       int64
	StatusID int64  // 关联server_status.id
	Name     string // 服务名称
	Status   string // 状态: running/stopped/not_installed
	Detail   string // 详细状态（如Oracle的实例信息）
}

// InitDB 初始化数据库
func InitDB() error {
	var err error
	// 使用 WAL 模式提高并发性能
	db, err = sql.Open("sqlite", "./monitor.db?_journal=WAL&_busy_timeout=5000")
	if err != nil {
		return err
	}

	// 设置连接池
	db.SetMaxOpenConns(1) // SQLite 建议单连接
	db.SetMaxIdleConns(1)

	// 创建用户表
	createUserTable := `
	CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT UNIQUE NOT NULL,
		password TEXT NOT NULL,
		role TEXT DEFAULT 'user'
	);`

	// 创建服务器表
	createServerTable := `
	CREATE TABLE IF NOT EXISTS servers (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		host TEXT NOT NULL,
		port INTEGER DEFAULT 22,
		username TEXT NOT NULL,
		password TEXT NOT NULL,
		services TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);`

	// 创建状态表（新结构 - 细化字段）
	createStatusTable := `
	CREATE TABLE IF NOT EXISTS server_status (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		server_id INTEGER NOT NULL,
		host TEXT,
		username TEXT,
		cpu_usage REAL DEFAULT 0,
		mem_total INTEGER DEFAULT 0,
		mem_used INTEGER DEFAULT 0,
		mem_usage REAL DEFAULT 0,
		net_rx_bytes INTEGER DEFAULT 0,
		net_tx_bytes INTEGER DEFAULT 0,
		net_rx_speed REAL DEFAULT 0,
		net_tx_speed REAL DEFAULT 0,
		checked_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (server_id) REFERENCES servers(id) ON DELETE CASCADE
	);`

	// 创建磁盘信息表
	createDiskInfoTable := `
	CREATE TABLE IF NOT EXISTS disk_info (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		status_id INTEGER NOT NULL,
		filesystem TEXT,
		mounted_on TEXT,
		total_gb REAL DEFAULT 0,
		used_gb REAL DEFAULT 0,
		usage_percent REAL DEFAULT 0,
		FOREIGN KEY (status_id) REFERENCES server_status(id) ON DELETE CASCADE
	);`

	// 创建服务状态表
	createServiceStatusTable := `
	CREATE TABLE IF NOT EXISTS service_status (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		status_id INTEGER NOT NULL,
		name TEXT NOT NULL,
		status TEXT DEFAULT 'stopped',
		detail TEXT,
		FOREIGN KEY (status_id) REFERENCES server_status(id) ON DELETE CASCADE
	);`

	// 创建登录失败记录表
	createLoginAttemptsTable := `
	CREATE TABLE IF NOT EXISTS login_attempts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT,
		ip_address TEXT,
		attempt_time DATETIME DEFAULT CURRENT_TIMESTAMP,
		success INTEGER DEFAULT 0
	);`

	if _, err := db.Exec(createUserTable); err != nil {
		return fmt.Errorf("create user table error: %v", err)
	}
	if _, err := db.Exec(createServerTable); err != nil {
		return fmt.Errorf("create server table error: %v", err)
	}
	if _, err := db.Exec(createStatusTable); err != nil {
		return fmt.Errorf("create status table error: %v", err)
	}
	if _, err := db.Exec(createDiskInfoTable); err != nil {
		return fmt.Errorf("create disk info table error: %v", err)
	}
	if _, err := db.Exec(createServiceStatusTable); err != nil {
		return fmt.Errorf("create service status table error: %v", err)
	}
	if _, err := db.Exec(createLoginAttemptsTable); err != nil {
		return fmt.Errorf("create login attempts table error: %v", err)
	}

	// 创建索引优化查询性能
	createIndexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_status_server_id ON server_status(server_id)",
		"CREATE INDEX IF NOT EXISTS idx_status_checked_at ON server_status(checked_at)",
		"CREATE INDEX IF NOT EXISTS idx_disk_status_id ON disk_info(status_id)",
		"CREATE INDEX IF NOT EXISTS idx_service_status_id ON service_status(status_id)",
	}
	for _, idx := range createIndexes {
		if _, err := db.Exec(idx); err != nil {
			log.Printf("create index warning: %v", err)
		}
	}

	// 迁移：为已有用户添加 role 字段（如果不存在）
	_, _ = db.Exec("ALTER TABLE users ADD COLUMN role TEXT DEFAULT 'user'")

	// 数据库迁移：检查是否需要从旧结构迁移到新结构
	migrateDatabase()

	// 插入默认用户（使用哈希密码），如果是新数据库
	hashedPassword, _ := HashPassword("admin123")
	_, _ = db.Exec("INSERT OR IGNORE INTO users (username, password, role) VALUES (?, ?, ?)", "admin", hashedPassword, "admin")

	return nil
}

// migrateDatabase 执行数据库结构迁移
func migrateDatabase() {
	// 检查server_status表是否存在
	var tableExists bool
	err := db.QueryRow(`
		SELECT COUNT(*) FROM sqlite_master 
		WHERE type='table' AND name='server_status'
	`).Scan(&tableExists)

	if err != nil || !tableExists {
		// 表不存在，是新数据库，不需要迁移
		return
	}

	// 检查旧表结构是否存在（通过检查旧列名）
	var hasOldColumns int
	err = db.QueryRow(`
		SELECT COUNT(*) FROM pragma_table_info('server_status') 
		WHERE name IN ('cpu', 'memory', 'disk', 'network_io', 'services')
	`).Scan(&hasOldColumns)

	if err != nil || hasOldColumns == 0 {
		// 没有旧列，说明已迁移
		return
	}

	log.Println("检测到旧数据库结构，开始迁移...")

	// 备份旧表
	_, err = db.Exec(`ALTER TABLE server_status RENAME TO server_status_old`)
	if err != nil {
		log.Printf("备份旧表失败: %v", err)
		return
	}

	// 重新创建新表结构
	createNewStatusTable := `
	CREATE TABLE server_status (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		server_id INTEGER NOT NULL,
		host TEXT,
		username TEXT,
		cpu_usage REAL DEFAULT 0,
		mem_total INTEGER DEFAULT 0,
		mem_used INTEGER DEFAULT 0,
		mem_usage REAL DEFAULT 0,
		net_rx_bytes INTEGER DEFAULT 0,
		net_tx_bytes INTEGER DEFAULT 0,
		net_rx_speed REAL DEFAULT 0,
		net_tx_speed REAL DEFAULT 0,
		checked_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (server_id) REFERENCES servers(id) ON DELETE CASCADE
	);`

	if _, err := db.Exec(createNewStatusTable); err != nil {
		log.Printf("创建新server_status表失败: %v", err)
		return
	}

	// 创建磁盘信息表（如果不存在）
	createDiskInfoTable := `
	CREATE TABLE IF NOT EXISTS disk_info (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		status_id INTEGER NOT NULL,
		filesystem TEXT,
		mounted_on TEXT,
		total_gb REAL DEFAULT 0,
		used_gb REAL DEFAULT 0,
		usage_percent REAL DEFAULT 0,
		FOREIGN KEY (status_id) REFERENCES server_status(id) ON DELETE CASCADE
	);`

	if _, err := db.Exec(createDiskInfoTable); err != nil {
		log.Printf("创建disk_info表失败: %v", err)
	}

	// 创建服务状态表（如果不存在）
	createServiceStatusTable := `
	CREATE TABLE IF NOT EXISTS service_status (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		status_id INTEGER NOT NULL,
		name TEXT NOT NULL,
		status TEXT DEFAULT 'stopped',
		detail TEXT,
		FOREIGN KEY (status_id) REFERENCES server_status(id) ON DELETE CASCADE
	);`

	if _, err := db.Exec(createServiceStatusTable); err != nil {
		log.Printf("创建service_status表失败: %v", err)
	}

	log.Println("旧数据已备份到 server_status_old 表，新表结构已创建")
}

// GetUserByUsername 根据用户名获取用户
func GetUserByUsername(username string) (*User, error) {
	user := &User{}
	err := db.QueryRow("SELECT id, username, password, role FROM users WHERE username = ?", username).
		Scan(&user.ID, &user.Username, &user.Password, &user.Role)
	if err != nil {
		return nil, err
	}
	return user, nil
}

// GetAllUsers 获取所有用户（不包含密码）
func GetAllUsers() ([]*User, error) {
	rows, err := db.Query("SELECT id, username, role FROM users ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []*User
	for rows.Next() {
		user := &User{}
		err := rows.Scan(&user.ID, &user.Username, &user.Role)
		if err != nil {
			continue
		}
		users = append(users, user)
	}
	return users, nil
}

// GetUserByID 根据ID获取用户
func GetUserByID(id int64) (*User, error) {
	user := &User{}
	err := db.QueryRow("SELECT id, username, password, role FROM users WHERE id = ?", id).
		Scan(&user.ID, &user.Username, &user.Password, &user.Role)
	if err != nil {
		return nil, err
	}
	return user, nil
}

// CreateUser 创建用户
func CreateUser(user *User) error {
	hashedPassword, err := HashPassword(user.Password)
	if err != nil {
		return err
	}

	result, err := db.Exec(
		"INSERT INTO users (username, password, role) VALUES (?, ?, ?)",
		user.Username, hashedPassword, user.Role,
	)
	if err != nil {
		return err
	}
	user.ID, _ = result.LastInsertId()
	return nil
}

// UpdateUser 更新用户信息
func UpdateUser(user *User) error {
	// 如果密码不为空，则更新密码
	if user.Password != "" {
		hashedPassword, err := HashPassword(user.Password)
		if err != nil {
			return err
		}
		_, err = db.Exec(
			"UPDATE users SET username = ?, password = ?, role = ? WHERE id = ?",
			user.Username, hashedPassword, user.Role, user.ID,
		)
		return err
	}

	// 不更新密码
	_, err := db.Exec(
		"UPDATE users SET username = ?, role = ? WHERE id = ?",
		user.Username, user.Role, user.ID,
	)
	return err
}

// UpdateUserPassword 更新用户密码（需要旧密码验证）
func UpdateUserPassword(userID int64, oldPassword, newPassword string) error {
	// 获取当前用户密码
	var currentHash string
	err := db.QueryRow("SELECT password FROM users WHERE id = ?", userID).Scan(&currentHash)
	if err != nil {
		return err
	}

	// 验证旧密码
	if !CheckPasswordHash(oldPassword, currentHash) {
		return fmt.Errorf("旧密码错误")
	}

	// 更新新密码
	hashedPassword, err := HashPassword(newPassword)
	if err != nil {
		return err
	}

	_, err = db.Exec("UPDATE users SET password = ? WHERE id = ?", hashedPassword, userID)
	return err
}

// DeleteUser 删除用户
func DeleteUser(id int64) error {
	_, err := db.Exec("DELETE FROM users WHERE id = ?", id)
	return err
}

// RecordLoginAttempt 记录登录尝试
func RecordLoginAttempt(username, ipAddress string, success bool) {
	successInt := 0
	if success {
		successInt = 1
	}
	db.Exec("INSERT INTO login_attempts (username, ip_address, success) VALUES (?, ?, ?)",
		username, ipAddress, successInt)
}

// CheckLoginAttempts 检查登录失败次数
// 返回：是否被锁定、剩余锁定时间（秒）、错误信息
func CheckLoginAttempts(username, ipAddress string) (bool, int, error) {
	// 配置参数
	const (
		maxAttempts     = 5   // 最大尝试次数
		lockoutDuration = 300 // 锁定时间（5分钟 = 300秒）
		windowDuration  = 900 // 统计窗口（15分钟 = 900秒）
	)

	// 清理过期记录（超过窗口期的记录）
	db.Exec("DELETE FROM login_attempts WHERE attempt_time < datetime('now', '-15 minutes')")

	// 检查该用户名/IP的失败次数
	var failedAttempts int
	err := db.QueryRow(`
		SELECT COUNT(*) FROM login_attempts 
		WHERE (username = ? OR ip_address = ?) 
		AND success = 0 
		AND attempt_time > datetime('now', '-15 minutes')
	`, username, ipAddress).Scan(&failedAttempts)

	if err != nil {
		return false, 0, err
	}

	// 如果超过最大尝试次数，检查锁定时间
	if failedAttempts >= maxAttempts {
		// 获取最后一次失败时间
		var lastAttempt string
		db.QueryRow(`
			SELECT attempt_time FROM login_attempts 
			WHERE (username = ? OR ip_address = ?) AND success = 0
			ORDER BY attempt_time DESC LIMIT 1
		`, username, ipAddress).Scan(&lastAttempt)

		// 计算剩余锁定时间
		// 简化处理：直接返回固定锁定时间
		return true, lockoutDuration, fmt.Errorf("登录失败次数过多，请 %d 分钟后重试", lockoutDuration/60)
	}

	return false, 0, nil
}

// CreateServer 创建服务器（密码加密存储）
func CreateServer(server *Server) error {
	// 加密服务器密码
	encryptedPassword, err := EncryptPassword(server.Password)
	if err != nil {
		return fmt.Errorf("encrypt password error: %v", err)
	}

	result, err := db.Exec(
		"INSERT INTO servers (host, port, username, password, services) VALUES (?, ?, ?, ?, ?)",
		server.Host, server.Port, server.Username, encryptedPassword, server.Services,
	)
	if err != nil {
		return err
	}
	server.ID, _ = result.LastInsertId()
	return nil
}

// GetAllServers 获取所有服务器（密码解密，同时保留加密密码用于前端传输）
func GetAllServers() ([]*Server, error) {
	rows, err := db.Query("SELECT id, host, port, username, password, services, created_at FROM servers")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var servers []*Server
	for rows.Next() {
		s := &Server{}
		err := rows.Scan(&s.ID, &s.Host, &s.Port, &s.Username, &s.Password, &s.Services, &s.CreatedAt)
		if err != nil {
			continue
		}
		// 保留加密密码用于前端传输
		s.EncryptedPassword = s.Password
		// 解密服务器密码用于后端使用
		decryptedPassword, err := DecryptPassword(s.Password)
		if err == nil {
			s.Password = decryptedPassword
		}
		servers = append(servers, s)
	}
	return servers, nil
}

// GetServerByID 根据ID获取服务器（密码解密，同时保留加密密码用于前端传输）
func GetServerByID(id int64) (*Server, error) {
	s := &Server{}
	err := db.QueryRow("SELECT id, host, port, username, password, services, created_at FROM servers WHERE id = ?", id).
		Scan(&s.ID, &s.Host, &s.Port, &s.Username, &s.Password, &s.Services, &s.CreatedAt)
	if err != nil {
		return nil, err
	}
	// 保留加密密码用于前端传输
	s.EncryptedPassword = s.Password
	// 解密服务器密码用于后端使用
	decryptedPassword, err := DecryptPassword(s.Password)
	if err == nil {
		s.Password = decryptedPassword
	}
	return s, nil
}

// UpdateServer 更新服务器（智能处理密码：如果是加密格式则保留，否则重新加密）
func UpdateServer(server *Server) error {
	var finalPassword string

	// 尝试解密密码，如果成功说明是加密格式，保留原值
	decryptedPassword, err := DecryptPassword(server.Password)
	if err == nil && decryptedPassword != "" {
		// 密码是加密格式，检查是否真的解密成功（不是乱码）
		// 通过尝试再次加密解密来验证
		finalPassword = server.Password
	} else {
		// 密码是明文，需要加密
		encryptedPassword, err := EncryptPassword(server.Password)
		if err != nil {
			return fmt.Errorf("encrypt password error: %v", err)
		}
		finalPassword = encryptedPassword
	}

	_, err = db.Exec(
		"UPDATE servers SET host = ?, port = ?, username = ?, password = ?, services = ? WHERE id = ?",
		server.Host, server.Port, server.Username, finalPassword, server.Services, server.ID,
	)
	return err
}

// DeleteServer 删除服务器
func DeleteServer(id int64) error {
	// 先删除关联的状态记录（避免外键约束冲突）
	_, err := db.Exec("DELETE FROM server_status WHERE server_id = ?", id)
	if err != nil {
		return err
	}

	// 再删除服务器记录
	_, err = db.Exec("DELETE FROM servers WHERE id = ?", id)
	return err
}

// SaveServerStatus 保存服务器状态（包含磁盘和服务状态，自动清理旧数据）
func SaveServerStatus(status *ServerStatusDetail) error {
	// 使用锁防止并发写入冲突
	dbWriteLock.Lock()
	defer dbWriteLock.Unlock()

	// 使用事务
	tx, err := db.Begin()
	if err != nil {
		log.Printf("服务器 %d 开始事务失败: %v", status.ServerID, err)
		return err
	}

	// 插入主状态记录
	result, err := tx.Exec(
		`INSERT INTO server_status 
		(server_id, host, username, cpu_usage, mem_total, mem_used, mem_usage, net_rx_bytes, net_tx_bytes, net_rx_speed, net_tx_speed, checked_at) 
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		status.ServerID, status.Host, status.Username,
		status.CPUUsage, status.MemTotal, status.MemUsed, status.MemUsage,
		status.NetRXBytes, status.NetTXBytes, status.NetRXSpeed, status.NetTXSpeed,
		status.CheckedAt,
	)
	if err != nil {
		tx.Rollback()
		log.Printf("保存服务器 %d 主状态失败: %v", status.ServerID, err)
		return err
	}

	// 获取新插入的状态ID
	statusID, err := result.LastInsertId()
	if err != nil {
		tx.Rollback()
		log.Printf("获取服务器 %d 状态ID失败: %v", status.ServerID, err)
		return err
	}

	// 插入磁盘信息
	for _, disk := range status.Disks {
		_, err = tx.Exec(
			"INSERT INTO disk_info (status_id, filesystem, mounted_on, total_gb, used_gb, usage_percent) VALUES (?, ?, ?, ?, ?, ?)",
			statusID, disk.Filesystem, disk.MountedOn, disk.TotalGB, disk.UsedGB, disk.UsagePercent,
		)
		if err != nil {
			tx.Rollback()
			log.Printf("保存服务器 %d 磁盘信息失败: %v", status.ServerID, err)
			return err
		}
	}

	// 插入服务状态
	for _, svc := range status.Services {
		_, err = tx.Exec(
			"INSERT INTO service_status (status_id, name, status, detail) VALUES (?, ?, ?, ?)",
			statusID, svc.Name, svc.Status, svc.Detail,
		)
		if err != nil {
			tx.Rollback()
			log.Printf("保存服务器 %d 服务状态失败: %v", status.ServerID, err)
			return err
		}
	}

	// 提交事务
	if err := tx.Commit(); err != nil {
		log.Printf("服务器 %d 提交事务失败: %v", status.ServerID, err)
		return err
	}

	log.Printf("成功保存服务器 %d 状态到数据库，时间: %v, 磁盘数: %d, 服务数: %d",
		status.ServerID, status.CheckedAt, len(status.Disks), len(status.Services))

	// 异步清理旧数据（在锁外执行）
	go cleanupOldStatusRecords()

	return nil
}

// cleanupOldStatusRecords 清理旧的状态记录
func cleanupOldStatusRecords() {
	// 获取总记录数
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM server_status").Scan(&count)
	if err != nil {
		log.Printf("检查记录数失败: %v", err)
		return
	}

	// 如果超过4096条，删除最早的2048条
	if count > 4096 {
		// 使用锁保护删除操作
		dbWriteLock.Lock()
		defer dbWriteLock.Unlock()

		// 使用更简单的删除语句
		_, err = db.Exec(`
			DELETE FROM server_status 
			WHERE id <= (SELECT MAX(id) FROM (SELECT id FROM server_status ORDER BY checked_at ASC LIMIT 2048))
		`)
		if err != nil {
			log.Printf("清理旧状态记录失败: %v", err)
		} else {
			log.Printf("已清理 %d 条旧状态记录", 2048)
		}
	}
}

// GetLatestServerStatus 获取服务器最新状态（包含磁盘和服务详情）
func GetLatestServerStatus(serverID int64) (*ServerStatusDetail, error) {
	// 查询主状态
	s := &ServerStatusDetail{}
	err := db.QueryRow(
		`SELECT id, server_id, host, username, cpu_usage, mem_total, mem_used, mem_usage, 
		net_rx_bytes, net_tx_bytes, net_rx_speed, net_tx_speed, checked_at 
		FROM server_status WHERE server_id = ? ORDER BY checked_at DESC LIMIT 1`,
		serverID,
	).Scan(&s.ID, &s.ServerID, &s.Host, &s.Username,
		&s.CPUUsage, &s.MemTotal, &s.MemUsed, &s.MemUsage,
		&s.NetRXBytes, &s.NetTXBytes, &s.NetRXSpeed, &s.NetTXSpeed, &s.CheckedAt)
	if err != nil {
		return nil, err
	}

	// 查询磁盘信息
	diskRows, err := db.Query(
		"SELECT id, filesystem, mounted_on, total_gb, used_gb, usage_percent FROM disk_info WHERE status_id = ? ORDER BY usage_percent DESC",
		s.ID,
	)
	if err != nil {
		log.Printf("查询服务器 %d 磁盘信息失败: %v", serverID, err)
	} else {
		defer diskRows.Close()
		for diskRows.Next() {
			d := DiskInfo{StatusID: s.ID}
			if err := diskRows.Scan(&d.ID, &d.Filesystem, &d.MountedOn, &d.TotalGB, &d.UsedGB, &d.UsagePercent); err == nil {
				s.Disks = append(s.Disks, d)
			}
		}
	}

	// 查询服务状态
	svcRows, err := db.Query(
		"SELECT id, name, status, detail FROM service_status WHERE status_id = ?",
		s.ID,
	)
	if err != nil {
		log.Printf("查询服务器 %d 服务状态失败: %v", serverID, err)
	} else {
		defer svcRows.Close()
		for svcRows.Next() {
			svc := ServiceStatus{StatusID: s.ID}
			if err := svcRows.Scan(&svc.ID, &svc.Name, &svc.Status, &svc.Detail); err == nil {
				s.Services = append(s.Services, svc)
			}
		}
	}

	return s, nil
}

// GetServerStatusHistory 保留向后兼容
func GetServerStatusHistory(serverID int64, limit int) ([]*ServerStatus, error) {
	if limit <= 0 {
		limit = 100 // 默认返回最近100条
	}

	rows, err := db.Query(
		`SELECT id, server_id, host, username, cpu_usage, mem_total, mem_used, mem_usage, 
		net_rx_bytes, net_tx_bytes, net_rx_speed, net_tx_speed, checked_at 
		FROM server_status WHERE server_id = ? ORDER BY checked_at DESC LIMIT ?`,
		serverID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var history []*ServerStatus
	for rows.Next() {
		s := &ServerStatus{}
		err := rows.Scan(&s.ID, &s.ServerID, &s.Host, &s.Username,
			&s.CPUUsage, &s.MemTotal, &s.MemUsed, &s.MemUsage,
			&s.NetRXBytes, &s.NetTXBytes, &s.NetRXSpeed, &s.NetTXSpeed, &s.CheckedAt)
		if err != nil {
			continue
		}
		history = append(history, s)
	}
	return history, nil
}

// ServerStatusHistory 带最大磁盘使用率的历史记录
type ServerStatusHistory struct {
	ID           int64
	ServerID     int64
	Host         string
	Username     string
	CheckedAt    time.Time
	CPUUsage     float64
	MemTotal     int64
	MemUsed      int64
	MemUsage     float64
	NetRXBytes   int64
	NetTXBytes   int64
	NetRXSpeed   float64
	NetTXSpeed   float64
	MaxDiskUsage float64 // 该时间点所有挂载点中的最大使用率
}

// DiskHistoryPointRaw 磁盘历史数据点（原始结构）
type DiskHistoryPointRaw struct {
	CheckedAt time.Time
	Available float64
}

// GetAllServerStatusOptimized 使用单次查询获取所有服务器最新状态（含磁盘和服务）
func GetAllServerStatusOptimized() (map[int64]*ServerStatusDetail, error) {
	// 查询每个服务器的最新状态ID
	rows, err := db.Query(`
		SELECT s.id, s.server_id, s.host, s.username, s.cpu_usage, s.mem_total, s.mem_used, s.mem_usage,
			s.net_rx_bytes, s.net_tx_bytes, s.net_rx_speed, s.net_tx_speed, s.checked_at
		FROM server_status s
		INNER JOIN (
			SELECT server_id, MAX(checked_at) as max_checked_at
			FROM server_status
			GROUP BY server_id
		) latest ON s.server_id = latest.server_id AND s.checked_at = latest.max_checked_at
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	statusMap := make(map[int64]*ServerStatusDetail)
	statusByID := make(map[int64]*ServerStatusDetail) // 用 status_id 做索引，加速后续关联查询
	statusIDs := []int64{}
	for rows.Next() {
		s := &ServerStatusDetail{}
		err := rows.Scan(&s.ID, &s.ServerID, &s.Host, &s.Username,
			&s.CPUUsage, &s.MemTotal, &s.MemUsed, &s.MemUsage,
			&s.NetRXBytes, &s.NetTXBytes, &s.NetRXSpeed, &s.NetTXSpeed, &s.CheckedAt)
		if err != nil {
			continue
		}
		statusMap[s.ServerID] = s
		statusByID[s.ID] = s
		statusIDs = append(statusIDs, s.ID)
	}
	rows.Close()

	if len(statusIDs) == 0 {
		return statusMap, nil
	}

	// 批量查询磁盘信息
	placeholders := make([]string, len(statusIDs))
	args := make([]interface{}, len(statusIDs))
	for i, id := range statusIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	diskQuery := fmt.Sprintf(
		"SELECT status_id, id, filesystem, mounted_on, total_gb, used_gb, usage_percent FROM disk_info WHERE status_id IN (%s) ORDER BY usage_percent DESC",
		strings.Join(placeholders, ","),
	)
	diskRows, err := db.Query(diskQuery, args...)
	if err == nil {
		defer diskRows.Close()
		for diskRows.Next() {
			var statusID int64
			d := DiskInfo{}
			if err := diskRows.Scan(&statusID, &d.ID, &d.Filesystem, &d.MountedOn, &d.TotalGB, &d.UsedGB, &d.UsagePercent); err == nil {
				if s, ok := statusByID[statusID]; ok {
					d.StatusID = statusID
					s.Disks = append(s.Disks, d)
				}
			}
		}
	}

	// 批量查询服务状态
	svcQuery := fmt.Sprintf(
		"SELECT status_id, id, name, status, detail FROM service_status WHERE status_id IN (%s)",
		strings.Join(placeholders, ","),
	)
	svcRows, err := db.Query(svcQuery, args...)
	if err == nil {
		defer svcRows.Close()
		for svcRows.Next() {
			var statusID int64
			svc := ServiceStatus{}
			if err := svcRows.Scan(&statusID, &svc.ID, &svc.Name, &svc.Status, &svc.Detail); err == nil {
				if s, ok := statusByID[statusID]; ok {
					svc.StatusID = statusID
					s.Services = append(s.Services, svc)
				}
			}
		}
	}

	return statusMap, nil
}

// GetServerStatusHistoryWithDiskUsage 获取历史状态记录（包含每个时间点的最大磁盘使用率）
func GetServerStatusHistoryWithDiskUsage(serverID int64, limit int) ([]*ServerStatusHistory, error) {
	if limit <= 0 {
		limit = 100
	}

	rows, err := db.Query(`
		SELECT s.id, s.server_id, s.host, s.username, s.cpu_usage, s.mem_total, s.mem_used, s.mem_usage,
			s.net_rx_bytes, s.net_tx_bytes, s.net_rx_speed, s.net_tx_speed, s.checked_at,
			COALESCE(MAX(d.usage_percent), 0) as max_disk_usage
		FROM server_status s
		LEFT JOIN disk_info d ON s.id = d.status_id
		WHERE s.server_id = ?
		GROUP BY s.id
		ORDER BY s.checked_at DESC
		LIMIT ?
	`, serverID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var history []*ServerStatusHistory
	for rows.Next() {
		h := &ServerStatusHistory{}
		err := rows.Scan(&h.ID, &h.ServerID, &h.Host, &h.Username,
			&h.CPUUsage, &h.MemTotal, &h.MemUsed, &h.MemUsage,
			&h.NetRXBytes, &h.NetTXBytes, &h.NetRXSpeed, &h.NetTXSpeed, &h.CheckedAt,
			&h.MaxDiskUsage)
		if err != nil {
			continue
		}
		history = append(history, h)
	}
	return history, nil
}

// GetDiskHistory 获取指定挂载点的历史余量数据
func GetDiskHistory(serverID int64, mountPoint string, limit int) ([]DiskHistoryPointRaw, error) {
	if limit <= 0 {
		limit = 100
	}

	rows, err := db.Query(`
		SELECT s.checked_at, (d.total_gb - d.used_gb) as available
		FROM disk_info d
		JOIN server_status s ON d.status_id = s.id
		WHERE s.server_id = ? AND d.mounted_on = ?
		ORDER BY s.checked_at DESC
		LIMIT ?
	`, serverID, mountPoint, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var points []DiskHistoryPointRaw
	for rows.Next() {
		var p DiskHistoryPointRaw
		if err := rows.Scan(&p.CheckedAt, &p.Available); err == nil {
			points = append(points, p)
		}
	}
	return points, nil
}
