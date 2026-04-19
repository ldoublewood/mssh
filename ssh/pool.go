package ssh

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/term"

	"mssh/config"
)

// Client 包装SSH客户端，保存连接信息
type Client struct {
	Host      *config.Host
	SSHClient *ssh.Client
	Session   *ssh.Session
	LastUsed  time.Time
	mu        sync.Mutex
}

// IsConnected 检查连接是否可用
func (c *Client) IsConnected() bool {
	if c.SSHClient == nil {
		return false
	}
	_, _, err := c.SSHClient.SendRequest("keepalive@openssh.com", true, nil)
	return err == nil
}

// Close 关闭连接
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.Session != nil {
		c.Session.Close()
		c.Session = nil
	}
	if c.SSHClient != nil {
		err := c.SSHClient.Close()
		c.SSHClient = nil
		return err
	}
	return nil
}

// Pool 管理SSH连接池
type Pool struct {
	clients map[string]*Client
	mu      sync.RWMutex
}

// NewPool 创建新的连接池
func NewPool() *Pool {
	return &Pool{
		clients: make(map[string]*Client),
	}
}

// GetClient 获取或创建SSH客户端
func (p *Pool) GetClient(host *config.Host) (*Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	key := host.Name

	if client, exists := p.clients[key]; exists {
		if client.IsConnected() {
			client.LastUsed = time.Now()
			return client, nil
		}
		client.Close()
		delete(p.clients, key)
	}

	client, err := p.createClient(host)
	if err != nil {
		return nil, err
	}

	p.clients[key] = client
	return client, nil
}

// createClient 创建新的SSH连接
func (p *Pool) createClient(host *config.Host) (*Client, error) {
	sshConfig := &ssh.ClientConfig{
		User:            host.User,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	var authMethods []ssh.AuthMethod

	if host.Password != "" {
		authMethods = append(authMethods, ssh.Password(host.Password))
	}

	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if agentConn, err := dialSSHAgent(sock); err == nil {
			authMethods = append(authMethods, ssh.PublicKeysCallback(agentConn.Signers))
		}
	}

	keyFiles := []string{
		filepath.Join(os.Getenv("HOME"), ".ssh", "id_rsa"),
		filepath.Join(os.Getenv("HOME"), ".ssh", "id_ed25519"),
		filepath.Join(os.Getenv("HOME"), ".ssh", "id_ecdsa"),
	}

	for _, keyFile := range keyFiles {
		if key, err := loadPrivateKey(keyFile); err == nil {
			authMethods = append(authMethods, ssh.PublicKeys(key))
		}
	}

	sshConfig.Auth = authMethods

	addr := fmt.Sprintf("%s:%d", host.IP, host.Port)
	sshClient, err := ssh.Dial("tcp", addr, sshConfig)
	if err != nil {
		return nil, fmt.Errorf("连接 %s 失败: %v", addr, err)
	}

	return &Client{
		Host:      host,
		SSHClient: sshClient,
		LastUsed:  time.Now(),
	}, nil
}

// Execute 在远程主机执行命令
func (p *Pool) Execute(host *config.Host, command string, output io.Writer) error {
	client, err := p.GetClient(host)
	if err != nil {
		return err
	}

	client.mu.Lock()
	defer client.mu.Unlock()

	if client.Session != nil {
		client.Session.Close()
	}

	session, err := client.SSHClient.NewSession()
	if err != nil {
		return fmt.Errorf("创建session失败: %v", err)
	}
	client.Session = session

	session.Stdout = output
	session.Stderr = output

	if term.IsTerminal(int(os.Stdout.Fd())) {
		width, height, _ := term.GetSize(int(os.Stdout.Fd()))
		if width == 0 {
			width = 80
		}
		if height == 0 {
			height = 24
		}
		modes := ssh.TerminalModes{
			ssh.ECHO:          0,
			ssh.TTY_OP_ISPEED: 14400,
			ssh.TTY_OP_OSPEED: 14400,
		}
		session.RequestPty("xterm-256color", height, width, modes)
	}

	return session.Run(command)
}

// ExecuteWithOutput 执行命令并返回输出
func (p *Pool) ExecuteWithOutput(host *config.Host, command string) (string, error) {
	var output strings.Builder
	err := p.Execute(host, command, &output)
	return output.String(), err
}

