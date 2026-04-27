package main

import (
	"bytes"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSHClient SSH客户端
type SSHClient struct {
	Host     string
	Port     int
	Username string
	Password string
	client   *ssh.Client
}

// NewSSHClient 创建SSH客户端
func NewSSHClient(host string, port int, username, password string) *SSHClient {
	return &SSHClient{
		Host:     host,
		Port:     port,
		Username: username,
		Password: password,
	}
}

// Connect 连接SSH
func (c *SSHClient) Connect() error {
	config := &ssh.ClientConfig{
		User: c.Username,
		Auth: []ssh.AuthMethod{
			ssh.Password(c.Password),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}

	addr := fmt.Sprintf("%s:%d", c.Host, c.Port)
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return err
	}
	c.client = client
	return nil
}

// Close 关闭连接
func (c *SSHClient) Close() {
	if c.client != nil {
		c.client.Close()
	}
}

// RunCommand 执行命令（带超时）
func (c *SSHClient) RunCommand(cmd string) (string, error) {
	if c.client == nil {
		if err := c.Connect(); err != nil {
			return "", err
		}
	}

	session, err := c.client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()

	var stdout bytes.Buffer
	session.Stdout = &stdout

	// 使用通道和超时机制
	type result struct {
		output string
		err    error
	}
	done := make(chan result, 1)

	go func() {
		err := session.Run(cmd)
		done <- result{stdout.String(), err}
	}()

	select {
	case res := <-done:
		return res.output, res.err
	case <-time.After(15 * time.Second):
		return "", fmt.Errorf("command timeout")
	}
}

// SystemInfo 系统信息
type SystemInfo struct {
	CPU       CPUInfo
	Memory    MemoryInfo
	Disks     []SSHDiskInfo
	NetworkIO NetworkIOInfo
}

// CPUInfo CPU信息
type CPUInfo struct {
	Usage string
}

// MemoryInfo 内存信息
type MemoryInfo struct {
	Total        string
	Used         string
	Free         string
	UsagePercent string
}

// SSHDiskInfo SSH采集的磁盘信息（原始字符串格式）
type SSHDiskInfo struct {
	Filesystem string
	Size       string
	Used       string
	Available  string
	UsePercent string
	MountedOn  string
}

// NetworkIOInfo 网络IO信息
type NetworkIOInfo struct {
	RX      string
	TX      string
	RXSpeed string // 接收瞬时速度
	TXSpeed string // 发送瞬时速度
}

// GetCPUInfo 获取CPU信息
func (c *SSHClient) GetCPUInfo() (*CPUInfo, error) {
	// 获取CPU使用率
	cmd := "top -bn1 | grep 'Cpu(s)' | awk '{print $2}' | cut -d'%' -f1"
	output, err := c.RunCommand(cmd)
	if err != nil {
		return nil, err
	}

	usage := strings.TrimSpace(output)
	if usage == "" {
		// 尝试另一种方式
		cmd = "grep 'cpu ' /proc/stat | awk '{usage=($2+$4)*100/($2+$4+$5)} END {print usage}'"
		output, err = c.RunCommand(cmd)
		if err != nil {
			return nil, err
		}
		usage = strings.TrimSpace(output)
	}

	return &CPUInfo{
		Usage: usage + "%",
	}, nil
}

// GetMemoryInfo 获取内存信息
func (c *SSHClient) GetMemoryInfo() (*MemoryInfo, error) {
	cmd := "free -h | grep Mem"
	output, err := c.RunCommand(cmd)
	if err != nil {
		return nil, err
	}

	// 解析: Mem:        7.7Gi       2.1Gi       3.2Gi       0.2Gi       2.4Gi       5.2Gi
	parts := strings.Fields(output)
	if len(parts) < 3 {
		return nil, fmt.Errorf("无法解析内存信息")
	}

	total := parts[1]
	used := parts[2]
	free := ""
	if len(parts) >= 4 {
		free = parts[3]
	}

	// 计算使用率
	cmd = "free | grep Mem | awk '{printf \"%.1f\", $3/$2 * 100}'"
	percentOutput, _ := c.RunCommand(cmd)
	usagePercent := strings.TrimSpace(percentOutput) + "%"

	return &MemoryInfo{
		Total:        total,
		Used:         used,
		Free:         free,
		UsagePercent: usagePercent,
	}, nil
}

// GetDiskInfo 获取磁盘信息（过滤远程挂载点）
func (c *SSHClient) GetDiskInfo() ([]SSHDiskInfo, error) {
	cmd := "df -h | grep -v 'Filesystem'"
	output, err := c.RunCommand(cmd)
	if err != nil {
		return nil, err
	}

	var disks []SSHDiskInfo
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 6 {
			continue
		}

		filesystem := parts[0]

		// 过滤远程挂载点：包含 :// 或 :/ 的是 NFS/SMB 等远程挂载
		// 例如：10.12.4.26:/data/backup、nas:/share、//server/share
		if strings.Contains(filesystem, ":") || strings.HasPrefix(filesystem, "//") {
			continue
		}

		disk := SSHDiskInfo{
			Filesystem: filesystem,
			Size:       parts[1],
			Used:       parts[2],
			Available:  parts[3],
			UsePercent: parts[4],
			MountedOn:  parts[5],
		}
		disks = append(disks, disk)
	}

	return disks, nil
}

