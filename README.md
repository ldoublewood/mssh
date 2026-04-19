# MSSH - 多机SSH客户端

一个支持多机操作的SSH客户端，使用Go语言开发，可以同时对多台远程服务器执行命令。

## 功能特性

- **多机并发操作**: 对主机组同时下发命令，支持并发和顺序两种模式
- **SSH连接池**: 复用SSH连接，避免每次操作重新建立连接
- **交互式命令行**: 使用readline库，支持命令编辑和历史记录
- **主机配置管理**: INI格式配置文件，支持主机分组和嵌套组
- **文件传输**: 支持SCP协议上传下载文件
- **单主机登录**: 支持交互式登录单台主机
- **历史命令**: 本地保存历史命令，按主机分类存储
- **免密登录支持**: 默认使用SSH密钥认证，同时支持密码配置

## 安装

```bash
# 克隆或下载代码
cd mssh

# 下载依赖
go mod tidy

# 编译
go build -o mssh .

# 可选: 安装到系统
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

### 2. 创建密码配置文件（可选，免密登录可不创建）

```ini
web1 = password123
web2 = password456
db1 = dbpassword
```

### 3. 运行程序

```bash
./mssh -c hosts.ini -p passwords.ini
```

## 使用说明

### 基本命令格式

```
mssh> host: command       # 在指定主机执行命令
mssh> group: command      # 在主机组的所有主机执行命令
mssh> host:               # 登录到指定主机（交互模式）
```

### 示例

```bash
# 在单台主机执行命令
mssh> web1: uname -a

# 在主机组执行命令（并发执行）
mssh> webservers: systemctl status nginx

# 登录单台主机
mssh> web1:

# 上传文件到多台主机
mssh> put ./config.ini webservers:/etc/app/

# 从单台主机下载文件
mssh> get web1:/var/log/app.log ./

# 切换执行模式
mssh> concurrent    # 并发模式（默认）
mssh> sequential    # 顺序模式
```

### 内置命令

| 命令 | 说明 |
|------|------|
| `help` | 显示帮助信息 |
| `hosts` | 列出所有主机 |
| `groups` | 列出所有组 |
| `concurrent` | 切换到并发模式 |
| `sequential` | 切换到顺序模式 |
| `exit/quit` | 退出程序 |

## 配置文件格式

### hosts.ini

```ini
# 主机组定义
[webservers]
web1 = user@192.168.1.10:22    # 指定端口
web2 = user@192.168.1.11       # 默认22端口

# 可以定义多个组
[dbservers]
db1 = root@192.168.2.10

# 未分组的主机
dev = dev@192.168.3.10
```

### 密码配置 passwords.ini

```ini
# 主机名 = 密码
web1 = password123
web2 = password456
```

## 目录结构

```
mssh/
├── main.go              # 程序入口
├── config/
│   └── hosts.go         # 主机配置解析
├── ssh/
│   └── pool.go          # SSH连接池
├── command/
│   └── executor.go      # 命令执行器
├── transfer/
│   └── scp.go           # SCP文件传输
├── history/
│   └── manager.go       # 历史命令管理
├── go.mod
├── hosts.ini.example    # 主机配置示例
└── passwords.ini.example # 密码配置示例
```

## 历史命令存储

历史命令默认存储在 `~/.mssh_history/` 目录下:
- `local_history.txt` - 本地命令历史
- `[主机名]/history.txt` - 各主机的远程命令历史

## 注意事项

1. 首次连接时会自动接受主机密钥（生产环境建议配置known_hosts）
2. 并发模式下命令会同时下发到所有主机
3. 下载操作仅支持单台主机，防止文件覆盖
4. 连接池会自动复用已建立的SSH连接

## 依赖

- `golang.org/x/crypto/ssh` - SSH客户端
- `github.com/chzyer/readline` - 命令行交互
- `golang.org/x/term` - 终端控制

## License

MIT