// StartShell 启动交互式shell（支持Ctrl+R历史搜索）
func (p *Pool) StartShell(host *config.Host) error {
	client, err := p.GetClient(host)
	if err != nil {
		return err
	}

	client.mu.Lock()
	defer client.mu.Unlock()

	if client.Session != nil {
		client.Session.Close()
	}

	session, err := client.SSHClient.NewSession()
	if err != nil {
		return fmt.Errorf("创建session失败: %v", err)
	}
	defer session.Close()

	// 设置stdin为原始模式
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("设置终端原始模式失败: %v", err)
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	// 获取终端尺寸
	width, height, _ := term.GetSize(int(os.Stdout.Fd()))
	if width == 0 {
		width = 80
	}
	if height == 0 {
		height = 24
	}

	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}

	if err := session.RequestPty("xterm-256color", height, width, modes); err != nil {
		return fmt.Errorf("请求伪终端失败: %v", err)
	}

	// 创建pipe用于转发输入/输出
	stdinPipe, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("创建stdin管道失败: %v", err)
	}

	stdoutPipe, err := session.StdoutPipe()
	if err != nil {
		return fmt.Errorf("创建stdout管道失败: %v", err)
	}

	stderrPipe, err := session.StderrPipe()
	if err != nil {
		return fmt.Errorf("创建stderr管道失败: %v", err)
	}

	if err := session.Shell(); err != nil {
		return fmt.Errorf("启动shell失败: %v", err)
	}

	// 加载所有主机的历史命令
	historyEntries := p.loadAllHostsHistory()

	// 启动goroutine转发stdout和stderr
	var wg sync.WaitGroup
	wg.Add(2)

	done := make(chan struct{})

	go func() {
		defer wg.Done()
		io.Copy(os.Stdout, stdoutPipe)
		close(done) // stdout结束时通知stdin转发退出
	}()

	go func() {
		defer wg.Done()
		io.Copy(os.Stderr, stderrPipe)
	}()

	// 读取stdin并检测Ctrl+R
	err = p.forwardStdinWithHistory(stdinPipe, host.Name, historyEntries, done)

	// 关闭stdin管道，通知远程shell输入结束
	stdinPipe.Close()

	// 等待输出转发完成
	wg.Wait()

	return session.Wait()
}

// forwardStdinWithHistory 转发stdin到远程，并拦截Ctrl+R进行历史搜索
func (p *Pool) forwardStdinWithHistory(stdinPipe io.WriteCloser, hostName string, historyEntries []HistoryEntry, done chan struct{}) error {

	buf := make([]byte, 1024)
	inSearchMode := false
	searchTerm := ""
	matchIndex := 0
	var matches []HistoryEntry

	for {
		// 检查是否需要退出
		select {
		case <-done:
			return nil
		default:
		}

		// 设置超时读取，避免阻塞
		os.Stdin.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, err := os.Stdin.Read(buf)
		os.Stdin.SetReadDeadline(time.Time{})

		if err != nil {
			// 检查是否是超时或其他可恢复错误
			if pathErr, ok := err.(*os.PathError); ok {
				if errno, ok := pathErr.Err.(syscall.Errno); ok && errno == syscall.EAGAIN {
					continue
				}
			}
			// 其他错误（如EOF）退出
			return err
		}

		if n == 0 {
			continue
		}

		i := 0
		for i < n {
			ch := buf[i]

		// 检测Ctrl+R (0x12) - 进入或继续搜索
			if ch == 0x12 {
				if !inSearchMode {
					// 首次进入搜索模式
					inSearchMode = true
					searchTerm = ""
					matchIndex = 0
					matches = p.findMatches(historyEntries, searchTerm)
					fmt.Printf("\r\n")
					p.showSearchStatus(searchTerm, matches, matchIndex, hostName)
				} else {
					// 已经在搜索模式，跳到下一个匹配
					if len(matches) > 0 {
						matchIndex = (matchIndex + 1) % len(matches)
						p.showSearchStatus(searchTerm, matches, matchIndex, hostName)
					}
				}
			i++
			continue
		}

			// 检测Ctrl+G (0x07) 或 Ctrl+C (0x03) - 取消搜索
			if (ch == 0x07 || ch == 0x03) && inSearchMode {
				inSearchMode = false
				searchTerm = ""
				matches = nil
				fmt.Printf("\r\n")
				i++
				continue
			}

			// 检测回车 - 执行匹配的命令
			if (ch == '\r' || ch == '\n') && inSearchMode {
				if len(matches) > 0 && matchIndex < len(matches) {
					selectedCmd := matches[matchIndex].Command
					inSearchMode = false
					searchTerm = ""
					matches = nil
					fmt.Printf("\r\n")
					stdinPipe.Write([]byte(selectedCmd + "\n"))
				} else {
					inSearchMode = false
					fmt.Printf("\r\n")
				}
				i++
				continue
			}

			// 检测退格 - 删除搜索字符
			if (ch == 0x7F || ch == 0x08) && inSearchMode {
				if len(searchTerm) > 0 {
					searchTerm = searchTerm[:len(searchTerm)-1]
					matches = p.findMatches(historyEntries, searchTerm)
					matchIndex = 0
					p.showSearchStatus(searchTerm, matches, matchIndex, hostName)
				}
				i++
				continue
			}

			// 在搜索模式下，普通字符添加到搜索词
			if inSearchMode && ch >= 32 && ch < 127 {
				searchTerm += string(ch)
				matches = p.findMatches(historyEntries, searchTerm)
				matchIndex = 0
				p.showSearchStatus(searchTerm, matches, matchIndex, hostName)
				i++
				continue
			}

			// 其他情况：正常转发到远程
			stdinPipe.Write([]byte{ch})
			i++
		}
	}
}

