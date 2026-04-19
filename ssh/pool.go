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

	// 启动goroutine转发stdout和stderr
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		io.Copy(os.Stdout, stdoutPipe)
	}()

	go func() {
		defer wg.Done()
		io.Copy(os.Stderr, stderrPipe)
	}()

	// 读取stdin并检测Ctrl+R
	err = p.forwardStdinWithHistory(stdinPipe, host.Name)

	// 等待输出转发完成
	wg.Wait()

	return session.Wait()
}

// forwardStdinWithHistory 转发stdin到远程，并拦截Ctrl+R进行历史搜索
func (p *Pool) forwardStdinWithHistory(stdinPipe io.WriteCloser, hostName string) error {
	// 读取本地历史文件
	historyCmds := p.loadHostHistory(hostName)

	buf := make([]byte, 1024)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return err
		}

		// 检测Ctrl+R (0x12)
		ctrlRIndex := -1
		for i := 0; i < n; i++ {
			if buf[i] == 0x12 { // Ctrl+R
				ctrlRIndex = i
				break
			}
		}

		if ctrlRIndex != -1 {
			// 发送Ctrl+R之前的数据
			if ctrlRIndex > 0 {
				stdinPipe.Write(buf[:ctrlRIndex])
			}

			// 显示历史搜索界面
			selectedCmd := p.showHistorySearch(historyCmds, hostName)
			if selectedCmd != "" {
				// 发送选中的命令到远程shell
				stdinPipe.Write([]byte(selectedCmd + "\n"))
			}

			// 发送剩余数据
			if ctrlRIndex+1 < n {
				stdinPipe.Write(buf[ctrlRIndex+1:])
			}
		} else {
			// 正常转发
			_, err = stdinPipe.Write(buf[:n])
			if err != nil {
				return err
			}
		}
	}
}

// loadHostHistory 加载指定主机的历史命令
func (p *Pool) loadHostHistory(hostName string) []string {
	usr, err := user.Current()
	if err != nil {
		return nil
	}

	historyFile := filepath.Join(usr.HomeDir, ".mssh_history", hostName, "history.txt")
	file, err := os.Open(historyFile)
	if err != nil {
		return nil
	}
	defer file.Close()

	var cmds []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		cmd := strings.TrimSpace(scanner.Text())
		if cmd != "" {
			cmds = append(cmds, cmd)
		}
	}

	return cmds
}

// showHistorySearch 显示历史搜索界面
func (p *Pool) showHistorySearch(historyCmds []string, hostName string) string {
	if len(historyCmds) == 0 {
		fmt.Printf("\r\n[mssh] 没有 [%s] 的历史命令\r\n", hostName)
		return ""
	}

	fmt.Printf("\r\n[mssh] [%s] 历史命令搜索 (输入关键词过滤, 回车选择, Ctrl+C取消):\r\n", hostName)

	// 使用简单的方式读取输入，避免复杂的终端模式切换
	searchTerm := ""
	buf := make([]byte, 1)

	for {
		// 显示当前搜索结果
		fmt.Printf("\r\n搜索: %s\r\n", searchTerm)

		// 过滤并显示匹配的历史命令
		matches := p.filterHistory(historyCmds, searchTerm)
		if len(matches) == 0 {
			fmt.Println("没有匹配的命令")
		} else {
			fmt.Printf("找到 %d 条匹配命令:\r\n", len(matches))
			for i, cmd := range matches {
				if i >= 10 {
					fmt.Printf("... 还有 %d 条\r\n", len(matches)-10)
					break
				}
				// 高亮匹配部分
				highlighted := p.highlightMatch(cmd, searchTerm)
				fmt.Printf("  [%d] %s\r\n", i+1, highlighted)
			}
		}

		fmt.Print("\r\n输入关键词或序号 (退格删除, 回车确认): ")

		// 逐字符读取输入
		inputBuffer := []byte{}
		for {
			// 设置超时读取，避免阻塞
			os.Stdin.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			n, err := os.Stdin.Read(buf)
			os.Stdin.SetReadDeadline(time.Time{})

			if err != nil {
				// 超时，继续
				continue
			}

			if n == 0 {
				continue
			}

			ch := buf[0]

			// 处理特殊键
			switch ch {
			case '\r', '\n': // 回车
				input := strings.TrimSpace(string(inputBuffer))

				// 检查是否是数字选择
				var num int
				if _, err := fmt.Sscanf(input, "%d", &num); err == nil && num > 0 && num <= len(matches) {
					fmt.Printf("\r\n")
					return matches[num-1]
				}

				// 检查是否是取消
				if input == "" || input == "q" || input == "quit" {
					fmt.Printf("\r\n")
					return ""
				}

				// 更新搜索词，重新显示
				searchTerm = input
				break // 跳出内层循环，重新显示搜索结果

			case 0x03: // Ctrl+C
				fmt.Printf("\r\n")
				return ""

			case 0x7F, 0x08: // 退格/Backspace
				if len(inputBuffer) > 0 {
					inputBuffer = inputBuffer[:len(inputBuffer)-1]
					// 刷新显示
					fmt.Printf("\b \b")
				}

			default:
				// 普通字符
				if ch >= 32 && ch < 127 { // 可打印字符
					inputBuffer = append(inputBuffer, ch)
					fmt.Printf("%c", ch)
				}
			}
		}
	}
}

// filterHistory 根据搜索词过滤历史命令
func (p *Pool) filterHistory(cmds []string, searchTerm string) []string {
	if searchTerm == "" {
		// 返回最近的命令（倒序）
		result := make([]string, len(cmds))
		for i := range cmds {
			result[i] = cmds[len(cmds)-1-i]
		}
		return result
	}

	var matches []string
	searchLower := strings.ToLower(searchTerm)

	// 从后往前搜索，显示最近的匹配
	for i := len(cmds) - 1; i >= 0; i-- {
		if strings.Contains(strings.ToLower(cmds[i]), searchLower) {
			matches = append(matches, cmds[i])
		}
	}

	return matches
}

// highlightMatch 高亮匹配的部分
func (p *Pool) highlightMatch(cmd, searchTerm string) string {
	if searchTerm == "" {
		return cmd
	}

	searchLower := strings.ToLower(searchTerm)
	cmdLower := strings.ToLower(cmd)

	var result strings.Builder
	lastIdx := 0

	for {
		idx := strings.Index(cmdLower[lastIdx:], searchLower)
		if idx == -1 {
			result.WriteString(cmd[lastIdx:])
			break
		}
		idx += lastIdx

		// 写入匹配前的部分
		result.WriteString(cmd[lastIdx:idx])
		// 高亮匹配部分 (使用ANSI颜色)
		result.WriteString("\x1b[1;33m") // 黄色加粗
		result.WriteString(cmd[idx : idx+len(searchTerm)])
		result.WriteString("\x1b[0m") // 重置

		lastIdx = idx + len(searchTerm)
	}

	return result.String()
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
