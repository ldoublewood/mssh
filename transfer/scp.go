package transfer

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"

	"mssh/config"
)

// SCPTransfer SCP文件传输
type SCPTransfer struct {
	pool interface {
		GetSession(host *config.Host) (*ssh.Session, error)
	}
}

// NewSCPTransfer 创建SCP传输器
func NewSCPTransfer(pool interface {
	GetSession(host *config.Host) (*ssh.Session, error)
}) *SCPTransfer {
	return &SCPTransfer{pool: pool}
}

// UploadFile 上传文件到远程主机
func (s *SCPTransfer) UploadFile(host *config.Host, localPath, remotePath string) error {
	session, err := s.pool.GetSession(host)
	if err != nil {
		return err
	}
	defer session.Close()

	// 读取本地文件
	file, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("打开本地文件失败: %v", err)
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return fmt.Errorf("获取文件信息失败: %v", err)
	}

	// SCP协议: C{mode} {size} {filename}
	filename := filepath.Base(remotePath)
	header := fmt.Sprintf("C0644 %d %s\n", stat.Size(), filename)

	stdin, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("获取stdin管道失败: %v", err)
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		return fmt.Errorf("获取stdout管道失败: %v", err)
	}

	// 启动scp命令
	remoteDir := filepath.Dir(remotePath)
	scpCmd := fmt.Sprintf("scp -t %s", remoteDir)
	if err := session.Start(scpCmd); err != nil {
		return fmt.Errorf("启动scp失败: %v", err)
	}

	// 等待确认
	if err := readResponse(stdout); err != nil {
		return fmt.Errorf("scp响应错误: %v", err)
	}

	// 发送文件头
	if _, err := stdin.Write([]byte(header)); err != nil {
		return fmt.Errorf("发送文件头失败: %v", err)
	}

	// 等待确认
	if err := readResponse(stdout); err != nil {
		return fmt.Errorf("scp响应错误: %v", err)
	}

	// 发送文件内容
	if _, err := io.Copy(stdin, file); err != nil {
		return fmt.Errorf("发送文件内容失败: %v", err)
	}

	// 发送结束符
	if _, err := stdin.Write([]byte{0}); err != nil {
		return fmt.Errorf("发送结束符失败: %v", err)
	}

	stdin.Close()
	return session.Wait()
}

// UploadFileToHosts 上传文件到多台主机
func (s *SCPTransfer) UploadFileToHosts(hosts []*config.Host, localPath, remotePath string, concurrent bool) []error {
	if !concurrent {
		// 顺序执行
		errors := make([]error, len(hosts))
		for i, host := range hosts {
			fmt.Printf("[%s] 开始上传...\n", host.Name)
			if err := s.UploadFile(host, localPath, remotePath); err != nil {
				errors[i] = fmt.Errorf("[%s] 上传失败: %v", host.Name, err)
			} else {
				fmt.Printf("[%s] 上传完成\n", host.Name)
			}
		}
		return errors
	}

	// 并发执行
	var wg sync.WaitGroup
	errors := make([]error, len(hosts))
	var mu sync.Mutex

	for i, host := range hosts {
		wg.Add(1)
		go func(idx int, h *config.Host) {
			defer wg.Done()
			fmt.Printf("[%s] 开始上传...\n", h.Name)
			if err := s.UploadFile(h, localPath, remotePath); err != nil {
				mu.Lock()
				errors[idx] = fmt.Errorf("[%s] 上传失败: %v", h.Name, err)
				mu.Unlock()
			} else {
				fmt.Printf("[%s] 上传完成\n", h.Name)
			}
		}(i, host)
	}

	wg.Wait()
	return errors
}