// HistoryEntry 历史命令条目，包含命令和来源主机
type HistoryEntry struct {
	Command string
	Host    string
}

// loadAllHostsHistory 加载所有主机的历史命令
func (p *Pool) loadAllHostsHistory() []HistoryEntry {
	usr, err := user.Current()
	if err != nil {
		return nil
	}

	historyDir := filepath.Join(usr.HomeDir, ".mssh_history")

	// 读取历史目录下的所有子目录（主机）
	entries, err := os.ReadDir(historyDir)
	if err != nil {
		return nil
	}

	var allEntries []HistoryEntry

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		hostName := entry.Name()
		// 跳过logs目录
		if hostName == "logs" {
			continue
		}

		historyFile := filepath.Join(historyDir, hostName, "history.txt")
		file, err := os.Open(historyFile)
		if err != nil {
			continue
		}

		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			cmd := strings.TrimSpace(scanner.Text())
			if cmd != "" {
				allEntries = append(allEntries, HistoryEntry{
					Command: cmd,
					Host:    hostName,
				})
			}
		}
		file.Close()
	}

	// 倒序排列（最新的在前）
	for i, j := 0, len(allEntries)-1; i < j; i, j = i+1, j-1 {
		allEntries[i], allEntries[j] = allEntries[j], allEntries[i]
	}

	return allEntries
}

// findMatches 查找匹配的历史命令（返回HistoryEntry列表）
func (p *Pool) findMatches(historyEntries []HistoryEntry, searchTerm string) []HistoryEntry {
	if searchTerm == "" {
		return historyEntries
	}

	searchLower := strings.ToLower(searchTerm)
	var matches []HistoryEntry

	for _, entry := range historyEntries {
		if strings.Contains(strings.ToLower(entry.Command), searchLower) {
			matches = append(matches, entry)
		}
	}

	return matches
}

// showSearchStatus 显示搜索状态（类似bash的反向搜索）
func (p *Pool) showSearchStatus(searchTerm string, matches []HistoryEntry, matchIndex int, currentHost string) {
	// 简单输出，不使用复杂的ANSI序列
	if len(matches) == 0 {
		if searchTerm == "" {
			fmt.Printf("\n[mssh] (reverse-i-search)[all-hosts]: ")
		} else {
			fmt.Printf("\n[mssh] (failed reverse-i-search)`%s': ", searchTerm)
		}
	} else {
		matchedEntry := matches[matchIndex]
		// 高亮匹配部分
		highlighted := p.highlightMatch(matchedEntry.Command, searchTerm)

		// 显示格式: [mssh] (reverse-i-search)`search': [host] command
		fmt.Printf("\n[mssh] (reverse-i-search)`%s': [%s] %s", searchTerm, matchedEntry.Host, highlighted)
	}
}

// highlightMatch 高亮匹配的部分
func (p *Pool) highlightMatch(cmd, searchTerm string) string {
	if searchTerm == "" {
		return cmd
	}

	searchLower := strings.ToLower(searchTerm)
	cmdLower := strings.ToLower(cmd)

	idx := strings.Index(cmdLower, searchLower)
	if idx == -1 {
		return cmd
	}

	// 使用ANSI颜色高亮匹配部分 (黄色加粗背景)
	before := cmd[:idx]
	match := cmd[idx : idx+len(searchTerm)]
	after := cmd[idx+len(searchTerm):]

	return fmt.Sprintf("%s\x1b[1;33m%s\x1b[0m%s", before, match, after)
}

// GetSession 获取新的session
func (p *Pool) GetSession(host *config.Host) (*ssh.Session, error) {
	client, err := p.GetClient(host)
	if err != nil {
		return nil, err
	}

	client.mu.Lock()
	defer client.mu.Unlock()

	return client.SSHClient.NewSession()
}

// Close 关闭所有连接
func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, client := range p.clients {
		client.Close()
	}
	p.clients = make(map[string]*Client)
}

// CloseHost 关闭指定主机的连接
func (p *Pool) CloseHost(hostName string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if client, exists := p.clients[hostName]; exists {
		client.Close()
		delete(p.clients, hostName)
	}
}

// loadPrivateKey 加载私钥
func loadPrivateKey(path string) (ssh.Signer, error) {
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ssh.ParsePrivateKey(key)
}

// dialSSHAgent 连接SSH agent
func dialSSHAgent(sock string) (agentConn, error) {
	conn, err := dialUnix(sock)
	if err != nil {
		return nil, err
	}
	return &agentClient{conn}, nil
}

// agentConn 接口
type agentConn interface {
	Signers() ([]ssh.Signer, error)
	Close() error
}

// agentClient SSH agent客户端
type agentClient struct {
	conn io.ReadWriteCloser
}

func (a *agentClient) Signers() ([]ssh.Signer, error) {
	return nil, fmt.Errorf("not implemented")
}

func (a *agentClient) Close() error {
	return a.conn.Close()
}

func dialUnix(path string) (io.ReadWriteCloser, error) {
	return nil, fmt.Errorf("not implemented")
}
