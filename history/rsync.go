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
	hostID         string
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
// hostID 用于确定本地存储路径
func NewRsyncSyncer(host *config.Host, hostID string) *RsyncSyncer {
	home, _ := os.UserHomeDir()
	msshDir := filepath.Join(home, ".mssh")

	// history 目录: ~/.mssh/<host_id>/history/
	localDir := filepath.Join(msshDir, hostID, "history")
	os.MkdirAll(localDir, 0755)

	// 日志目录: ~/.mssh/<host_id>/logs/
	logDir := filepath.Join(msshDir, hostID, "logs")
	os.MkdirAll(logDir, 0755)

	logPath := filepath.Join(logDir, "sync.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		logFile = os.Stderr
	}

	logger := log.New(logFile, "", log.LstdFlags)

	return &RsyncSyncer{
		host:           host,
		hostID:         hostID,
		localDir:       localDir,
		interval:       1 * time.Minute,
		stopCh:         make(chan struct{}),
		rsyncAvailable: checkRsyncAvailable(),
		logger:         logger,
		logFile:        logFile,
	}
}

func checkRsyncAvailable() bool {
	_, err := exec.LookPath("rsync")
	return err == nil
}

func (r *RsyncSyncer) SetInterval(interval time.Duration) {
	r.interval = interval
}

func (r *RsyncSyncer) Start() {
	r.detectRemoteHistoryFile()
	os.MkdirAll(r.localDir, 0755)
	r.logger.Println("[历史同步] 启动rsync同步服务...")
	r.sync()
	r.wg.Add(1)
	go r.syncLoop()
}

func (r *RsyncSyncer) Stop() {
	close(r.stopCh)
	r.wg.Wait()
	r.logger.Println("[历史同步] 执行最后一次同步...")
	if err := r.syncWithRetry(2); err != nil {
		r.logger.Printf("[历史同步] 最后一次同步失败（连接可能已关闭）: %v\n", err)
	}
	if r.logFile != nil && r.logFile != os.Stderr {
		r.logFile.Close()
	}
}

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

func (r *RsyncSyncer) syncWithRsync(localFile string) error {
	remoteAddr := fmt.Sprintf("%s@%s:%s", r.host.User, r.host.IP, r.remoteFile)
	args := []string{
		"-avz", "--append", "-e",
		fmt.Sprintf("ssh -p %d -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null", r.host.Port),
		remoteAddr, localFile,
	}
	if _, err := os.Stat(localFile); os.IsNotExist(err) {
		os.MkdirAll(filepath.Dir(localFile), 0755)
		f, err := os.Create(localFile)
		if err != nil {
			return fmt.Errorf("创建本地文件失败: %v", err)
		}
		f.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "rsync", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("rsync失败: %v, 输出: %s", err, string(output))
	}
	outputStr := string(output)
	if strings.Contains(outputStr, "bytes/sec") || strings.Contains(outputStr, "speedup") {
		lines := strings.Split(outputStr, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "sent") &&
				!strings.HasPrefix(line, "total") && !strings.HasPrefix(line, "receiving") &&
				!strings.HasPrefix(line, "building") && !strings.Contains(line, "files to consider") {
				if !strings.HasPrefix(line, "./") && !strings.HasPrefix(line, "/") {
					r.logger.Printf("[历史同步] 已更新: %s\n", filepath.Base(r.remoteFile))
					break
				}
			}
		}
	}
	return nil
}

func (r *RsyncSyncer) syncWithSCP(localFile string) error {
	remoteAddr := fmt.Sprintf("%s@%s:%s", r.host.User, r.host.IP, r.remoteFile)
	args := []string{
		"-P", fmt.Sprintf("%d", r.host.Port),
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		remoteAddr, localFile,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "scp", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("scp失败: %v, 输出: %s", err, string(output))
	}
	r.logger.Printf("[历史同步] 已同步: %s (使用SCP)\n", filepath.Base(r.remoteFile))
	return nil
}

func (r *RsyncSyncer) detectRemoteHistoryFile() {
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
		r.remoteFile = "~/.bash_history"
	}
	checkCmd := fmt.Sprintf("ssh -p %d -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 %s@%s 'test -f %s && echo exists' 2>/dev/null",
		r.host.Port, r.host.User, r.host.IP, r.remoteFile)
	cmd = exec.Command("sh", "-c", checkCmd)
	output, err = cmd.Output()
	if err != nil || strings.TrimSpace(string(output)) != "exists" {
		if r.remoteFile == "~/.bash_history" {
			r.remoteFile = "~/.zsh_history"
		} else {
			r.remoteFile = "~/.bash_history"
		}
		checkCmd = fmt.Sprintf("ssh -p %d -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 %s@%s 'test -f %s && echo exists' 2>/dev/null",
			r.host.Port, r.host.User, r.host.IP, r.remoteFile)
		cmd = exec.Command("sh", "-c", checkCmd)
		output, err = cmd.Output()
		if err != nil || strings.TrimSpace(string(output)) != "exists" {
			r.remoteFile = "~/.bash_history"
		}
	}
	r.logger.Printf("[历史同步] 检测到的远程历史文件: %s\n", r.remoteFile)
}

func (r *RsyncSyncer) GetLastSyncTime() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastSyncTime
}

func (r *RsyncSyncer) GetLocalHistoryFile() string {
	if r.remoteFile == "" {
		return filepath.Join(r.localDir, "history.txt")
	}
	return filepath.Join(r.localDir, filepath.Base(r.remoteFile))
}
