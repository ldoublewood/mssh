# MSSH - Multi-Host SSH Client

A Go-powered multi-host SSH management tool supporting concurrent command execution across remote servers, file transfer, single-host interactive login, and MCP protocol integration.

## Operating Modes

MSSH provides four operating modes for different scenarios:

| Mode | Launch | Description |
|------|--------|-------------|
| **Interactive** | `mssh` | REPL interactive shell with auto-completion and history search |
| **Command** | `mssh host: command` | One-shot command execution, auto-reuses daemon connections |
| **Daemon** | `mssh --daemon` | Background process holding the connection pool, serves commands via Unix socket |
| **MCP** | `mssh --mcp` | MCP server over stdio, for integration with Claude Code and other clients |

## Features

- **Multi-host concurrent execution**: Dispatch commands to host groups simultaneously, with concurrent and sequential modes
- **SSH connection pool**: Reuse SSH connections to avoid repeated handshake overhead
- **Connection keepalive**: Daemon mode keeps connections alive with configurable idle timeout
- **Interactive REPL**: Readline-based command line with editing, history, and Tab auto-completion
- **Local shell emulation**: Built-in shell executor supporting cd, pwd, export, alias, maintaining working directory and environment variables
- **Single-host interactive login**: PTY terminal mode login with cross-host Ctrl+R history search
- **Host configuration management**: INI-format config files with host groups and nested subgroups
- **File transfer**: SCP-based upload/download, multi-host batch upload supported
- **Command history**: Local history storage organized by host, with rsync/SCP remote sync
- **Multiple auth methods**: SSH Agent, key files (RSA/Ed25519/ECDSA), password config
- **MCP protocol integration**: Implements MCP (Model Context Protocol) for use as a Claude Code sub-agent managing remote servers
- **Alias support**: Auto-loads aliases from user's .bashrc/.zshrc

## Installation

```bash
cd mssh
go mod tidy
go build -o mssh .

# Optional: install to system path
sudo cp mssh /usr/local/bin/
```

## Quick Start

### 1. Create hosts config file hosts.ini

```ini
[webservers]
web1 = user@192.168.1.10:22
web2 = user@192.168.1.11

[dbservers]
db1 = root@192.168.2.10
```

### 2. Create password config file (optional, skip for key-based auth)

```ini
web1 = password123
web2 = password456
db1 = dbpassword
```

### 3. Run

```bash
# Interactive mode
./mssh -c hosts.ini -p passwords.ini

# Command mode (one-shot)
./mssh -c hosts.ini webservers: uptime

# Daemon mode (background, auto keepalive)
./mssh -c hosts.ini --daemon

# MCP mode (for Claude Code integration)
./mssh -c hosts.ini --mcp
```

## Usage

### Interactive Mode

```
mssh> host: command       # Execute command on a specific host
mssh> group: command      # Execute command on all hosts in a group
mssh> host:               # Login to a specific host (interactive)
```

```bash
# Execute command on a single host
mssh> web1: uname -a

# Execute command on a host group (concurrent)
mssh> webservers: systemctl status nginx

# Login to a single host
mssh> web1:

# Upload file to multiple hosts
mssh> put ./config.ini webservers:/etc/app/

# Download file from a single host
mssh> get web1:/var/log/app.log ./

# Execute local commands
mssh> ls -la
mssh> cd /tmp && pwd

# Switch execution mode
mssh> concurrent         # Concurrent mode (default)
mssh> sequential         # Sequential mode
```

### Command Mode

```bash
# One-shot execution, auto-starts/reuses daemon for connection keepalive
./mssh webservers: uptime
./mssh -c hosts.ini web1: df -h

# Disable connection keepalive
./mssh --no-keepalive web1: ls

# Sequential execution
./mssh -s webservers: systemctl restart nginx
```

### Daemon Mode

```bash
# Start background daemon
./mssh -c hosts.ini --daemon

# Custom idle timeout (default: 5 minutes)
./mssh -c hosts.ini --daemon --keepalive 30m

# Disable timeout (persistent)
./mssh -c hosts.ini --daemon --keepalive 0s
```

The daemon listens on a Unix socket (`~/.mssh/daemon.sock`) and manages the connection pool lifecycle.

