package ssh

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/term"

	"mssh/config"
)

// Client 包装SSH客户端，保存连接信息
type Client struct {
	Host         *config.Host
	SSHClient    *ssh.Client
	Session      *ssh.Session
	LastUsed     time.Time
	HostID       string // 唯一主机标识: <主机名>_<用户名>_<主机指纹>
	Fingerprint  string // SSH主机公钥指纹
	keepaliveCh  chan struct{}
	mu           sync.Mutex
}

// IsConnected 检查连接是否可用
func (c *Client) IsConnected() bool {
	if c.SSHClient == nil {
		return false
	}
	_, _, err := c.SSHClient.SendRequest("keepalive@openssh.com", true, nil)
	return err == nil
}

// GetHostID 获取主机唯一标识
func (c *Client) GetHostID() string {
	return c.HostID
}

// Close 关闭连接
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.keepaliveCh != nil {
		close(c.keepaliveCh)
		c.keepaliveCh = nil
	}
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

// knownHostEntry known_hosts 文件中的条目
type knownHostEntry struct {
	Fingerprint string `json:"fingerprint"`
}

// knownHostsFile 返回 known_hosts 文件路径
func knownHostsFile() string {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".mssh")
	os.MkdirAll(dir, 0700)
	return filepath.Join(dir, "known_hosts")
}

// loadKnownHosts 加载已知主机指纹
func loadKnownHosts() map[string]string {
	result := make(map[string]string)
	path := knownHostsFile()
	data, err := os.ReadFile(path)
	if err != nil {
		return result
	}
	var entries map[string]knownHostEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return result
	}
	for addr, entry := range entries {
		result[addr] = entry.Fingerprint
	}
	return result
}

// saveKnownHost 保存已知主机指纹
func saveKnownHost(addr, fingerprint string) {
	entries := loadKnownHosts()
	entries[addr] = fingerprint
	path := knownHostsFile()
	data, _ := json.MarshalIndent(entries, "", "  ")
	os.WriteFile(path, data, 0600)
}

// hostKeyFingerprint 计算主机密钥的SHA256指纹，返回文件系统安全的字符串
func hostKeyFingerprint(key ssh.PublicKey) string {
	hash := sha256.Sum256(key.Marshal())
	encoded := base64.StdEncoding.EncodeToString(hash[:])
	encoded = strings.NewReplacer("/", "_", "+", "-", "=", "").Replace(encoded)
	return encoded
}

// createClient 创建新的SSH连接
func (p *Pool) createClient(host *config.Host) (*Client, error) {
	var capturedFingerprint string
	addr := fmt.Sprintf("%s:%d", host.IP, host.Port)

	knownHosts := loadKnownHosts()

	sshConfig := &ssh.ClientConfig{
		User: host.User,
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			fp := hostKeyFingerprint(key)
			capturedFingerprint = fp

			if storedFP, known := knownHosts[addr]; known {
				if storedFP != fp {
					return fmt.Errorf("主机密钥指纹不匹配: 期望 %s, 实际 %s", storedFP, fp)
				}
			}
			return nil
		},
		Timeout: 10 * time.Second,
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

	sshClient, err := ssh.Dial("tcp", addr, sshConfig)
	if err != nil {
		return nil, fmt.Errorf("连接 %s 失败: %v", addr, err)
	}

	// 保存新主机的指纹（TOFU）
	if capturedFingerprint != "" {
		if _, known := knownHosts[addr]; !known {
			saveKnownHost(addr, capturedFingerprint)
		}
	} else {
		capturedFingerprint = "noauth"
	}

	hostID := fmt.Sprintf("%s_%s_%s", host.Name, host.User, capturedFingerprint)

	keepaliveCh := make(chan struct{})
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_, _, err := sshClient.SendRequest("keepalive@openssh.com", true, nil)
				if err != nil {
					return
				}
			case <-keepaliveCh:
				return
			}
		}
	}()

	return &Client{
		Host:        host,
		SSHClient:   sshClient,
		LastUsed:    time.Now(),
		HostID:      hostID,
		Fingerprint: capturedFingerprint,
		keepaliveCh: keepaliveCh,
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

// StartShell 启动交互式shell
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

	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("设置终端原始模式失败: %v", err)
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

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

	historyEntries := p.loadAllHostsHistory()

	var wg sync.WaitGroup
	wg.Add(2)

	done := make(chan struct{})

	go func() {
		defer wg.Done()
		io.Copy(os.Stdout, stdoutPipe)
		close(done)
	}()

	go func() {
		defer wg.Done()
		io.Copy(os.Stderr, stderrPipe)
	}()

	err = p.forwardStdinWithHistory(stdinPipe, host.Name, historyEntries, done)

	stdinPipe.Close()
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
		select {
		case <-done:
			return nil
		default:
		}

		os.Stdin.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, err := os.Stdin.Read(buf)
		os.Stdin.SetReadDeadline(time.Time{})

		if err != nil {
			if pathErr, ok := err.(*os.PathError); ok {
				if errno, ok := pathErr.Err.(syscall.Errno); ok && errno == syscall.EAGAIN {
					continue
				}
			}
			return err
		}

		if n == 0 {
			continue
		}

		i := 0
		for i < n {
			ch := buf[i]

			if ch == 0x12 {
				if !inSearchMode {
					inSearchMode = true
					searchTerm = ""
					matchIndex = 0
					matches = p.findMatches(historyEntries, searchTerm)
					p.showSearchStatus(searchTerm, matches, matchIndex, hostName)
				} else {
					if len(matches) > 0 {
						matchIndex = (matchIndex + 1) % len(matches)
						p.showSearchStatus(searchTerm, matches, matchIndex, hostName)
					}
				}
				i++
				continue
			}

			if (ch == 0x07 || ch == 0x03) && inSearchMode {
				inSearchMode = false
				searchTerm = ""
				matches = nil
				fmt.Printf("\r\n\r")
				i++
				continue
			}

			if (ch == '\r' || ch == '\n') && inSearchMode {
				if len(matches) > 0 && matchIndex < len(matches) {
					selectedCmd := matches[matchIndex].Command
					inSearchMode = false
					searchTerm = ""
					matches = nil
					fmt.Printf("\r\n\r")
					stdinPipe.Write([]byte(selectedCmd + "\n"))
				} else {
					inSearchMode = false
					fmt.Printf("\r\n\r")
				}
				i++
				continue
			}

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

			if inSearchMode && ch >= 32 && ch < 127 {
				searchTerm += string(ch)
				matches = p.findMatches(historyEntries, searchTerm)
				matchIndex = 0
				p.showSearchStatus(searchTerm, matches, matchIndex, hostName)
				i++
				continue
			}

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

	// 优先扫描新目录 ~/.mssh/<host_id>/history/
	msshDir := filepath.Join(usr.HomeDir, ".mssh")
	entries := loadHistoryFromDir(msshDir)

	// 兼容旧目录 ~/.mssh_history/<hostname>/
	oldDir := filepath.Join(usr.HomeDir, ".mssh_history")
	oldEntries := loadHistoryFromDir(oldDir)
	entries = append(entries, oldEntries...)

	// 倒序排列（最新的在前）
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}

	return entries
}

