# 服务器监测系统 (server-monitor)

## 项目概述

本项目是一个基于 Go 语言开发的服务器监控 Web 应用。它通过 SSH 协议连接到远程 Linux 服务器，采集 CPU、内存、磁盘、网络 IO 以及指定服务的运行状态，并通过 Web 仪表盘进行展示。系统支持多用户管理、基于角色的访问控制、登录安全保护、定时自动巡检以及 CSV 数据导出功能。

**核心功能：**
- 通过 SSH 批量采集服务器系统指标（CPU、内存、磁盘、网络）。
- 监控常用服务状态（如 nginx、tomcat、mysql、redis、oracle、java 进程等）。
- Web 仪表盘实时展示所有服务器状态，支持单个服务器详情页与历史趋势图表。
- 基于角色的用户管理（admin / user / guest）。
- 定时任务：每小时整点自动全量巡检，每天 17:00 自动导出 CSV 报表。
- 手动触发全量检查与即时 CSV 导出。

## 技术栈

- **语言与运行时：** Go 1.25.0
- **Web 框架：** 标准库 `net/http`（无外部 Web 框架）
- **数据库：** SQLite（通过 `modernc.org/sqlite` 纯 Go 驱动）
- **SSH 客户端：** `golang.org/x/crypto/ssh`
- **密码安全：** `golang.org/x/crypto/bcrypt`（用户密码），AES-256-GCM（服务器密码）
- **前端：** 纯 HTML/CSS/JavaScript（模板引擎为 Go 标准库 `html/template`），详情页使用 Chart.js (CDN) 绘制趋势图。

## 项目结构

所有 Go 源码位于项目根目录，采用单包结构（`package main`）。

```
.
├── go.mod              # Go 模块定义
├── go.sum              # 依赖校验
├── main.go             # 程序入口、HTTP 路由注册、session 管理、内存缓存
├── db.go               # SQLite 数据库初始化、表结构、CRUD、数据迁移
├── handlers.go         # HTTP 请求处理器（页面渲染与 REST API）
├── middleware.go       # 认证中间件、管理员权限中间件、请求日志
├── ssh.go              # SSH 客户端封装、远程命令执行、系统信息采集
├── monitor.go          # 服务器状态检查调度与结果持久化
├── scheduler.go        # 定时任务配置与执行逻辑
├── crypto.go           # 密码哈希（bcrypt）、AES-GCM 加解密
├── template.go         # HTML 模板预编译与渲染辅助函数
├── utils.go            # 字符串解析与数据转换工具函数
├── templates/          # HTML 模板文件
│   ├── login.html
│   ├── dashboard.html
│   ├── server_detail.html
│   └── users.html
├── export/             # 定时任务 CSV 导出目录（运行时创建）
└── monitor.db          # SQLite 数据库文件（运行时创建）
```

## 构建与运行

### 前置要求

- 安装 Go 1.25.0 或更高版本。
- 确保 Go 模块代理可访问（用于下载 `modernc.org/sqlite` 等依赖）。

### 本地开发运行

```bash
go run .
```

或先构建再运行：

```bash
go build -o server-monitor.exe .
./server-monitor.exe
```

### 访问应用

启动后，服务监听 `http://localhost:18080`。

- 默认管理员账号：`admin`
- 默认管理员密码：`admin123`

首次启动时会自动创建 SQLite 数据库 `monitor.db`，并插入默认管理员用户。

### 生产构建

项目不依赖 CGO（使用纯 Go 的 SQLite 驱动），可直接交叉编译：

```bash
# Windows
go build -o server-monitor.exe .

# Linux
go build -o server-monitor .
```

## 代码风格与约定

- **语言：** 源码中的注释、变量命名、日志输出均以中文为主。
- **包结构：** 当前为单包应用（`package main`），所有 `.go` 文件位于根目录，未按子包拆分。
- **错误处理：** 采用 Go 惯用的 `if err != nil` 模式，关键错误会记录日志（`log.Printf`）。
- **并发：**
  - 使用 `sync.RWMutex` 保护内存中的 session、状态缓存和检查锁。
  - 使用 `sync.Mutex` 保护 SQLite 写入操作（`dbWriteLock`）。
  - 每台服务器有独立的检查锁（`checkingLocks`），防止并发重复检查同一服务器。
  - SSH 检查与定时任务均在独立的 goroutine 中执行。
- **数据库访问：** 使用标准库 `database/sql` 配合预编译的 SQL 语句（部分地方使用字符串拼接实现 `IN` 查询）。

## 主要模块说明

### 1. 数据库层 (`db.go`)

