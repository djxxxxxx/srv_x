package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"time"
)

// loginHandler 登录处理
func loginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		http.ServeFile(w, r, "templates/login.html")
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")

	// 获取客户端IP
	ipAddress := r.RemoteAddr
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		ipAddress = forwarded
	}

	// 检查是否被锁定
	locked, remainingTime, err := CheckLoginAttempts(username, ipAddress)
	if err != nil {
		w.Write([]byte(fmt.Sprintf("<script>alert('%s');history.back();</script>", err.Error())))
		return
	}
	if locked {
		w.Write([]byte(fmt.Sprintf("<script>alert('登录失败次数过多，请 %d 分钟后重试');history.back();</script>", remainingTime/60)))
		return
	}

	user, err := GetUserByUsername(username)
	if err != nil || !CheckPasswordHash(password, user.Password) {
		// 记录失败登录
		RecordLoginAttempt(username, ipAddress, false)
		w.Write([]byte("<script>alert('用户名或密码错误');history.back();</script>"))
		return
	}

	// 记录成功登录
	RecordLoginAttempt(username, ipAddress, true)

	// 创建会话
	sessionID := fmt.Sprintf("%d", time.Now().UnixNano())
	sessions[sessionID] = username
	sessionsMu.Lock()
	sessionsExpiry[sessionID] = time.Now().Add(sessionExpiryDuration)
	sessionsMu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		MaxAge:   int(sessionExpiryDuration.Seconds()),
	})

	http.Redirect(w, r, "/dashboard", http.StatusFound)
}

// logoutHandler 退出处理
func logoutHandler(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("session")
	if err == nil {
		sessionsMu.Lock()
		delete(sessions, cookie.Value)
		delete(sessionsExpiry, cookie.Value)
		sessionsMu.Unlock()
	}

	http.SetCookie(w, &http.Cookie{
		Name:   "session",
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})

	http.Redirect(w, r, "/login", http.StatusFound)
}

// dashboardHandler 仪表盘页面
func dashboardHandler(w http.ResponseWriter, r *http.Request) {
	username := getContextUsername(r)

	// 获取用户角色
	user, err := GetUserByUsername(username)
	if err != nil {
		user = &User{Role: "user"}
	}

	servers, err := GetAllServers()
	if err != nil {
		servers = []*Server{}
	}

	// 从数据库读取所有服务器状态（确保数据完整性）
	statusMap, _ := GetAllServerStatusOptimized()

	// 同时更新内存缓存（用数据库中的最新数据）
	statusCacheMu.Lock()
	for id, status := range statusMap {
		statusCache[id] = status
	}
	statusCacheMu.Unlock()

	data := struct {
		Username string
		Role     string
		Servers  []*Server
		Status   map[int64]*ServerStatusDetail
	}{
		Username: username,
		Role:     user.Role,
		Servers:  servers,
		Status:   statusMap,
	}

	renderTemplate(w, "dashboard.html", data)
}

