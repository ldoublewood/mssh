package ssh

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	// 尝试发送keep-alive
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

	// 检查是否已有可用连接
	if client, exists := p.clients[key]; exists {
		if client.IsConnected() {
			client.LastUsed = time.Now()
			return client, nil
		}
		// 连接已断开，删除旧连接
		client.Close()
		delete(p.clients, key)
	}

	// 创建新连接
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
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // 生产环境应使用known_hosts
		Timeout:         10 * time.Second,
	}

	// 配置认证方式
	var authMethods []ssh.AuthMethod

	// 1. 尝试使用密码认证
	if host.Password != "" {
		authMethods = append(authMethods, ssh.Password(host.Password))
	}

	// 2. 尝试SSH agent
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if agentConn, err := dialSSHAgent(sock); err == nil {
			authMethods = append(authMethods, ssh.PublicKeysCallback(agentConn.Signers))
		}
	}

	// 3. 尝试常用密钥文件
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

	// 关闭旧session
	if client.Session != nil {
		client.Session.Close()
	}

	// 创建新session
	session, err := client.SSHClient.NewSession()
	if err != nil {
		return fmt.Errorf("创建session失败: %v", err)
	}
	client.Session = session

	// 设置输出
	session.Stdout = output
	session.Stderr = output

	// 设置伪终端以支持彩色输出
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

// ExecuteWithInput 执行需要交互的命令
func (p *Pool) ExecuteWithInput(host *config.Host, stdin io.Reader, stdout, stderr io.Writer) error {
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

	// 设置IO
	session.Stdin = stdin
	session.Stdout = stdout
	session.Stderr = stderr

	// 设置伪终端
	width, height := 80, 24
	if term.IsTerminal(int(os.Stdout.Fd())) {
		w, h, _ := term.GetSize(int(os.Stdout.Fd()))
		if w > 0 {
			width = w
		}
		if h > 0 {
			height = h
		}
	}

	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}

	if err := session.RequestPty("xterm-256color", height, width, modes); err != nil {
		return fmt.Errorf("请求伪终端失败: %v", err)
	}

	if err := session.Shell(); err != nil {
		return fmt.Errorf("启动shell失败: %v", err)
	}

	return session.Wait()
}

// GetSession 获取新的session（用于自定义操作如SCP）
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

// CleanupIdle 清理空闲连接
func (p *Pool) CleanupIdle(maxIdle time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	for name, client := range p.clients {
		if now.Sub(client.LastUsed) > maxIdle {
			client.Close()
			delete(p.clients, name)
		}
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
	// 简化的agent实现，实际应使用golang.org/x/crypto/ssh/agent
	return nil, fmt.Errorf("not implemented")
}

func (a *agentClient) Close() error {
	return a.conn.Close()
}

func dialUnix(path string) (io.ReadWriteCloser, error) {
	// 简化实现
	return nil, fmt.Errorf("not implemented")
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

	session.Stdin = os.Stdin
	session.Stdout = os.Stdout
	session.Stderr = os.Stderr

	if err := session.Shell(); err != nil {
		return fmt.Errorf("启动shell失败: %v", err)
	}

	return session.Wait()
}

// ExecuteAndStream 执行命令并实时输出
func (p *Pool) ExecuteAndStream(host *config.Host, command string, prefix string) error {
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

	// 设置伪终端
	width, height := 80, 24
	modes := ssh.TerminalModes{
		ssh.ECHO:          0,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	session.RequestPty("xterm-256color", height, width, modes)

	stdout, err := session.StdoutPipe()
	if err != nil {
		return fmt.Errorf("获取stdout管道失败: %v", err)
	}

	stderr, err := session.StderrPipe()
	if err != nil {
		return fmt.Errorf("获取stderr管道失败: %v", err)
	}

	if err := session.Start(command); err != nil {
		return fmt.Errorf("启动命令失败: %v", err)
	}

	// 实时输出
	var wg sync.WaitGroup
	wg.Add(2)

	outputLine := func(reader io.Reader) {
		defer wg.Done()
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			fmt.Printf("%s%s\n", prefix, scanner.Text())
		}
	}

	go outputLine(stdout)
	go outputLine(stderr)

	wg.Wait()
	return session.Wait()
}
