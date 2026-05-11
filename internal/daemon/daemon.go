package daemon

import (
	"os"
	"path/filepath"
	"time"
)

const (
	DefaultSocketName  = "daemon.sock"
	DefaultIdleTimeout = 5 * time.Minute
)

// Request 发送给 daemon 的命令请求
type Request struct {
	Command       string `json:"command"`
	HostsFile     string `json:"hosts_file"`
	PasswordsFile string `json:"passwords_file"`
	Sequential    bool   `json:"sequential"`
}

// Response daemon 返回的执行结果
type Response struct {
	Output   string `json:"output"`
	Error    string `json:"error,omitempty"`
	ExitCode int    `json:"exit_code"`
}

// SocketPath 返回 daemon socket 路径
func SocketPath() string {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".mssh")
	os.MkdirAll(dir, 0700)
	return filepath.Join(dir, DefaultSocketName)
}

// PidPath 返回 daemon pid 文件路径
func PidPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".mssh", "daemon.pid")
}
