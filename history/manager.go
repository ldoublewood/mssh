package history

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Manager 管理历史命令
type Manager struct {
	baseDir     string
	historyFile string
	currentHost string
	mu          sync.Mutex
}

// NewManager 创建历史命令管理器
func NewManager(baseDir string) (*Manager, error) {
	if baseDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("获取用户目录失败: %v", err)
		}
		baseDir = filepath.Join(home, ".mssh")
	}

	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("创建历史目录失败: %v", err)
	}

	return &Manager{
		baseDir: baseDir,
	}, nil
}

// SetHostID 设置当前主机的 host_id，创建对应的 history 目录
func (m *Manager) SetHostID(hostID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.currentHost = hostID
	if hostID != "" {
		hostDir := filepath.Join(m.baseDir, hostID, "history")
		os.MkdirAll(hostDir, 0755)
		m.historyFile = filepath.Join(hostDir, "history.txt")
	} else {
		m.historyFile = filepath.Join(m.baseDir, "local_history.txt")
	}
}

// SetHost 兼容旧接口，使用 host 名作为 host_id
func (m *Manager) SetHost(host string) {
	m.SetHostID(host)
}

// SaveCommand 保存命令到历史（带文件锁防并发）
func (m *Manager) SaveCommand(command string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.historyFile == "" {
		m.historyFile = filepath.Join(m.baseDir, "local_history.txt")
	}

	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}

	f, err := os.OpenFile(m.historyFile, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("打开历史文件失败: %v", err)
	}
	defer f.Close()

	// 获取排他锁
	if err := lockFileWithTimeout(f, 2*time.Second); err != nil {
		return fmt.Errorf("获取文件锁失败: %v", err)
	}
	defer unlockFile(f)

	// 检查是否与最后一条重复
	if lastCmd, _ := readLastLine(f); lastCmd == command {
		return nil
	}

	_, err = fmt.Fprintln(f, command)
	return err
}

// LoadHistory 加载历史命令（带共享锁）
func (m *Manager) LoadHistory() ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.historyFile == "" {
		return nil, nil
	}

	f, err := os.Open(m.historyFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	if err := lockFileWithTimeout(f, 2*time.Second); err != nil {
		return nil, fmt.Errorf("获取文件锁失败: %v", err)
	}
	defer unlockFile(f)

	var commands []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		cmd := strings.TrimSpace(scanner.Text())
		if cmd != "" {
			commands = append(commands, cmd)
		}
	}
	return commands, scanner.Err()
}

// readLastLine 读取文件的最后一行（调用者需持有锁）
func readLastLine(f *os.File) (string, error) {
	var lastLine string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lastLine = scanner.Text()
	}
	return lastLine, scanner.Err()
}

// GetHistoryFile 获取当前历史文件路径
func (m *Manager) GetHistoryFile() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.historyFile
}

// GetHostHistoryDir 获取指定主机的历史目录
func (m *Manager) GetHostHistoryDir(host string) string {
	return filepath.Join(m.baseDir, host, "history")
}

// GetBaseDir 获取历史基础目录
func (m *Manager) GetBaseDir() string {
	return m.baseDir
}

// SyncFromRemote 从远程同步历史命令
func (m *Manager) SyncFromRemote(host string, sshConn interface{}) error {
	return fmt.Errorf("not implemented: use transfer.RsyncHistory instead")
}

// SyncToRemote 同步历史命令到远程
func (m *Manager) SyncToRemote(host string, sshConn interface{}) error {
	return fmt.Errorf("not implemented: use transfer.RsyncHistory instead")
}

// ListHostDirs 列出所有主机历史目录
func (m *Manager) ListHostDirs() ([]string, error) {
	entries, err := os.ReadDir(m.baseDir)
	if err != nil {
		return nil, err
	}

	var hosts []string
	for _, entry := range entries {
		if entry.IsDir() {
			hosts = append(hosts, entry.Name())
		}
	}
	return hosts, nil
}

// ClearHistory 清空当前历史
func (m *Manager) ClearHistory() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.historyFile == "" {
		return nil
	}

	return os.Remove(m.historyFile)
}

// lockFileWithTimeout 带超时的非阻塞文件锁
func lockFileWithTimeout(f *os.File, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等待文件锁超时")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// unlockFile 释放文件锁
func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
