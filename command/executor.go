package command

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/chzyer/readline"
	cryptossh "golang.org/x/crypto/ssh"

	"mssh/config"
	"mssh/history"
	"mssh/shell"
	"mssh/ssh"
	"mssh/transfer"
)

// Executor 命令执行器
type Executor struct {
	config      *config.Config
	pool        *ssh.Pool
	history     *history.Manager
	transfer    *transfer.TransferManager
	localShell  *shell.Executor
	rl          *readline.Instance
	concurrent  bool
	currentHost string // 当前登录的主机（用于交互模式）
}

// NewExecutor 创建命令执行器
func NewExecutor(cfg *config.Config, pool *ssh.Pool, hist *history.Manager) *Executor {
	return &Executor{
		config:     cfg,
		pool:       pool,
		history:    hist,
		transfer:   transfer.NewTransferManager(pool),
		localShell: shell.NewExecutor(),
		concurrent: true,
	}
}

// SetReadline 设置readline实例
func (e *Executor) SetReadline(rl *readline.Instance) {
	e.rl = rl
}

// Execute 执行命令
func (e *Executor) Execute(input string) error {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil
	}

	// 保存历史命令（非远程命令）
	if !e.isRemoteCommand(input) {
		e.history.SaveCommand(input)
	}

	// 解析命令类型
	if e.isBuiltInCommand(input) {
		return e.executeBuiltIn(input)
	}

	// 检查是否是远程命令格式: host: command 或 group: command
	if hostOrGroup, cmd, ok := e.parseRemoteCommand(input); ok {
		return e.executeRemoteCommand(hostOrGroup, cmd)
	}

	// 检查是否是登录命令: host:
	if host, ok := e.parseLoginCommand(input); ok {
		return e.executeLogin(host)
	}

	// 检查是否是传输命令
	if ok, direction, hostOrGroup, localPath, remotePath := e.parseTransferCommand(input); ok {
		return e.executeTransfer(direction, hostOrGroup, localPath, remotePath)
	}

	// 作为本地系统命令执行
	return e.executeLocalCommand(input)
}

// isRemoteCommand 检查是否是远程命令
func (e *Executor) isRemoteCommand(input string) bool {
	if _, _, ok := e.parseRemoteCommand(input); ok {
		return true
	}
	if _, ok := e.parseLoginCommand(input); ok {
		return true
	}
	return false
}

// isBuiltInCommand 检查是否是内置命令
func (e *Executor) isBuiltInCommand(input string) bool {
	builtins := []string{"help", "hosts", "groups", "exit", "quit", "q", "concurrent", "sequential"}
	cmd := strings.Fields(input)[0]
	for _, b := range builtins {
		if cmd == b {
			return true
		}
	}
	return false
}

// executeBuiltIn 执行内置命令
func (e *Executor) executeBuiltIn(input string) error {
	parts := strings.Fields(input)
	if len(parts) == 0 {
		return nil
	}

	cmd := parts[0]
	switch cmd {
	case "help":
		e.showHelp()
	case "hosts":
		e.listHosts()
	case "groups":
		e.listGroups()
	case "exit", "quit", "q":
		return fmt.Errorf("EXIT")
	case "concurrent":
		e.concurrent = true
		fmt.Println("已切换到并发模式")
	case "sequential":
		e.concurrent = false
		fmt.Println("已切换到顺序模式")
	}
	return nil
}

// parseRemoteCommand 解析远程命令: host: command 或 group: command
func (e *Executor) parseRemoteCommand(input string) (hostOrGroup, command string, ok bool) {
	idx := strings.Index(input, ":")
	if idx == -1 {
		return "", "", false
	}

	// 检查是否是登录命令（后面没有命令内容）
	if idx == len(input)-1 || strings.TrimSpace(input[idx+1:]) == "" {
		return "", "", false
	}

	hostOrGroup = strings.TrimSpace(input[:idx])
	command = strings.TrimSpace(input[idx+1:])

	// 验证host或group是否存在
	if !e.config.HostExists(hostOrGroup) && !e.config.GroupExists(hostOrGroup) {
		return "", "", false
	}

	return hostOrGroup, command, true
}

// parseLoginCommand 解析登录命令: host:
func (e *Executor) parseLoginCommand(input string) (string, bool) {
	if !strings.HasSuffix(input, ":") {
		return "", false
	}

	host := strings.TrimSuffix(input, ":")
	host = strings.TrimSpace(host)

	// 只能登录单台主机，不能是组
	if !e.config.HostExists(host) {
		return "", false
	}

	return host, true
}

