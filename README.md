# Server Monitor / 服务器监控

> **Vibe Coding Project** — 初始版本由 [通义灵码](https://tongyi.aliyun.com/lingma) 生成，后期由 [Kimi](https://kimi.moonshot.cn) 参与优化与迭代。

一个基于 Go 语言开发的服务器监控 Web 应用，通过 SSH 协议批量采集 Linux 服务器系统指标，并通过 Web 仪表盘实时展示。

## 功能特性

- **🚀 无 Agent 部署**：纯 SSH 远程采集，被监控服务器无需安装任何客户端或守护进程
- **系统指标采集**：CPU、内存、磁盘、网络 IO
- **服务状态监控**：nginx、tomcat、mysql、redis、oracle、java 等常用服务
- **Web 仪表盘**：实时展示所有服务器状态，支持详情页与历史趋势图表
- **用户管理**：基于角色的访问控制（admin / user / guest）
- **定时巡检**：每小时整点自动全量巡检，每日 17:00 自动导出 CSV 报表
- **安全保护**：bcrypt 密码哈希、AES-256-GCM 服务器密码加密、登录防暴力破解

## 技术栈

| 组件 | 技术 |
|------|------|
| 语言 | Go 1.25.0 |
| Web 框架 | 标准库 `net/http` |
| 数据库 | SQLite (`modernc.org/sqlite`) |
| SSH 客户端 | `golang.org/x/crypto/ssh` |
| 前端 | 纯 HTML/CSS/JS + Chart.js |

## 快速开始

### 前置要求

- Go 1.25.0+

### 运行

```bash
# 克隆仓库
git clone https://github.com/djxxxxxx/srv_x.git
cd srv_x

# 直接运行
go run .

# 或构建后运行
go build -o server-monitor .
./server-monitor
```

启动后访问 http://localhost:18080

- 默认管理员账号：`admin`
- 默认管理员密码：`admin123`

> 💡 **Agentless 设计**：本项目通过标准 SSH 协议连接目标服务器执行系统命令采集数据，被监控端无需安装任何额外软件。只需确保目标服务器已开启 SSH 服务，并在 Web 界面中配置正确的连接信息（IP、端口、用户名、密码/密钥）即可开始监控。

### ⚠️ 重要：部署须知

本项目使用 Go 标准库的 `html/template` 渲染前端页面，**`templates/` 目录必须与可执行文件位于同一目录下**。

**正确目录结构示例：**

```
/opt/srv_x/
├── server-monitor      <-- 可执行文件
├── templates/
│   ├── login.html
│   ├── dashboard.html
│   ├── server_detail.html
│   └── users.html
└── monitor.db          <-- 运行时自动生成
```

若 `templates/` 目录缺失或路径不正确，启动后会报 "template not found" 错误。

## 交叉编译

项目不依赖 CGO，可直接交叉编译：

```bash
# Linux
GOOS=linux GOARCH=amd64 go build -o server-monitor .

# Windows
GOOS=windows GOARCH=amd64 go build -o server-monitor.exe .

# macOS
GOOS=darwin GOARCH=amd64 go build -o server-monitor .
```

## 项目结构

```
.
├── main.go              # 程序入口、路由注册、session 管理
├── db.go                # SQLite 数据库操作
├── handlers.go          # HTTP 请求处理器
├── middleware.go        # 认证与权限中间件
├── ssh.go               # SSH 客户端与系统信息采集
├── monitor.go           # 状态检查调度
├── scheduler.go         # 定时任务
├── crypto.go            # 密码哈希与加密
├── template.go          # HTML 模板渲染
├── utils.go             # 工具函数
└── templates/           # HTML 模板文件（运行时必须存在）
    ├── login.html
    ├── dashboard.html
    ├── server_detail.html
    └── users.html
```

## 安全提示

- 生产环境部署时请修改 `crypto.go` 中的 `encryptionKey`
- SSH 连接当前忽略主机密钥校验，建议在内网环境使用
- 默认管理员密码请在首次登录后立即修改

## License

MIT
