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

	"golang.org/x/crypto/ssh"

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
	// conn 是登录期间已建立的 SSH 连接。远程文件探测、周期同步与退出前
	// 最后一次同步都复用它读取远程历史文件，避免反复新建连接带来的明显延迟。
	conn *ssh.Client
}

// NewRsyncSyncer 创建rsync同步器
// hostID 用于确定本地存储路径；conn 为可复用的已建立 SSH 连接（可为 nil）
func NewRsyncSyncer(host *config.Host, hostID string, conn *ssh.Client) *RsyncSyncer {
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
		conn:           conn,
	}
}

func checkRsyncAvailable() bool {
	_, err := exec.LookPath("rsync")
	return err == nil
}

// finalSyncTimeout 退出时最后一次同步的最大等待时间。
// 局域网内通过已有连接读取历史文件通常远快于此；慢速网络下超时即跳过，不阻塞退出。
const finalSyncTimeout = 500 * time.Millisecond

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

	// 最后一次同步是尽力而为的：在后台执行并只等待有限时间，
	// 避免在慢速网络下同步拖慢退出（历史上只差最后一次周期同步，最多 1 分钟）
	done := make(chan struct{})
	go func() {
		if err := r.syncFinal(); err != nil {
			r.logger.Printf("[历史同步] 最后一次同步失败（连接可能已关闭）: %v\n", err)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(finalSyncTimeout):
		r.logger.Println("[历史同步] 最后一次同步超时，跳过以加快退出")
	}

	if r.logFile != nil && r.logFile != os.Stderr {
		r.logFile.Close()
	}
}

// syncOnce 执行一次同步：优先复用已建立的 SSH 连接（conn），避免重新建连；
// 无可用连接时回退到独立的 rsync/scp。
func (r *RsyncSyncer) syncOnce(localFile string) error {
	if r.conn != nil {
		return r.syncViaSession(localFile)
	}
	if r.rsyncAvailable {
		return r.syncWithRsync(localFile)
	}
	return r.syncWithSCP(localFile)
}

// syncFinal 执行最后一次同步（尽力而为，由 Stop 在后台调用）
func (r *RsyncSyncer) syncFinal() error {
	if r.remoteFile == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	localFile := filepath.Join(r.localDir, filepath.Base(r.remoteFile))
	return r.syncOnce(localFile)
}

// syncViaSession 通过已建立的 SSH 连接直接读取远程历史文件并写入本地。
// 相比重新执行 rsync/scp（每次都要新建一条 SSH 连接），这几乎是无延迟的。
func (r *RsyncSyncer) syncViaSession(localFile string) error {
	session, err := r.conn.NewSession()
	if err != nil {
		return fmt.Errorf("创建SSH会话失败: %v", err)
	}
	defer session.Close()

	// 远程路径可能为 ~/.zsh_history 形式，交由远程 shell 展开
	cmd := fmt.Sprintf("cat %s", r.remoteFile)

	type sessionResult struct {
		out []byte
		err error
	}
	resCh := make(chan sessionResult, 1)
	go func() {
		out, err := session.Output(cmd)
		resCh <- sessionResult{out, err}
	}()

	// 通过已有连接读取应当很快；5 秒超时作为安全兜底
	select {
	case res := <-resCh:
		if res.err != nil {
			return fmt.Errorf("读取远程历史失败: %v", res.err)
		}
		if err := os.WriteFile(localFile, res.out, 0600); err != nil {
			return fmt.Errorf("写入本地历史失败: %v", err)
		}
		r.logger.Printf("[历史同步] 同步成功（复用已有连接）: %s\n", filepath.Base(r.remoteFile))
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("通过已有连接读取远程历史超时")
	}
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
	if err := r.syncOnce(localFile); err != nil {
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

// detectRemoteHistoryFile 探测远程 shell 与历史文件。
// 优先复用已建立的 SSH 连接（conn），避免登录时用外部 ssh 命令重新建连；
// 连接不可用时回退到外部 ssh 命令。
func (r *RsyncSyncer) detectRemoteHistoryFile() {
	if r.conn != nil {
		if err := r.detectViaSession(); err != nil {
			r.logger.Printf("[历史同步] 通过已有连接探测失败，回退外部ssh: %v\n", err)
			r.detectViaExternal()
		}
		return
	}
	r.detectViaExternal()
}

// detectViaSession 通过已建立的 SSH 连接探测远程 shell 与历史文件。
// 用单条命令完成探测，尽量减少会话（session）数——部分服务端对每个
// 新会话都有额外的处理延迟。
func (r *RsyncSyncer) detectViaSession() error {
	// 一条命令探测 shell 与两个历史文件；末尾的 true 保证退出码为 0，
	// 以便文件均不存在时仍能拿到输出（session.Output 遇非零退出码会报错）
	cmd := "echo $SHELL; test -f ~/.zsh_history && echo ZSH_EXISTS; test -f ~/.bash_history && echo BASH_EXISTS; true"
	out, err := r.runViaSession(cmd)
	if err != nil {
		return fmt.Errorf("通过已有连接探测失败: %v", err)
	}
	output := string(out)

	switch {
	case strings.Contains(output, "ZSH_EXISTS"):
		r.remoteFile = "~/.zsh_history"
	case strings.Contains(output, "BASH_EXISTS"):
		r.remoteFile = "~/.bash_history"
	case strings.Contains(output, "zsh"):
		// 两个历史文件都不存在时，按 shell 偏好选择（尽力而为）
		r.remoteFile = "~/.zsh_history"
	default:
		r.remoteFile = "~/.bash_history"
	}
	r.logger.Printf("[历史同步] 检测到的远程历史文件: %s\n", r.remoteFile)
	return nil
}

// runViaSession 在已建立的 SSH 连接上执行单条命令并返回输出。
// 通过 5 秒超时兜底，避免服务端不响应 exec 请求导致调用方（登录路径）卡死。
func (r *RsyncSyncer) runViaSession(cmd string) ([]byte, error) {
	session, err := r.conn.NewSession()
	if err != nil {
		return nil, fmt.Errorf("创建SSH会话失败: %v", err)
	}
	defer session.Close()

	type sessionResult struct {
		out []byte
		err error
	}
	resCh := make(chan sessionResult, 1)
	go func() {
		out, err := session.Output(cmd)
		resCh <- sessionResult{out, err}
	}()

	select {
	case res := <-resCh:
		return res.out, res.err
	case <-time.After(5 * time.Second):
		return nil, fmt.Errorf("通过已有连接执行命令超时: %s", cmd)
	}
}

// detectViaExternal 通过外部 ssh 命令探测远程 shell 与历史文件（回退路径）
func (r *RsyncSyncer) detectViaExternal() {
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
