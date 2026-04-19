package history

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mssh/config"
)

// RsyncSyncer rsync历史同步器
type RsyncSyncer struct {
	host           *config.Host
	localDir       string
	remoteFile     string
	interval       time.Duration
	stopCh         chan struct{}
	wg             sync.WaitGroup
	lastSyncTime   time.Time
	mu             sync.Mutex
	rsyncAvailable bool
	logger         *log.Logger
	logFile        *os.File
}

// NewRsyncSyncer 创建rsync同步器
func NewRsyncSyncer(host *config.Host, localBaseDir string) *RsyncSyncer {
	localDir := filepath.Join(localBaseDir, host.Name)
	
	// 创建日志目录
	logDir := filepath.Join(localBaseDir, "logs")
	os.MkdirAll(logDir, 0755)
	
	// 打开日志文件
	logPath := filepath.Join(logDir, fmt.Sprintf("%s_sync.log", host.Name))
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		// 如果无法创建日志文件，使用标准错误输出
		logFile = os.Stderr
	}
	
	logger := log.New(logFile, "", log.LstdFlags)
	
	return &RsyncSyncer{
		host:           host,
		localDir:       localDir,
		interval:       1 * time.Minute, // 默认1分钟
		stopCh:         make(chan struct{}),
		rsyncAvailable: checkRsyncAvailable(),
		logger:         logger,
		logFile:        logFile,
	}
}

// checkRsyncAvailable 检查rsync是否可用
func checkRsyncAvailable() bool {
	_, err := exec.LookPath("rsync")
	return err == nil
}

// SetInterval 设置同步间隔
func (r *RsyncSyncer) SetInterval(interval time.Duration) {
	r.interval = interval
}

// Start 启动定期同步
func (r *RsyncSyncer) Start() {
	// 首先检测远程历史文件路径
	r.detectRemoteHistoryFile()

	// 创建本地目录
	os.MkdirAll(r.localDir, 0755)

	// 立即执行一次同步
	r.logger.Println("[历史同步] 启动rsync同步服务...")
	r.sync()

	// 启动定期同步goroutine
	r.wg.Add(1)
	go r.syncLoop()
}

// Stop 停止同步并执行最后一次同步
func (r *RsyncSyncer) Stop() {
	close(r.stopCh)
	r.wg.Wait()

	// 执行最后一次同步（失败只记录日志，不报错）
	r.logger.Println("[历史同步] 执行最后一次同步...")
	if err := r.syncWithRetry(2); err != nil {
		r.logger.Printf("[历史同步] 最后一次同步失败（连接可能已关闭）: %v\n", err)
	}
	
	// 关闭日志文件
	if r.logFile != nil && r.logFile != os.Stderr {
		r.logFile.Close()
	}
}

// syncWithRetry 带重试的同步
func (r *RsyncSyncer) syncWithRetry(maxRetries int) error {
	if r.remoteFile == "" {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	localFile := filepath.Join(r.localDir, filepath.Base(r.remoteFile))

	var lastErr error
	for i := 0; i < maxRetries; i++ {
		if r.rsyncAvailable {
			lastErr = r.syncWithRsync(localFile)
		} else {
			lastErr = r.syncWithSCP(localFile)
		}
		
		if lastErr == nil {
			r.lastSyncTime = time.Now()
			return nil
		}
		
		if i < maxRetries-1 {
			time.Sleep(500 * time.Millisecond)
		}
	}
	
	return lastErr
}

// syncLoop 定期同步循环
func (r *RsyncSyncer) syncLoop() {
	defer r.wg.Done()

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-r.stopCh:
			return
		case <-ticker.C:
			r.sync()
		}
	}
}