// GetNetworkIO 获取网络IO
func (c *SSHClient) GetNetworkIO() (*NetworkIOInfo, error) {
	// 获取网卡名称
	cmd := "cat /proc/net/dev | grep -E '^[ ]*eth|^[ ]*ens|^[ ]*enp' | head -1 | awk -F: '{print $1}' | tr -d ' '"
	iface, err := c.RunCommand(cmd)
	if err != nil || strings.TrimSpace(iface) == "" {
		// 尝试其他方式获取网卡
		cmd = "ip route | grep default | awk '{print $5}' | head -1"
		iface, err = c.RunCommand(cmd)
		if err != nil {
			return nil, err
		}
	}

	iface = strings.TrimSpace(iface)
	if iface == "" {
		return &NetworkIOInfo{RX: "N/A", TX: "N/A", RXSpeed: "N/A", TXSpeed: "N/A"}, nil
	}

	// 获取RX和TX字节数（总量）
	cmd = fmt.Sprintf("cat /proc/net/dev | grep '%s' | awk -F: '{print $2}' | awk '{print $1, $9}'", iface)
	output, err := c.RunCommand(cmd)
	if err != nil {
		return nil, err
	}

	parts := strings.Fields(output)
	if len(parts) < 2 {
		return &NetworkIOInfo{RX: "N/A", TX: "N/A", RXSpeed: "N/A", TXSpeed: "N/A"}, nil
	}

	rxBytes := parseBytes(parts[0])
	txBytes := parseBytes(parts[1])

	// 获取瞬时流量（使用sar命令）
	rxSpeed, txSpeed := c.getNetworkSpeed(iface)

	return &NetworkIOInfo{
		RX:      rxBytes,
		TX:      txBytes,
		RXSpeed: rxSpeed,
		TXSpeed: txSpeed,
	}, nil
}

// getNetworkSpeed 获取网络瞬时速度
func (c *SSHClient) getNetworkSpeed(iface string) (string, string) {
	// 使用sar命令获取瞬时流量（参考Python实现）
	// sar -n DEV 1 1: 采样1秒，采样1次
	cmd := "sar -n DEV 1 1 | grep -v IFACE | grep -v Average | grep -v '^$' | grep -v 'Linux'"
	output, err := c.RunCommand(cmd)
	if err != nil {
		return "N/A", "N/A"
	}

	lines := strings.Split(output, "\n")
	var totalRxKB, totalTxKB float64
	var hasData bool

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Linux") {
			continue
		}

		parts := strings.Fields(line)
		// 确保至少有8列（时间 AM/PM 网卡 rxpck/s txpck/s rxkB/s txkB/s ...）
		if len(parts) < 8 {
			continue
		}

		// 获取网卡名称（第3列，因为第1列是时间，第2列是AM/PM）
		currentIface := parts[2]

		// 只取 ens* 或 eth* 开头的物理网卡
		if !strings.HasPrefix(currentIface, "ens") && !strings.HasPrefix(currentIface, "eth") {
			continue
		}

		// 解析rxkB/s和txkB/s（第6和第7列）
		// sar输出格式: 时间 AM/PM 网卡 rxpck/s txpck/s rxkB/s txkB/s ...
		rxKB, err1 := strconv.ParseFloat(parts[5], 64)
		txKB, err2 := strconv.ParseFloat(parts[6], 64)

		if err1 == nil && err2 == nil {
			totalRxKB += rxKB
			totalTxKB += txKB
			hasData = true
			log.Printf("网卡 %s: RX=%.2f KB/s, TX=%.2f KB/s", currentIface, rxKB, txKB)
		}
	}

	if hasData {
		return fmt.Sprintf("%.2f KB/s", totalRxKB), fmt.Sprintf("%.2f KB/s", totalTxKB)
	}

	return "N/A", "N/A"
}