// serverDetailHandler 服务器详情页面
func serverDetailHandler(w http.ResponseWriter, r *http.Request) {
	// 获取服务器ID
	serverIDStr := r.URL.Query().Get("id")
	if serverIDStr == "" {
		http.Error(w, "缺少服务器ID", http.StatusBadRequest)
		return
	}

	serverID, err := strconv.ParseInt(serverIDStr, 10, 64)
	if err != nil {
		http.Error(w, "无效的服务器ID", http.StatusBadRequest)
		return
	}

	// 获取服务器信息
	server, err := GetServerByID(serverID)
	if err != nil {
		http.Error(w, "服务器不存在", http.StatusNotFound)
		return
	}

	// 获取历史记录（获取更多数据用于图表展示）
	history, err := GetServerStatusHistoryWithDiskUsage(serverID, 500)
	if err != nil {
		history = []*ServerStatusHistory{}
	}

	// 获取最新状态（包含磁盘和服务详情）
	latestStatus, _ := GetLatestServerStatus(serverID)

	// 将历史记录转为JSON（使用新结构字段）
	type StatusJSON struct {
		CheckedAt  string  `json:"CheckedAt"`
		CPU        float64 `json:"CPU"`
		MemUsage   float64 `json:"MemUsage"`
		DiskUsage  float64 `json:"DiskUsage"`
		NetRXSpeed float64 `json:"NetRXSpeed"`
		NetTXSpeed float64 `json:"NetTXSpeed"`
	}

	var historyJSON []StatusJSON
	for _, h := range history {
		historyJSON = append(historyJSON, StatusJSON{
			CheckedAt:  h.CheckedAt.Format("2006-01-02T15:04:05+08:00"),
			CPU:        h.CPUUsage,
			MemUsage:   h.MemUsage,
			DiskUsage:  h.MaxDiskUsage,
			NetRXSpeed: h.NetRXSpeed,
			NetTXSpeed: h.NetTXSpeed,
		})
	}

	historyJSONBytes, _ := json.Marshal(historyJSON)

	// 获取每个挂载点的历史余量数据
	type DiskHistoryPoint struct {
		CheckedAt string  `json:"checkedAt"`
		Available float64 `json:"available"`
	}
	type DiskHistory struct {
		MountedOn string             `json:"mountedOn"`
		Points    []DiskHistoryPoint `json:"points"`
	}
	diskHistories := []DiskHistory{}

	// 获取所有唯一的挂载点
	mountPoints := make(map[string]bool)
	if latestStatus != nil {
		for _, d := range latestStatus.Disks {
			mountPoints[d.MountedOn] = true
		}
	}

	for mountPoint := range mountPoints {
		dh := DiskHistory{MountedOn: mountPoint}
		// 查询该挂载点的历史数据（计算余量 = total - used）
		points, err := GetDiskHistory(serverID, mountPoint, 100)
		if err == nil {
			for _, p := range points {
				dh.Points = append(dh.Points, DiskHistoryPoint{
					CheckedAt: p.CheckedAt.Format("2006-01-02T15:04:05+08:00"),
					Available: p.Available,
				})
			}
		}
		// 反转顺序（从旧到新）
		for i, j := 0, len(dh.Points)-1; i < j; i, j = i+1, j-1 {
			dh.Points[i], dh.Points[j] = dh.Points[j], dh.Points[i]
		}
		diskHistories = append(diskHistories, dh)
	}

	diskHistoryJSON, _ := json.Marshal(diskHistories)

	data := struct {
		Server          *Server
		History         []*ServerStatusHistory
		HistoryJSON     template.JS
		LatestStatus    *ServerStatusDetail
		DiskHistoryJSON template.JS
	}{
		Server:          server,
		History:         history,
		HistoryJSON:     template.JS(historyJSONBytes),
		LatestStatus:    latestStatus,
		DiskHistoryJSON: template.JS(diskHistoryJSON),
	}

	renderTemplate(w, "server_detail.html", data)
}