- **数据库：** SQLite，启用 WAL 模式（`_journal=WAL`），连接池限制为 1 个连接。
- **表结构：**
  - `users`：系统用户（密码使用 bcrypt 哈希）。
  - `servers`：被监控的服务器列表（SSH 密码使用 AES 加密存储）。
  - `server_status`：每次检查的主状态记录（CPU、内存、网络）。
  - `disk_info`：磁盘详情（一对多关联 `server_status`）。
  - `service_status`：服务状态详情（一对多关联 `server_status`）。
  - `login_attempts`：登录失败记录（用于登录保护）。
- **数据清理：** `server_status` 表记录数超过 4096 条时，自动删除最早的 2048 条。
- **迁移：** 启动时自动检测旧版表结构（存在 `cpu`、`memory` 等旧字段时），将旧表重命名为 `server_status_old` 并重建新表。

### 2. SSH 采集层 (`ssh.go`)

- 使用密码认证连接远程 Linux 服务器，`HostKeyCallback` 设置为 `ssh.InsecureIgnoreHostKey()`（**生产环境应注意此安全风险**）。
- 命令执行默认超时 15 秒，整体系统信息采集超时 30 秒。
- 采集命令依赖目标服务器的标准 Linux 工具：`top`、`free`、`df`、`sar`、`systemctl`、`ps` 等。
- **磁盘采集：** 自动过滤远程挂载点（包含 `:` 或 `//` 的文件系统）。
- **服务检查：** 针对常见服务（nginx、tomcat、mysql、redis、oracle、minio、rabbitmq、java、kingbase 等）有定制化的进程检测命令。
- **Oracle 特殊处理：** 检查 `oracle` 用户是否存在，执行 `lsnrctl status` 并解析监听器中的服务实例与状态。

### 3. Web 层 (`handlers.go` + `middleware.go`)

- **认证：** 基于 Cookie 的 session（内存存储），24 小时过期，每小时清理一次过期 session。
- **登录保护：** 15 分钟内同一用户名或 IP 失败 5 次即锁定 5 分钟。
- **权限角色：**
  - `admin`：可增删改服务器、管理用户、配置定时任务。
  - `user` / `guest`：可查看仪表盘和服务器详情，可修改自己的密码。
- **API 风格：** 大部分成功响应直接返回字符串 `"OK"`，错误返回 `http.Error`。
- **模板函数：** 预定义了 `safeHTML`、`progressBar`、`serviceTag`、`formatBytes` 等辅助函数用于前端渲染。

### 4. 定时任务 (`scheduler.go`)

- 默认每小时整点触发一次全量服务器检查。
- 仅在每天 17:00 的巡检任务中自动导出 CSV 到 `export/` 目录。
- 支持通过 API (`/api/schedule/config`, `/api/schedule/trigger`) 查看配置、修改间隔/开关、手动触发。

### 5. 安全 (`crypto.go`)

- **用户密码：** `bcrypt.DefaultCost` 加盐哈希。
- **服务器密码：** AES-256-GCM 加密，硬编码密钥为 `server-monitor-encryption-key-32`。**注意：** 生产环境部署时建议通过环境变量或配置文件注入密钥，避免硬编码。

## 测试说明

当前代码库中**没有包含任何单元测试或集成测试文件**。如果需要添加测试：

- 由于项目使用单包结构，测试文件可直接放在根目录，命名为 `*_test.go`。
- 数据库相关逻辑可使用内存 SQLite（`:memory:`）进行隔离测试。
- SSH 相关逻辑建议抽象接口后进行 mock 测试。

## 部署注意事项

1. **数据库文件：** `monitor.db` 为运行时生成的 SQLite 文件，部署时应确保应用对该文件及其所在目录有读写权限。
2. **导出目录：** `export/` 目录会在首次启动定时任务时自动创建，同样需要写权限。
3. **静态文件：** 代码中注册了 `/static/` 路由，但当前项目目录下无 `static/` 文件夹，如需添加前端静态资源可直接创建该目录。
4. **加密密钥：** 修改 `crypto.go` 中的 `encryptionKey` 以更换 AES 加密密钥。**注意：** 更换密钥后，已加密存储在数据库中的服务器密码将无法解密。
5. **端口：** 默认监听 `18080`，如需修改请编辑 `main.go` 中的 `http.ListenAndServe` 调用。
6. **目标服务器要求：** 被监控的 Linux 服务器需要开启 SSH 服务，且账号具有执行系统监控命令的权限。

## 安全考量

- 应用内部使用内存 session，重启后所有用户需要重新登录。
- SSH 连接忽略主机密钥校验，存在中间人攻击风险，建议在受信任的内网环境中使用。
- 管理员可查看/修改所有服务器的连接密码（前端传输为加密形式，后端解密使用）。
- 登录接口有基本的防暴力破解保护（失败次数锁定）。