// CheckServiceStatus 检查服务状态
func (c *SSHClient) CheckServiceStatus(serviceName string) string {
	// 针对 oracle 特殊处理
	if serviceName == "oracle" {
		return c.checkOracleStatus()
	}

	// 针对不同服务的检查命令
	var cmd string
	switch serviceName {
	case "firewalld":
		cmd = "systemctl is-active firewalld 2>/dev/null || echo 'inactive'"
	case "tomcat":
		// 使用更可靠的方式：直接检查进程，因为 tomcat 可能以不同用户运行
		cmd = "ps -ef | grep -i tomcat | grep -v grep > /dev/null && echo 'active' || echo 'inactive'"
	case "nginx":
		// 直接检查进程，因为 nginx 可能不是通过 systemctl 启动的
		// 使用 { ...; } 包裹确保返回码为 0
		cmd = "{ ps -ef | grep '[n]ginx' > /dev/null && echo 'active' || echo 'inactive'; }"
	case "redis":
		// 先检查 systemctl，如果没有再检查 redis-server 进程
		cmd = "{ systemctl is-active redis 2>/dev/null | grep -q '^active' || systemctl is-active redis-server 2>/dev/null | grep -q '^active' || ps -ef | grep '[r]edis-server' > /dev/null; } && echo 'active' || echo 'inactive'"
	case "nfs":
		// 先检查 systemctl，如果没有再检查 nfsd 进程
		cmd = "{ systemctl is-active nfs 2>/dev/null | grep -q '^active' || systemctl is-active nfs-server 2>/dev/null | grep -q '^active' || ps -ef | grep '[n]fsd' > /dev/null; } && echo 'active' || echo 'inactive'"
	case "mysql", "mariadb":
		cmd = "systemctl is-active mysql 2>/dev/null || systemctl is-active mariadb 2>/dev/null || systemctl is-active mysqld 2>/dev/null || echo 'inactive'"
	case "minio":
		cmd = "systemctl is-active minio 2>/dev/null || ps -ef | grep -i minio | grep -v grep > /dev/null && echo 'active' || echo 'inactive'"
	case "rabbitmq":
		cmd = "systemctl is-active rabbitmq-server 2>/dev/null || echo 'inactive'"
	case "java":
		// 检测 Java 进程（用于 Spring Boot 等 Java 应用）
		cmd = "{ ps -ef | grep '[j]ava' > /dev/null; } && echo 'active' || echo 'inactive'"
	case "kingbase":
		// 检测人大金仓数据库进程
		cmd = "{ systemctl is-active kingbase8d 2>/dev/null | grep -q '^active' || systemctl is-active kingbase 2>/dev/null | grep -q '^active' || ps -ef | grep '[k]ingbase' > /dev/null; } && echo 'active' || echo 'inactive'"
	default:
		cmd = fmt.Sprintf("systemctl is-active %s 2>/dev/null || echo 'unknown'", serviceName)
	}

	output, err := c.RunCommand(cmd)
	// 即使命令返回错误，也尝试读取输出
	status := strings.TrimSpace(output)
	if err != nil && status == "" {
		return "已停止"
	}
	// 只取第一行，避免多余输出
	lines := strings.Split(status, "\n")
	if len(lines) > 0 {
		status = strings.TrimSpace(lines[0])
	}

	if status == "active" {
		return "运行中"
	} else if status == "inactive" || status == "unknown" || status == "" {
		return "已停止"
	}
	return status
}

// checkOracleStatus 检查 Oracle 服务状态，返回详细实例信息
func (c *SSHClient) checkOracleStatus() string {
	// 先检查 oracle 用户是否存在（使用 grep 检查 /etc/passwd 更可靠）
	checkUserCmd := "grep -q '^oracle:' /etc/passwd 2>/dev/null && echo 'exists' || echo 'notfound'"
	userOutput, err := c.RunCommand(checkUserCmd)

	if err != nil {
		// 如果命令失败，尝试 id 命令作为备选
		checkUserCmd = "id oracle 2>/dev/null && echo 'exists' || echo 'notfound'"
		userOutput, err = c.RunCommand(checkUserCmd)
		if err != nil {
			return "未安装"
		}
	}

	if strings.TrimSpace(userOutput) != "exists" {
		return "未安装"
	}

	// 检查 lsnrctl 状态
	cmd := "su - oracle -c 'lsnrctl status' 2>/dev/null || echo 'LSNRCTL_FAILED'"
	output, err := c.RunCommand(cmd)
	if err != nil || strings.Contains(output, "LSNRCTL_FAILED") {
		// 用户存在但 lsnrctl 失败，可能是 oracle 已安装但未启动
		return "已停止"
	}

	// 解析 lsnrctl status 输出
	return parseOracleListenerStatus(output)
}