### MCP Mode

Integrate into Claude Code or any MCP-compatible client for automated remote server management:

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

**MCP Tools:**

| Tool | Description |
|------|-------------|
| `ssh_execute` | Execute command on remote hosts, supports single host or groups, concurrent results |
| `ssh_list_hosts` | List all configured hosts and host groups |
| `ssh_upload` | Upload local files to remote hosts |
| `ssh_download` | Download files from a remote host (single host only) |

**MCP Resources:**
- `mssh://hosts` — Host inventory (JSON)
- `mssh://groups` — Group inventory (JSON)

### Interactive Mode Built-in Commands

| Command | Description |
|------|-------------|
| `help` | Show help |
| `hosts` | List all hosts |
| `groups` | List all groups |
| `concurrent` | Switch to concurrent mode |
| `sequential` | Switch to sequential mode |
| `exit`/`quit`/`q` | Exit the program |

### Remote Login Mode

Once in a remote interactive shell:
- Commands are sent directly to the remote host
- `Ctrl+R` enters history search mode (cross-host search)
- Press `Ctrl+R` again to cycle through matches
- `Ctrl+G` or `Ctrl+C` cancels search
- `Enter` executes the selected history command
- `Ctrl+D` exits remote login and returns to local mode

## Configuration File Format

### hosts.ini

```ini
# Host group definition
[webservers]
web1 = user@192.168.1.10:22    # Custom port
web2 = user@192.168.1.11       # Default port 22

# Nested subgroups (a group member can also be a group)
[all]
subgroup = webservers
subgroup = dbservers

[dbservers]
db1 = root@192.168.2.10

# Ungrouped hosts
dev = dev@192.168.3.10
```

### passwords.ini

```ini
# hostname = password
web1 = password123
web2 = password456
```

## Command-Line Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-c` | `hosts.ini` | Path to hosts config file |
| `-p` | `passwords.ini` | Path to passwords config file |
| `-s` | `false` | Use sequential mode (default: concurrent) |
| `--daemon` | `false` | Run in daemon mode |
| `--mcp` | `false` | Run in MCP server mode |
| `--keepalive` | `5m` | Connection keepalive duration, `0s` disables timeout |
| `--no-keepalive` | `false` | Disable connection keepalive (command mode) |

## Authentication

SSH authentication is attempted in the following priority order:
1. SSH Agent (`SSH_AUTH_SOCK`)
2. Key files (`~/.ssh/id_rsa`, `id_ed25519`, `id_ecdsa`)
3. Password config (passwords.ini)

Host keys are auto-accepted on first connection (configure known_hosts for production use).

## Command History Storage

```
~/.mssh_history/
├── local_history.txt          # Local command history
├── web1/                      # Remote host history
│   └── .bash_history
├── web2/
│   └── .zsh_history
└── logs/                      # Sync logs
    └── web1_sync.log
```

Remote shell history files are incrementally synced via rsync/SCP during remote login sessions.

## Directory Structure

```
mssh/
├── main.go              # Entry point, mode dispatch
├── config/
│   └── hosts.go         # Host/group config parser (supports nested groups)
├── ssh/
│   └── pool.go          # SSH connection pool, interactive shell, history search
├── command/
│   └── executor.go      # Command parser and executor
├── shell/
│   └── local.go         # Local shell emulation (cd/export/alias etc.)
├── transfer/
│   └── scp.go           # SCP file transfer
├── history/
│   ├── manager.go       # Command history management
│   └── rsync.go         # rsync/SCP history sync
├── mcp/
│   ├── protocol.go      # JSON-RPC 2.0 protocol type definitions
│   ├── server.go        # MCP Server (stdio communication)
│   └── tools.go         # MCP tool implementations
├── internal/daemon/
│   ├── daemon.go        # Shared types and constants
│   ├── server.go        # Daemon server (Unix socket)
│   └── client.go        # Daemon client
├── go.mod
├── hosts.ini.example
└── passwords.ini.example
```

## Dependencies

- `github.com/chzyer/readline` — Interactive command line and auto-completion
- `golang.org/x/crypto/ssh` — SSH client
- `golang.org/x/term` — Terminal control

## License

MIT
