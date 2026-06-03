package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"time"
)

// IsRunning 检查 daemon 是否在运行
func IsRunning() bool {
	conn, err := net.DialTimeout("unix", SocketPath(), 100*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// SendCommand 通过 daemon 执行命令，返回输出
func SendCommand(hostsFile, passwordsFile, command string, sequential bool, idleTimeout time.Duration) (*Response, error) {
	if IsRunning() {
		if !CheckConfig(hostsFile, passwordsFile) {
			StopDaemon()
			time.Sleep(200 * time.Millisecond)
		}
	}

	if !IsRunning() {
		if err := StartDaemon(hostsFile, passwordsFile, sequential, idleTimeout); err != nil {
			return nil, fmt.Errorf("启动daemon失败: %v", err)
		}
	}

	return sendRequest(command, hostsFile, passwordsFile, sequential)
}

// sendRequest 发送请求到 daemon 并获取响应
func sendRequest(command, hostsFile, passwordsFile string, sequential bool) (*Response, error) {
	conn, err := net.DialTimeout("unix", SocketPath(), 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("连接daemon失败: %v", err)
	}
	defer conn.Close()

	req := Request{
		Command:       command,
		HostsFile:     hostsFile,
		PasswordsFile: passwordsFile,
		Sequential:    sequential,
	}

	if err := json.NewEncoder(conn).Encode(&req); err != nil {
		return nil, fmt.Errorf("发送请求失败: %v", err)
	}

	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		if err == io.EOF {
			return nil, fmt.Errorf("daemon连接意外关闭（命令执行时间可能超过了keepalive超时，尝试增大 --keepalive 参数，如 --keepalive 30m）")
		}
		return nil, fmt.Errorf("读取响应失败: %v", err)
	}

	return &resp, nil
}

// StartDaemon 在后台启动 daemon 进程
func StartDaemon(hostsFile, passwordsFile string, sequential bool, idleTimeout time.Duration) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("获取可执行文件路径失败: %v", err)
	}

	args := []string{
		"--daemon",
		"-c", hostsFile,
		"-p", passwordsFile,
		"--keepalive", idleTimeout.String(),
	}
	if sequential {
		args = append(args, "-s")
	}

	cmd := exec.Command(exe, args...)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动后台进程失败: %v", err)
	}
	cmd.Process.Release()

	for i := 0; i < 50; i++ {
		time.Sleep(100 * time.Millisecond)
		if IsRunning() {
			return nil
		}
	}

	return fmt.Errorf("等待daemon启动超时")
}

// StopDaemon 停止正在运行的 daemon
func StopDaemon() {
	conn, err := net.DialTimeout("unix", SocketPath(), time.Second)
	if err != nil {
		return
	}
	defer conn.Close()

	req := Request{Command: "__STOP__"}
	json.NewEncoder(conn).Encode(&req)
	conn.Close()
}