// loadHistoryFromDir 从指定目录加载所有主机的 history 文件
func loadHistoryFromDir(baseDir string) []HistoryEntry {
	var allEntries []HistoryEntry

	hostDirs, err := os.ReadDir(baseDir)
	if err != nil {
		return nil
	}

	for _, entry := range hostDirs {
		if !entry.IsDir() {
			continue
		}
		hostName := entry.Name()
		if hostName == "logs" {
			continue
		}

		// 新结构: <host_id>/history/ 下的文件
		historyDir := filepath.Join(baseDir, hostName, "history")
		historyFiles, _ := filepath.Glob(filepath.Join(historyDir, "*"))
		for _, hf := range historyFiles {
			loadHistoryFile(hf, hostName, &allEntries)
		}

		// 旧结构: <hostname>/ 下的文件（直接放在host目录下）
		oldFiles := []string{
			filepath.Join(baseDir, hostName, ".bash_history"),
			filepath.Join(baseDir, hostName, ".zsh_history"),
			filepath.Join(baseDir, hostName, "history.txt"),
		}
		for _, hf := range oldFiles {
			loadHistoryFile(hf, hostName, &allEntries)
		}
	}

	return allEntries
}

// loadHistoryFile 读取单个 history 文件
func loadHistoryFile(path, hostName string, entries *[]HistoryEntry) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		cmd := strings.TrimSpace(scanner.Text())
		if cmd != "" {
			if strings.HasPrefix(cmd, ":") {
				parts := strings.SplitN(cmd, ";", 2)
				if len(parts) == 2 {
					cmd = strings.TrimSpace(parts[1])
				}
			}
			if cmd != "" {
				*entries = append(*entries, HistoryEntry{
					Command: cmd,
					Host:    hostName,
				})
			}
		}
	}
}

// findMatches 查找匹配的历史命令
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

// showSearchStatus 显示搜索状态
func (p *Pool) showSearchStatus(searchTerm string, matches []HistoryEntry, matchIndex int, currentHost string) {
	fmt.Printf("\r\033[2K")

	if len(matches) == 0 {
		if searchTerm == "" {
			fmt.Printf("(reverse-i-search)[all-hosts]: ")
		} else {
			fmt.Printf("(failed reverse-i-search)`%s': ", searchTerm)
		}
	} else {
		matchedEntry := matches[matchIndex]
		highlighted := p.highlightMatch(matchedEntry.Command, searchTerm)
		fmt.Printf("(reverse-i-search)`%s': [%s] %s", searchTerm, matchedEntry.Host, highlighted)
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
	ag := agent.NewClient(a.conn)
	return ag.Signers()
}

func (a *agentClient) Close() error {
	return a.conn.Close()
}

func dialUnix(path string) (io.ReadWriteCloser, error) {
	return net.Dial("unix", path)
}
