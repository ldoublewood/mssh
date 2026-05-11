# MSSH - 多机 SSH 客户端

一个用 Go 编写的多机 SSH 管理工具，支持对多台远程服务器并发执行命令、文件传输、单机交互登录以及 MCP 协议集成。

## 运行模式

MSSH 提供四种运行模式，适用于不同场景：

| 模式 | 启动方式 | 说明 |
|------|---------|------|
| **交互模式** | `mssh` | 进入 REPL 交互命令行，支持自动补全和历史搜索 |
| **命令模式** | `mssh host: command` | 单次命令执行，自动通过 daemon 复用连接 |
| **Daemon 模式** | `mssh --daemon` | 后台常驻进程，持有连接池，通过 Unix socket 响应命令 |
| **MCP 模式** | `mssh --mcp` | 通过 stdio 提供 MCP 服务，供 Claude Code 等客户端集成 |

## 功能特性

- **多机并发执行**: 对主机组同时下发命令，支持并发和顺序两种模式
- **SSH 连接池**: 复用 SSH 连接，避免重复建立连接的开销
- **连接保持 (Keepalive)**: Daemon 模式下保持连接存活，可配置空闲超时自动释放
- **交互式 REPL**: 基于 readline，支持命令编辑、历史记录、Tab 自动补全
- **本地 Shell 模拟**: 内置本地 shell 执行器，支持 cd、pwd、export、alias 等内置命令，维护工作目录和环境变量
- **单机交互登录**: 以 PTY 终端模式登录远程主机，支持 Ctrl+R 跨主机历史搜索
- **主机配置管理**: INI 格式配置文件，支持主机分组和嵌套子组
- **文件传输**: 基于 SCP 协议上传/下载文件，上传支持多机批量
- **命令历史**: 本地保存历史命令，按主机分类存储，支持 rsync/SCP 远程同步
- **多认证方式**: SSH Agent、密钥文件 (RSA/Ed25519/ECDSA)、密码配置
- **MCP 协议集成**: 实现 MCP (Model Context Protocol)，可作为 Claude Code 子代理远程管理多台服务器
- **别名支持**: 自动加载用户 .bashrc/.zshrc 中的别名定义

## 安装

```bash
cd mssh
go mod tidy
go build -o mssh .

# 可选: 安装到系统路径
sudo cp mssh /usr/local/bin/
```

## 快速开始

### 1. 创建主机配置文件 hosts.ini

```ini
[webservers]
web1 = user@192.168.1.10:22
web2 = user@192.168.1.11

[dbservers]
db1 = root@192.168.2.10
```

### 2. 创建密码配置文件（可选，免密登录可跳过）

```ini
web1 = password123
web2 = password456
db1 = dbpassword
```

### 3. 运行

```bash
# 交互模式
./mssh -c hosts.ini -p passwords.ini

# 命令模式（单次执行）
./mssh -c hosts.ini webservers: uptime

# Daemon 模式（后台常驻，自动保活）
./mssh -c hosts.ini --daemon

# MCP 模式（供 Claude Code 集成）
./mssh -c hosts.ini --mcp
```

## 使用说明

### 交互模式

```
mssh> host: command       # 在指定主机执行命令
mssh> group: command      # 在主机组的所有主机执行命令
mssh> host:               # 登录到指定主机（交互模式）
```

```bash
# 在单台主机执行命令
mssh> web1: uname -a

# 在主机组并发执行命令
mssh> webservers: systemctl status nginx

# 登录单台主机
mssh> web1:

# 上传文件到多台主机
mssh> put ./config.ini webservers:/etc/app/

# 从单台主机下载文件
mssh> get web1:/var/log/app.log ./

# 执行本地命令
mssh> ls -la
mssh> cd /tmp && pwd

# 切换执行模式
mssh> concurrent         # 并发模式（默认）
mssh> sequential         # 顺序模式
```

### 命令模式

```bash
# 单次执行，自动启动/复用 daemon 保持连接
./mssh webservers: uptime
./mssh -c hosts.ini web1: df -h

# 禁用连接保持
./mssh --no-keepalive web1: ls

# 顺序执行
./mssh -s webservers: systemctl restart nginx
```

### Daemon 模式

```bash
# 启动后台守护进程
./mssh -c hosts.ini --daemon

# 自定义空闲超时（默认 5 分钟）
./mssh -c hosts.ini --daemon --keepalive 30m

# 禁用超时（永久保持）
./mssh -c hosts.ini --daemon --keepalive 0s
```

Daemon 通过 Unix socket (`~/.mssh/daemon.sock`) 接收命令，自动管理连接池生命周期。

### MCP 模式