// parseTransferCommand 解析传输命令
func (e *Executor) parseTransferCommand(input string) (ok bool, direction, hostOrGroup, localPath, remotePath string) {
	// 格式: put localpath host:remotepath 或 get host:remotepath localpath
	parts := strings.Fields(input)
	if len(parts) != 3 {
		return false, "", "", "", ""
	}

	if parts[0] == "put" {
		direction = "put"
		localPath = parts[1]
		// 解析 host:remotepath
		idx := strings.Index(parts[2], ":")
		if idx == -1 {
			return false, "", "", "", ""
		}
		hostOrGroup = parts[2][:idx]
		remotePath = parts[2][idx+1:]
	} else if parts[0] == "get" {
		direction = "get"
		// 解析 host:remotepath
		idx := strings.Index(parts[1], ":")
		if idx == -1 {
			return false, "", "", "", ""
		}
		hostOrGroup = parts[1][:idx]
		remotePath = parts[1][idx+1:]
		localPath = parts[2]
	} else {
		return false, "", "", "", ""
	}

	// 验证host或group存在
	if !e.config.HostExists(hostOrGroup) && !e.config.GroupExists(hostOrGroup) {
		return false, "", "", "", ""
	}

	return true, direction, hostOrGroup, localPath, remotePath
}

// executeRemoteCommand 执行远程命令
func (e *Executor) executeRemoteCommand(hostOrGroup, command string) error {
	var hosts []*config.Host
	var err error

	if e.config.GroupExists(hostOrGroup) {
		hosts, err = e.config.GetHostsByGroup(hostOrGroup)
		if err != nil {
			return err
		}
	} else {
		host, _ := e.config.GetHost(hostOrGroup)
		hosts = []*config.Host{host}
	}

	if len(hosts) == 0 {
		return fmt.Errorf("没有可用的主机")
	}

	if len(hosts) == 1 || !e.concurrent {
		// 顺序执行
		for _, host := range hosts {
			fmt.Printf("\n=== [%s] %s ===\n", host.Name, command)
			output, err := e.pool.ExecuteWithOutput(host, command)
			if err != nil {
				fmt.Printf("错误: %v\n", err)
			} else {
				fmt.Print(output)
			}
		}
	} else {
		// 并发执行
		var wg sync.WaitGroup
		results := make(map[string]struct {
			output string
			err    error
		})
		var mu sync.Mutex

		for _, host := range hosts {
			wg.Add(1)
			go func(h *config.Host) {
				defer wg.Done()
				output, err := e.pool.ExecuteWithOutput(h, command)
				mu.Lock()
				results[h.Name] = struct {
					output string
					err    error
				}{output, err}
				mu.Unlock()
			}(host)
		}

		wg.Wait()

		// 统一输出结果
		for _, host := range hosts {
			fmt.Printf("\n=== [%s] %s ===\n", host.Name, command)
			result := results[host.Name]
			if result.err != nil {
				fmt.Printf("错误: %v\n", result.err)
			} else {
				fmt.Print(result.output)
			}
		}
	}

	return nil
}

// executeLogin 登录单台主机
func (e *Executor) executeLogin(hostName string) error {
	host, exists := e.config.GetHost(hostName)
	if !exists {
		return fmt.Errorf("主机 '%s' 不存在", hostName)
	}

	fmt.Printf("正在登录 %s (%s@%s:%d)...\n", host.Name, host.User, host.IP, host.Port)

	e.currentHost = hostName
	e.history.SetHost(hostName)

	// 创建并启动rsync历史同步器
	homeDir, _ := os.UserHomeDir()
	historyBaseDir := filepath.Join(homeDir, ".mssh_history")
	rsyncer := history.NewRsyncSyncer(host, historyBaseDir)
	rsyncer.Start()

	// 进入交互模式
	oldPrompt := e.rl.Config.Prompt
	e.rl.Config.Prompt = fmt.Sprintf("[%s] ", hostName)

	// 启动远程shell
	err := e.pool.StartShell(host)

	// 停止同步器并执行最后一次同步
	// 注意：同步错误只记录到日志，不影响退出流程
	rsyncer.Stop()

	// 恢复提示符
	e.rl.Config.Prompt = oldPrompt
	e.currentHost = ""
	e.history.SetHost("")

	// 忽略 StartShell 的退出错误，因为这是用户正常退出 shell
	// 错误可能包括：exit status 0, exit status 1, connection closed 等
	if err != nil {
		// 检查是否是 SSH 退出错误类型
		if _, ok := err.(*cryptossh.ExitError); ok {
			// SSH 会话正常退出，忽略错误
			return nil
		}
		// 对于其他错误（如连接关闭），也视为正常退出
		return nil
	}

	return nil
}