// serverStatusAPIHandler 获取单个服务器最新状态（用于局部刷新）
func serverStatusAPIHandler(w http.ResponseWriter, r *http.Request) {
	idStr := r.URL.Query().Get("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "无效的服务器ID", http.StatusBadRequest)
		return
	}

	// 优先从内存缓存读取
	statusCacheMu.RLock()
	status, ok := statusCache[id]
	statusCacheMu.RUnlock()

	if !ok {
		// 内存缓存没有，从数据库读取
		status, err = GetLatestServerStatus(id)
		if err != nil {
			http.Error(w, "未找到状态", http.StatusNotFound)
			return
		}
	}

	// 转换为前端期望的字段名
	resp := struct {
		CPUUsage   float64         `json:"CPU"`
		MemUsage   float64         `json:"MemUsage"`
		NetRXBytes int64           `json:"NetRXBytes"`
		NetTXBytes int64           `json:"NetTXBytes"`
		NetRXSpeed float64         `json:"NetRXSpeed"`
		NetTXSpeed float64         `json:"NetTXSpeed"`
		Disks      []DiskInfo      `json:"Disks"`
		Services   []ServiceStatus `json:"Services"`
		CheckedAt  time.Time       `json:"CheckedAt"`
		Error      string          `json:"Error"`
	}{
		CPUUsage:   status.CPUUsage,
		MemUsage:   status.MemUsage,
		NetRXBytes: status.NetRXBytes,
		NetTXBytes: status.NetTXBytes,
		NetRXSpeed: status.NetRXSpeed,
		NetTXSpeed: status.NetTXSpeed,
		Disks:      status.Disks,
		Services:   status.Services,
		CheckedAt:  status.CheckedAt,
		Error:      status.Error,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// serversAPIHandler 服务器API处理
func serversAPIHandler(w http.ResponseWriter, r *http.Request) {
	// 获取用户角色
	role := getContextRole(r)

	switch r.Method {
	case "POST":
		// 只有admin可以创建服务器
		if role != "admin" {
			http.Error(w, "权限不足，只有管理员可以添加服务器", http.StatusForbidden)
			return
		}

		// 创建服务器
		port, _ := strconv.Atoi(r.FormValue("port"))
		if port == 0 {
			port = 22
		}

		server := &Server{
			Host:     r.FormValue("host"),
			Port:     port,
			Username: r.FormValue("username"),
			Password: r.FormValue("password"),
			Services: r.FormValue("services"),
		}

		if err := CreateServer(server); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// 立即检查一次
		go checkServer(server.ID)

		w.Write([]byte("OK"))

	case "PUT":
		// 只有admin可以更新服务器
		if role != "admin" {
			http.Error(w, "权限不足，只有管理员可以编辑服务器", http.StatusForbidden)
			return
		}

		// 更新服务器
		r.ParseForm()
		id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
		port, _ := strconv.Atoi(r.FormValue("port"))
		if port == 0 {
			port = 22
		}

		server := &Server{
			ID:       id,
			Host:     r.FormValue("host"),
			Port:     port,
			Username: r.FormValue("username"),
			Password: r.FormValue("password"),
			Services: r.FormValue("services"),
		}

		if err := UpdateServer(server); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Write([]byte("OK"))

	case "DELETE":
		// 只有admin可以删除服务器
		if role != "admin" {
			http.Error(w, "权限不足，只有管理员可以删除服务器", http.StatusForbidden)
			return
		}

		// 删除服务器 - 从URL查询参数获取ID
		r.ParseForm()
		idStr := r.FormValue("id")
		if idStr == "" {
			// 尝试从URL查询参数获取
			idStr = r.URL.Query().Get("id")
		}
		id, _ := strconv.ParseInt(idStr, 10, 64)
		if id == 0 {
			http.Error(w, "无效的服务器ID", http.StatusBadRequest)
			return
		}
		if err := DeleteServer(id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Write([]byte("OK"))

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// checkServerHandler 检查单个服务器（异步）
func checkServerHandler(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	go checkServer(id)
	w.Write([]byte("OK"))
}

// checkServerSyncHandler 同步检查单个服务器并返回结果
func checkServerSyncHandler(w http.ResponseWriter, r *http.Request) {
	idStr := r.URL.Query().Get("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "无效的服务器ID", http.StatusBadRequest)
		return
	}

	status := checkServerSync(id)

	// 转换为前端期望的字段名
	resp := struct {
		CPUUsage   float64         `json:"CPU"`
		MemUsage   float64         `json:"MemUsage"`
		NetRXBytes int64           `json:"NetRXBytes"`
		NetTXBytes int64           `json:"NetTXBytes"`
		NetRXSpeed float64         `json:"NetRXSpeed"`
		NetTXSpeed float64         `json:"NetTXSpeed"`
		Disks      []DiskInfo      `json:"Disks"`
		Services   []ServiceStatus `json:"Services"`
		CheckedAt  time.Time       `json:"CheckedAt"`
		Error      string          `json:"Error"`
	}{
		CPUUsage:   status.CPUUsage,
		MemUsage:   status.MemUsage,
		NetRXBytes: status.NetRXBytes,
		NetTXBytes: status.NetTXBytes,
		NetRXSpeed: status.NetRXSpeed,
		NetTXSpeed: status.NetTXSpeed,
		Disks:      status.Disks,
		Services:   status.Services,
		CheckedAt:  status.CheckedAt,
		Error:      status.Error,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// checkAllServersHandler 检查所有服务器
func checkAllServersHandler(w http.ResponseWriter, r *http.Request) {
	servers, err := GetAllServers()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	for _, server := range servers {
		go checkServer(server.ID)
	}

	w.Write([]byte("OK"))
}

// checkingCountHandler 获取正在检查的服务器数量
func checkingCountHandler(w http.ResponseWriter, r *http.Request) {
	checkingMu.RLock()
	defer checkingMu.RUnlock()

	idStr := r.URL.Query().Get("id")
	if idStr != "" {
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err == nil {
			if checkingServers[id] {
				w.Write([]byte("1"))
			} else {
				w.Write([]byte("0"))
			}
			return
		}
	}

	w.Write([]byte(fmt.Sprintf("%d", checkingCount)))
}

// exportHandler 导出CSV
func exportHandler(w http.ResponseWriter, r *http.Request) {
	servers, err := GetAllServers()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
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

	// 生成带日期时间的文件名
	filename := fmt.Sprintf("servers_%s.csv", time.Now().Format("20060102_150405"))

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment;filename=%s", filename))

	// BOM for Excel
	w.Write([]byte("\xEF\xBB\xBF"))

	writer := csv.NewWriter(w)
	defer writer.Flush()

	// 表头
	writer.Write([]string{"IP地址", "用户名", "CPU使用率(%)", "内存使用", "内存使用率(%)", "磁盘挂载(余量/总量)", "网络RX速度(KB/s)", "网络TX速度(KB/s)", "服务状态", "检查时间"})

	for _, s := range servers {
		status, ok := statusMap[s.ID]
		if !ok {
			status = &ServerStatusDetail{}
		}

		checkedAt := ""
		if !status.CheckedAt.IsZero() {
			checkedAt = status.CheckedAt.Format("2006-01-02 15:04:05")
		}

		// 构建磁盘信息字符串（每行一个挂载点）
		diskInfo := ""
		for _, d := range status.Disks {
			if diskInfo != "" {
				diskInfo += "\n"
			}
			diskInfo += fmt.Sprintf("%s: %.1f%% (%.1f/%.1fG)", d.MountedOn, d.UsagePercent, d.TotalGB-d.UsedGB, d.TotalGB)
		}

		// 构建服务状态字符串（每行一个服务）
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
			fmt.Sprintf("%.2f", status.NetRXSpeed),
			fmt.Sprintf("%.2f", status.NetTXSpeed),
			svcInfo,
			checkedAt,
		})
	}
}

// usersHandler 用户管理页面
func usersHandler(w http.ResponseWriter, r *http.Request) {
	username := getContextUsername(r)
	role := getContextRole(r)

	// 获取所有用户
	users, err := GetAllUsers()
	if err != nil {
		users = []*User{}
	}

	data := struct {
		Username string
		Role     string
		Users    []*User
	}{
		Username: username,
		Role:     role,
		Users:    users,
	}

	renderTemplate(w, "users.html", data)
}

// usersAPIHandler 用户管理API
func usersAPIHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		// 获取单个用户
		idStr := r.URL.Query().Get("id")
		if idStr != "" {
			id, _ := strconv.ParseInt(idStr, 10, 64)
			user, err := GetUserByID(id)
			if err != nil {
				http.Error(w, "用户不存在", http.StatusNotFound)
				return
			}
			// 不返回密码
			user.Password = ""
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":%d,"username":"%s","role":"%s"}`, user.ID, user.Username, user.Role)
			return
		}
		w.Write([]byte("OK"))

	case "POST":
		// 创建用户
		r.ParseForm()
		user := &User{
			Username: r.FormValue("username"),
			Password: r.FormValue("password"),
			Role:     r.FormValue("role"),
		}
		if user.Username == "" || user.Password == "" {
			http.Error(w, "用户名和密码不能为空", http.StatusBadRequest)
			return
		}
		if user.Role == "" {
			user.Role = "user"
		}
		if err := CreateUser(user); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Write([]byte("OK"))

	case "PUT":
		// 更新用户
		r.ParseForm()
		id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
		user := &User{
			ID:       id,
			Username: r.FormValue("username"),
			Password: r.FormValue("password"),
			Role:     r.FormValue("role"),
		}
		if user.Username == "" {
			http.Error(w, "用户名不能为空", http.StatusBadRequest)
			return
		}
		if err := UpdateUser(user); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Write([]byte("OK"))

	case "DELETE":
		// 删除用户
		idStr := r.URL.Query().Get("id")
		if idStr == "" {
			http.Error(w, "无效的用户ID", http.StatusBadRequest)
			return
		}
		id, _ := strconv.ParseInt(idStr, 10, 64)

		// 不能删除自己
		currentUserID := getContextUserID(r)
		if id == currentUserID {
			http.Error(w, "不能删除当前登录用户", http.StatusBadRequest)
			return
		}

		if err := DeleteUser(id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Write([]byte("OK"))

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// changePasswordHandler 修改密码（所有用户可用）
func changePasswordHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.ParseForm()
	oldPassword := r.FormValue("oldPassword")
	newPassword := r.FormValue("newPassword")

	if oldPassword == "" || newPassword == "" {
		http.Error(w, "旧密码和新密码不能为空", http.StatusBadRequest)
		return
	}

	userID := getContextUserID(r)
	if err := UpdateUserPassword(userID, oldPassword, newPassword); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Write([]byte("OK"))
}

// scheduleConfigHandler 获取/更新定时任务配置
func scheduleConfigHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		ScheduleConfig.configMu.RLock()
		config := map[string]interface{}{
			"enabled":   ScheduleConfig.Enabled,
			"interval":  ScheduleConfig.Interval,
			"exportDir": ScheduleConfig.ExportDir,
			"isRunning": ScheduleConfig.isRunning,
			"lastRun":   ScheduleConfig.LastRun.Format("2006-01-02 15:04:05"),
			"nextRun":   ScheduleConfig.NextRun.Format("2006-01-02 15:04:05"),
		}
		ScheduleConfig.configMu.RUnlock()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(config)

	case "POST":
		var req struct {
			Enabled  bool `json:"enabled"`
			Interval int  `json:"interval"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "无效的请求数据", http.StatusBadRequest)
			return
		}

		UpdateScheduleConfig(req.Enabled, req.Interval)
		w.Write([]byte(`{"status":"ok"}`))

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// scheduleTriggerHandler 手动触发定时任务
func scheduleTriggerHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	go performScheduledTask(ScheduleConfig.ExportDir)
	w.Write([]byte(`{"status":"ok","message":"定时任务已手动触发"}`))
}