集成到 Claude Code 等支持 MCP 的客户端中，用于自动化远程服务器管理：

```json
{
  "mcpServers": {
    "mssh": {
      "command": "/usr/local/bin/mssh",
      "args": ["--mcp", "-c", "/path/to/hosts.ini", "-p", "/path/to/passwords.ini"]
    }
  }
}
```

**MCP 工具列表:**

| 工具 | 说明 |
|------|------|
| `ssh_execute` | 在远程主机执行命令，支持单机/组，并发返回结果 |
| `ssh_list_hosts` | 列出所有配置的主机和主机组 |
| `ssh_upload` | 上传本地文件到远程主机 |
| `ssh_download` | 从远程主机下载文件（仅支持单台） |

**MCP 资源:**
- `mssh://hosts` — 主机清单 (JSON)
- `mssh://groups` — 主机组清单 (JSON)

### 交互模式内置命令

| 命令 | 说明 |
|------|------|
| `help` | 显示帮助信息 |
| `hosts` | 列出所有主机 |
| `groups` | 列出所有组 |
| `concurrent` | 切换到并发模式 |
| `sequential` | 切换到顺序模式 |
| `exit`/`quit`/`q` | 退出程序 |

### 远程登录模式

进入远程交互 shell 后：
- 命令直接发送到远程主机执行
- `Ctrl+R` 进入历史搜索模式（跨主机搜索）
- 再次 `Ctrl+R` 切换下一个匹配
- `Ctrl+G` 或 `Ctrl+C` 取消搜索
- 回车执行选中的历史命令
- `Ctrl+D` 退出远程登录，返回本地

## 配置文件格式

### hosts.ini

```ini
# 主机组定义
[webservers]
web1 = user@192.168.1.10:22    # 指定端口
web2 = user@192.168.1.11       # 默认 22 端口

# 嵌套子组（组的成员也可以是另一个组）
[all]
subgroup = webservers
subgroup = dbservers

[dbservers]
db1 = root@192.168.2.10

# 未分组的主机
dev = dev@192.168.3.10
```

### passwords.ini

```ini
# 主机名 = 密码
web1 = password123
web2 = password456
```

## 命令行参数

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-c` | `hosts.ini` | 主机配置文件路径 |
| `-p` | `passwords.ini` | 密码配置文件路径 |
| `-s` | `false` | 使用顺序模式（默认并发） |
| `--daemon` | `false` | 以 daemon 模式运行 |
| `--mcp` | `false` | 以 MCP 服务器模式运行 |
| `--keepalive` | `5m` | 连接保持时长，`0s` 禁用超时 |
| `--no-keepalive` | `false` | 禁用连接保持（命令模式） |

## 认证方式

SSH 认证按以下优先级尝试：
1. SSH Agent (`SSH_AUTH_SOCK`)
2. 密钥文件 (`~/.ssh/id_rsa`, `id_ed25519`, `id_ecdsa`)
3. 密码配置（passwords.ini）

首次连接自动接受主机密钥（生产环境建议配置 known_hosts）。

## 历史命令存储

```
~/.mssh_history/
├── local_history.txt          # 本地命令历史
├── web1/                      # 远程主机历史
│   └── .bash_history
├── web2/
│   └── .zsh_history
└── logs/                      # 同步日志
    └── web1_sync.log
```

远程登录时自动通过 rsync/SCP 增量同步远程主机的 shell 历史文件。

## 目录结构

```
mssh/
├── main.go              # 程序入口，模式分发
├── config/
│   └── hosts.go         # 主机/组配置解析（支持嵌套组）
├── ssh/
│   └── pool.go          # SSH 连接池、交互 shell、历史搜索
├── command/
│   └── executor.go      # 命令解析与执行器
├── shell/
│   └── local.go         # 本地 shell 模拟（cd/export/alias 等）
├── transfer/
│   └── scp.go           # SCP 文件传输
├── history/
│   ├── manager.go       # 历史命令管理
│   └── rsync.go         # rsync/SCP 历史同步
├── mcp/
│   ├── protocol.go      # JSON-RPC 2.0 协议类型定义
│   ├── server.go        # MCP Server (stdio 通信)
│   └── tools.go         # MCP 工具实现
├── internal/daemon/
│   ├── daemon.go        # 共享类型与常量
│   ├── server.go        # Daemon 服务端（Unix socket）
│   └── client.go        # Daemon 客户端
├── go.mod
├── hosts.ini.example
└── passwords.ini.example
```

## 依赖

- `github.com/chzyer/readline` — 命令行交互与自动补全
- `golang.org/x/crypto/ssh` — SSH 客户端
- `golang.org/x/term` — 终端控制

## License

MIT