// executeTransfer 执行文件传输
func (e *Executor) executeTransfer(direction, hostOrGroup, localPath, remotePath string) error {
	var hosts []*config.Host
	var err error

	if e.config.GroupExists(hostOrGroup) {
		hosts, err = e.config.GetHostsByGroup(hostOrGroup)
		if err != nil {
			return err
		}
	} else {
		host, _ := e.config.GetHost(hostOrGroup)
		hosts = []*config.Host{host}
	}

	if direction == "put" {
		// 上传到远程
		if _, err := os.Stat(localPath); err != nil {
			return fmt.Errorf("本地文件不存在: %s", localPath)
		}
		fmt.Printf("上传 %s 到 %d 台主机...\n", localPath, len(hosts))
		return e.transfer.Upload(hosts, localPath, remotePath, e.concurrent)
	} else {
		// 从远程下载（仅支持单台）
		if len(hosts) > 1 {
			return fmt.Errorf("下载操作仅支持单台主机，请指定具体主机而非组")
		}
		fmt.Printf("从 %s 下载 %s 到 %s...\n", hosts[0].Name, remotePath, localPath)
		return e.transfer.Download(hosts[0], remotePath, localPath)
	}
}

// executeLocalCommand 执行本地系统命令
// 使用本地 shell 执行器，支持所有 shell 内置命令和状态保持
func (e *Executor) executeLocalCommand(input string) error {
	return e.localShell.Execute(input)
}

// showHelp 显示帮助信息
func (e *Executor) showHelp() {
	help := `
多机SSH客户端 - 帮助信息

命令格式:
  host: command       - 在指定主机执行命令
  group: command      - 在主机组的所有主机执行命令
  host:               - 登录到指定主机（交互模式）
  put local remote    - 上传文件到主机或组 (格式: put /path/file host:/path/file)
  get host:remote local - 从主机下载文件 (仅支持单台)

内置命令:
  help                - 显示此帮助
  hosts               - 列出所有主机
  groups              - 列出所有组
  concurrent          - 切换到并发模式（默认）
  sequential          - 切换到顺序模式
  exit/quit/q         - 退出程序

示例:
  web1: uname -a                    - 在web1执行命令
  webservers: systemctl status nginx - 在webservers组执行命令
  db1:                              - 登录到db1
  put ./config.ini web1:/etc/app/   - 上传文件
  get web1:/var/log/app.log ./      - 下载文件

配置文件:
  hosts.ini  - 主机配置 (用户名@IP:端口)
  passwords.ini - 密码配置（可选，支持免密登录时不需要）
`
	fmt.Println(help)
}

// listHosts 列出所有主机
func (e *Executor) listHosts() {
	fmt.Println("主机列表:")
	for _, name := range e.config.GetAllHostNames() {
		host, _ := e.config.GetHost(name)
		fmt.Printf("  %s -> %s@%s:%d\n", name, host.User, host.IP, host.Port)
	}
}

// listGroups 列出所有组
func (e *Executor) listGroups() {
	fmt.Println("主机组列表:")
	for _, name := range e.config.GetAllGroupNames() {
		group, _ := e.config.GetGroup(name)
		fmt.Printf("  %s: hosts=%v\n", name, group.Hosts)
	}
}

// GetPrompt 获取当前提示符
// 本地模式显示 [mssh] 前缀的 shell 提示符
// 远程模式显示 [hostname] 前缀
func (e *Executor) GetPrompt() string {
	if e.currentHost != "" {
		return fmt.Sprintf("[%s] ", e.currentHost)
	}
	// 使用本地 shell 的提示符
	return e.localShell.GetPrompt()
}

// IsInRemoteMode 是否在远程交互模式
func (e *Executor) IsInRemoteMode() bool {
	return e.currentHost != ""
}

// Cleanup 清理资源
func (e *Executor) Cleanup() {
	e.pool.Close()
}

// SetConcurrent 设置并发模式
func (e *Executor) SetConcurrent(concurrent bool) {
	e.concurrent = concurrent
}

// IsConcurrent 获取并发模式
func (e *Executor) IsConcurrent() bool {
	return e.concurrent
}