// sync 执行单次同步
func (r *RsyncSyncer) sync() {
	if r.remoteFile == "" {
		r.logger.Println("[历史同步] 未检测到远程历史文件，跳过同步")
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	localFile := filepath.Join(r.localDir, filepath.Base(r.remoteFile))

	var err error
	if r.rsyncAvailable {
		err = r.syncWithRsync(localFile)
	} else {
		err = r.syncWithSCP(localFile)
	}

	if err != nil {
		r.logger.Printf("[历史同步] 同步失败: %v\n", err)
	} else {
		r.lastSyncTime = time.Now()
	}
}

// syncWithRsync 使用rsync增量同步
func (r *RsyncSyncer) syncWithRsync(localFile string) error {
	// 构建rsync命令: rsync -avz --append -e "ssh -p port" user@host:remote local
	remoteAddr := fmt.Sprintf("%s@%s:%s", r.host.User, r.host.IP, r.remoteFile)

	args := []string{
		"-avz",           // 归档模式、详细、压缩
		"--append",       // 增量追加模式（只传输新增部分）
		"-e",             // 指定ssh命令
		fmt.Sprintf("ssh -p %d -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null", r.host.Port),
		remoteAddr,
		localFile,
	}

	// 如果本地文件不存在，先创建空文件
	if _, err := os.Stat(localFile); os.IsNotExist(err) {
		os.MkdirAll(filepath.Dir(localFile), 0755)
		file, err := os.Create(localFile)
		if err != nil {
			return fmt.Errorf("创建本地文件失败: %v", err)
		}
		file.Close()
	}

	cmd := exec.Command("rsync", args...)
	// 使用上下文设置超时
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd = exec.CommandContext(ctx, "rsync", args...)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("rsync失败: %v, 输出: %s", err, string(output))
	}

	// 检查输出，如果有传输内容才记录日志
	outputStr := string(output)
	if strings.Contains(outputStr, "bytes/sec") || strings.Contains(outputStr, "speedup") {
		// 提取传输的文件名
		lines := strings.Split(outputStr, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			// 过滤出文件名行（不是以空格开头，不是统计信息）
			if line != "" && !strings.HasPrefix(line, "sent") &&
				!strings.HasPrefix(line, "total") && !strings.HasPrefix(line, "receiving") &&
				!strings.HasPrefix(line, "building") && !strings.Contains(line, "files to consider") {
				// 可能是文件路径
				if !strings.HasPrefix(line, "./") && !strings.HasPrefix(line, "/") {
					r.logger.Printf("[历史同步] 已更新: %s\n", filepath.Base(r.remoteFile))
					break
				}
			}
		}
	}

	return nil
}

// syncWithSCP 使用SCP作为rsync的备选方案
func (r *RsyncSyncer) syncWithSCP(localFile string) error {
	// 构建scp命令: scp -P port user@host:remote local
	remoteAddr := fmt.Sprintf("%s@%s:%s", r.host.User, r.host.IP, r.remoteFile)

	args := []string{
		"-P", fmt.Sprintf("%d", r.host.Port),
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		remoteAddr,
		localFile,
	}

	cmd := exec.Command("scp", args...)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd = exec.CommandContext(ctx, "scp", args...)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("scp失败: %v, 输出: %s", err, string(output))
	}

	r.logger.Printf("[历史同步] 已同步: %s (使用SCP)\n", filepath.Base(r.remoteFile))
	return nil
}

// detectRemoteHistoryFile 检测远程历史文件路径
func (r *RsyncSyncer) detectRemoteHistoryFile() {
	// 先尝试检测shell类型
	shellCmd := fmt.Sprintf("ssh -p %d -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 %s@%s 'echo $SHELL' 2>/dev/null",
		r.host.Port, r.host.User, r.host.IP)

	cmd := exec.Command("sh", "-c", shellCmd)
	output, err := cmd.Output()
	if err == nil {
		shell := strings.TrimSpace(string(output))
		if strings.Contains(shell, "zsh") {
			r.remoteFile = "~/.zsh_history"
		} else {
			r.remoteFile = "~/.bash_history"
		}
	} else {
		// 默认使用bash_history
		r.remoteFile = "~/.bash_history"
	}

	// 检测文件是否存在
	checkCmd := fmt.Sprintf("ssh -p %d -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 %s@%s 'test -f %s && echo exists' 2>/dev/null",
		r.host.Port, r.host.User, r.host.IP, r.remoteFile)

	cmd = exec.Command("sh", "-c", checkCmd)
	output, err = cmd.Output()
	if err != nil || strings.TrimSpace(string(output)) != "exists" {
		// 尝试另一个历史文件
		if r.remoteFile == "~/.bash_history" {
			r.remoteFile = "~/.zsh_history"
		} else {
			r.remoteFile = "~/.bash_history"
		}

		// 再次检测
		checkCmd = fmt.Sprintf("ssh -p %d -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 %s@%s 'test -f %s && echo exists' 2>/dev/null",
			r.host.Port, r.host.User, r.host.IP, r.remoteFile)
		cmd = exec.Command("sh", "-c", checkCmd)
		output, err = cmd.Output()
		if err != nil || strings.TrimSpace(string(output)) != "exists" {
			// 两个都不存在，使用默认的
			r.remoteFile = "~/.bash_history"
		}
	}

	r.logger.Printf("[历史同步] 检测到的远程历史文件: %s\n", r.remoteFile)
}

// GetLastSyncTime 获取上次同步时间
func (r *RsyncSyncer) GetLastSyncTime() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastSyncTime
}

// GetLocalHistoryFile 获取本地历史文件路径
func (r *RsyncSyncer) GetLocalHistoryFile() string {
	if r.remoteFile == "" {
		return filepath.Join(r.localDir, "history.txt")
	}
	return filepath.Join(r.localDir, filepath.Base(r.remoteFile))
}