// parseOracleListenerStatus 解析 lsnrctl status 输出，提取服务实例信息
func parseOracleListenerStatus(output string) string {
	var services []string
	lines := strings.Split(output, "\n")

	for i, line := range lines {
		line = strings.TrimSpace(line)
		// 匹配 Service "xxx" has 1 instance(s).
		// 格式: Service "orcl" has 1 instance(s).
		if strings.HasPrefix(line, `Service "`) && strings.Contains(line, `" has`) {
			// 提取服务名 - 找到第一个引号和第二个引号之间的内容
			firstQuote := strings.Index(line, `"`)
			if firstQuote != -1 {
				secondQuote := strings.Index(line[firstQuote+1:], `"`)
				if secondQuote != -1 {
					serviceName := line[firstQuote+1 : firstQuote+1+secondQuote]
					// 查找下一行的 Instance 状态
					if i+1 < len(lines) {
						nextLine := strings.TrimSpace(lines[i+1])
						// 格式: Instance "orcl", status UNKNOWN, has 1 handler(s) for this service...
						if strings.Contains(nextLine, "Instance") && strings.Contains(nextLine, "status") {
							// 提取状态 - 找到 "status " 后面的值
							statusIdx := strings.Index(nextLine, "status ")
							if statusIdx != -1 {
								statusPart := nextLine[statusIdx+7:] // 跳过 "status "
								// 取状态值（到逗号或行尾）
								commaIdx := strings.Index(statusPart, ",")
								if commaIdx != -1 {
									statusPart = statusPart[:commaIdx]
								}
								services = append(services, serviceName+"::"+strings.TrimSpace(statusPart))
							}
						}
					}
				}
			}
		}
	}

	if len(services) == 0 {
		return ""
	}
	// 多个实例用 | 分隔，前端会拆分成多行显示
	return "|" + strings.Join(services, "|")
}

// parseBytes 将字节数转换为可读格式
func parseBytes(bytesStr string) string {
	var bytes float64
	fmt.Sscanf(bytesStr, "%f", &bytes)

	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
		TB = GB * 1024
	)

	switch {
	case bytes >= TB:
		return fmt.Sprintf("%.2f TB", bytes/TB)
	case bytes >= GB:
		return fmt.Sprintf("%.2f GB", bytes/GB)
	case bytes >= MB:
		return fmt.Sprintf("%.2f MB", bytes/MB)
	case bytes >= KB:
		return fmt.Sprintf("%.2f KB", bytes/KB)
	default:
		return fmt.Sprintf("%.0f B", bytes)
	}
}

// GetAllSystemInfo 获取所有系统信息
func (c *SSHClient) GetAllSystemInfo() (*SystemInfo, error) {
	type result struct {
		info *SystemInfo
		err  error
	}

	done := make(chan result, 1)

	go func() {
		cpu, err := c.GetCPUInfo()
		if err != nil {
			cpu = &CPUInfo{Usage: "N/A"}
		}

		memory, err := c.GetMemoryInfo()
		if err != nil {
			memory = &MemoryInfo{Total: "N/A", Used: "N/A", Free: "N/A", UsagePercent: "N/A"}
		}

		disks, err := c.GetDiskInfo()
		if err != nil {
			disks = []SSHDiskInfo{}
		}

		netIO, err := c.GetNetworkIO()
		if err != nil {
			netIO = &NetworkIOInfo{RX: "N/A", TX: "N/A"}
		}

		done <- result{
			info: &SystemInfo{
				CPU:       *cpu,
				Memory:    *memory,
				Disks:     disks,
				NetworkIO: *netIO,
			},
			err: nil,
		}
	}()

	select {
	case res := <-done:
		return res.info, res.err
	case <-time.After(60 * time.Second):
		return &SystemInfo{
			CPU:       CPUInfo{Usage: "超时"},
			Memory:    MemoryInfo{Total: "超时", Used: "超时", Free: "超时", UsagePercent: "超时"},
			Disks:     []SSHDiskInfo{},
			NetworkIO: NetworkIOInfo{RX: "超时", TX: "超时"},
		}, fmt.Errorf("get system info timeout")
	}
}