// DownloadFile 从远程主机下载文件（单台）
func (s *SCPTransfer) DownloadFile(host *config.Host, remotePath, localPath string) error {
	session, err := s.pool.GetSession(host)
	if err != nil {
		return err
	}
	defer session.Close()

	stdin, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("获取stdin管道失败: %v", err)
	}
	defer stdin.Close()

	stdout, err := session.StdoutPipe()
	if err != nil {
		return fmt.Errorf("获取stdout管道失败: %v", err)
	}

	stderr, err := session.StderrPipe()
	if err != nil {
		return fmt.Errorf("获取stderr管道失败: %v", err)
	}

	// 启动scp命令
	scpCmd := fmt.Sprintf("scp -f %s", remotePath)
	if err := session.Start(scpCmd); err != nil {
		return fmt.Errorf("启动scp失败: %v", err)
	}

	// 发送确认
	stdin.Write([]byte{0})

	// 读取响应头
	buf := make([]byte, 1)
	if _, err := stdout.Read(buf); err != nil {
		return fmt.Errorf("读取响应失败: %v", err)
	}

	if buf[0] != 'C' {
		// 错误响应
		errMsg, _ := io.ReadAll(stderr)
		return fmt.Errorf("scp错误: %s", string(errMsg))
	}

	// 读取文件头
	headerBuf := make([]byte, 1024)
	n, err := stdout.Read(headerBuf)
	if err != nil {
		return fmt.Errorf("读取文件头失败: %v", err)
	}

	header := string(headerBuf[:n])
	var mode string
	var size int64
	var filename string
	fmt.Sscanf(header, "%s %d %s", &mode, &size, &filename)

	// 发送确认
	stdin.Write([]byte{0})

	// 创建本地文件
	if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
		return fmt.Errorf("创建本地目录失败: %v", err)
	}

	file, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("创建本地文件失败: %v", err)
	}
	defer file.Close()

	// 读取文件内容
	if _, err := io.CopyN(file, stdout, size); err != nil {
		return fmt.Errorf("接收文件内容失败: %v", err)
	}

	// 发送确认
	stdin.Write([]byte{0})

	return session.Wait()
}

// readResponse 读取scp响应
func readResponse(r io.Reader) error {
	buf := make([]byte, 1)
	if _, err := r.Read(buf); err != nil {
		return err
	}

	if buf[0] == 0 {
		return nil
	}

	// 读取错误信息
	var errMsg strings.Builder
	buf = make([]byte, 256)
	for {
		n, err := r.Read(buf)
		if err != nil || n == 0 || buf[n-1] == '\n' {
			break
		}
		errMsg.Write(buf[:n])
	}
	return fmt.Errorf("scp错误: %s", errMsg.String())
}

// RsyncHistory 使用rsync同步历史命令
func RsyncHistory(host *config.Host, pool interface {
	ExecuteWithOutput(host *config.Host, command string) (string, error)
}, direction string) error {
	// 获取远程历史文件
	remoteHistoryFiles := []string{
		"~/.bash_history",
		"~/.zsh_history",
	}

	switch direction {
	case "from":
		// 从远程拉取到本地
		// 实际实现需要使用rsync over SSH
		// 这里简化处理：直接读取远程文件内容并合并
		for _, remoteFile := range remoteHistoryFiles {
			content, err := pool.ExecuteWithOutput(host, fmt.Sprintf("cat %s 2>/dev/null || echo ''", remoteFile))
			if err != nil {
				continue
			}
			// 保存到本地
			_ = content // 实际应保存到历史目录
		}

	case "to":
		// 从本地推送到远程
		// 实际实现需要使用rsync over SSH
	}

	return nil
}

// TransferManager 文件传输管理器
type TransferManager struct {
	scPool interface {
		GetSession(host *config.Host) (*ssh.Session, error)
		ExecuteWithOutput(host *config.Host, command string) (string, error)
	}
}

// NewTransferManager 创建传输管理器
func NewTransferManager(pool interface {
	GetSession(host *config.Host) (*ssh.Session, error)
	ExecuteWithOutput(host *config.Host, command string) (string, error)
}) *TransferManager {
	return &TransferManager{scPool: pool}
}

// Upload 上传文件到主机或组
func (t *TransferManager) Upload(hosts []*config.Host, localPath, remotePath string, concurrent bool) error {
	scp := NewSCPTransfer(t.scPool)
	errors := scp.UploadFileToHosts(hosts, localPath, remotePath, concurrent)

	// 检查错误
	for _, err := range errors {
		if err != nil {
			return err
		}
	}
	return nil
}

// Download 从单台主机下载文件
func (t *TransferManager) Download(host *config.Host, remotePath, localPath string) error {
	scp := NewSCPTransfer(t.scPool)
	return scp.DownloadFile(host, remotePath, localPath)
}
