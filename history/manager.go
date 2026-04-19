package history

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
		baseDir = filepath.Join(home, ".cmd_history")
	}

	// 创建基础目录
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("创建历史目录失败: %v", err)
	}

	return &Manager{
		baseDir: baseDir,
	}, nil
}

// SetHost 设置当前主机
func (m *Manager) SetHost(host string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.currentHost = host
	if host != "" {
		hostDir := filepath.Join(m.baseDir, host)
		os.MkdirAll(hostDir, 0755)
		m.historyFile = filepath.Join(hostDir, "history.txt")
	} else {
		m.historyFile = filepath.Join(m.baseDir, "local_history.txt")
	}
}

// SaveCommand 保存命令到历史
func (m *Manager) SaveCommand(command string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.historyFile == "" {
		m.historyFile = filepath.Join(m.baseDir, "local_history.txt")
	}

	// 不保存空命令和重复命令
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}

	// 检查是否与最后一条重复
	if lastCmd, _ := m.getLastCommand(); lastCmd == command {
		return nil
	}

	file, err := os.OpenFile(m.historyFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("打开历史文件失败: %v", err)
	}
	defer file.Close()

	_, err = fmt.Fprintln(file, command)
	return err
}

// getLastCommand 获取最后一条命令
func (m *Manager) getLastCommand() (string, error) {
	file, err := os.Open(m.historyFile)
	if err != nil {
		return "", err
	}
	defer file.Close()

	var lastLine string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lastLine = scanner.Text()
	}
	return lastLine, scanner.Err()
}

// LoadHistory 加载历史命令，返回历史命令列表
func (m *Manager) LoadHistory() ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.historyFile == "" {
		return nil, nil
	}

	file, err := os.Open(m.historyFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()

	var commands []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		cmd := strings.TrimSpace(scanner.Text())
		if cmd != "" {
			commands = append(commands, cmd)
		}
	}
	return commands, scanner.Err()
}

// GetHistoryFile 获取当前历史文件路径
func (m *Manager) GetHistoryFile() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.historyFile
}

// GetHostHistoryDir 获取指定主机的历史目录
func (m *Manager) GetHostHistoryDir(host string) string {
	return filepath.Join(m.baseDir, host)
}

// GetBaseDir 获取历史基础目录
func (m *Manager) GetBaseDir() string {
	return m.baseDir
}

// SyncFromRemote 从远程同步历史命令（通过rsync）
func (m *Manager) SyncFromRemote(host string, sshConn interface{}) error {
	// 实际实现需要在transfer模块中调用rsync
	// 这里提供接口定义
	return fmt.Errorf("not implemented: use transfer.RsyncHistory instead")
}

// SyncToRemote 同步历史命令到远程
func (m *Manager) SyncToRemote(host string, sshConn interface{}) error {
	// 实际实现需要在transfer模块中调用rsync
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
