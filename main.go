package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/chzyer/readline"

	"mssh/command"
	"mssh/config"
	"mssh/history"
	"mssh/internal/daemon"
	"mssh/mcp"
	"mssh/ssh"
)

const (
	msshDir           = ".mssh"
	hostsFileName     = "hosts.ini"
	passwordsFileName = "passwords.ini"
)

func main() {
	var (
		hostsFile     = flag.String("c", "", "主机配置文件路径")
		passwordsFile = flag.String("p", "", "密码配置文件路径")
		sequential    = flag.Bool("s", false, "使用顺序模式（默认并发）")
		asDaemon      = flag.Bool("daemon", false, "以后台daemon模式运行")
		asMCP         = flag.Bool("mcp", false, "以MCP服务器模式运行（stdio通信）")
		keepalive     = flag.Duration("keepalive", daemon.DefaultIdleTimeout, "连接保持时长（如 5m, 30s），0s 禁用")
		noKeepalive   = flag.Bool("no-keepalive", false, "禁用连接保持功能")
	)
	flag.Parse()

	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = os.Getenv("HOME")
	}

	if *hostsFile == "" {
		cwd, _ := os.Getwd()
		localPath := filepath.Join(cwd, msshDir, hostsFileName)
		homePath := filepath.Join(homeDir, msshDir, hostsFileName)
		if _, err := os.Stat(localPath); err == nil {
			*hostsFile = localPath
		} else {
			*hostsFile = homePath
		}
	}

	if *passwordsFile == "" {
		cwd, _ := os.Getwd()
		localPath := filepath.Join(cwd, msshDir, passwordsFileName)
		homePath := filepath.Join(homeDir, msshDir, passwordsFileName)
		if _, err := os.Stat(localPath); err == nil {
			*passwordsFile = localPath
		} else {
			*passwordsFile = homePath
		}
	}

	// daemon 模式：后台运行，持有连接池，通过 socket 接受命令
	if *asDaemon {
		srv, err := daemon.NewServer(*hostsFile, *passwordsFile, *sequential, *keepalive)
		if err != nil {
			fmt.Fprintf(os.Stderr, "启动daemon失败: %v\n", err)
			os.Exit(1)
		}
		if err := srv.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "daemon运行错误: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// MCP 模式：通过 stdio 提供 MCP 服务，供 Claude Code 等客户端调用
	if *asMCP {
		runMCP(*hostsFile, *passwordsFile)
		return
	}

	// 检查hosts文件
	if _, err := os.Stat(*hostsFile); err != nil {
		fmt.Fprintf(os.Stderr, "错误: hosts文件 '%s' 不存在\n", *hostsFile)
		fmt.Println("\n使用说明:")
		fmt.Println("  1. 创建 hosts.ini 文件，格式如下:")
		fmt.Println("     [webservers]")
		fmt.Println("     web1 = user@192.168.1.10:22")
		fmt.Println("     web2 = user@192.168.1.11")
		fmt.Println("     [dbservers]")
		fmt.Println("     db1 = user@192.168.1.20")
		fmt.Println("\n  2. 可选创建 passwords.ini 文件（免密登录可跳过）:")
		fmt.Println("     web1 = password123")
		fmt.Println("     web2 = password456")
		fmt.Println("\n  3. 放置到 .mssh/hosts.ini（当前目录或 ~ 目录均可）")
		fmt.Println("  4. 运行: mssh")
		os.Exit(1)
	}

	// 单次命令模式: mssh [flags] host: command
	if args := flag.Args(); len(args) > 0 {
		cmd := strings.Join(args, " ")

		// 登录命令（host:）需要交互式 TTY，不能通过 daemon 执行
		if !isLoginCommand(cmd) && !*noKeepalive && *keepalive > 0 {
			// 通过 daemon 执行，复用连接
			resp, err := daemon.SendCommand(*hostsFile, *passwordsFile, cmd, *sequential, *keepalive)
			if err != nil {
				fmt.Fprintf(os.Stderr, "错误: %v\n", err)
				os.Exit(1)
			}
			if resp.Output != "" {
				fmt.Print(resp.Output)
			}
			if resp.Error != "" {
				fmt.Fprintf(os.Stderr, "错误: %s\n", resp.Error)
			}
			os.Exit(resp.ExitCode)
		}
		// 不使用 keepalive（或登录命令），直接执行
		runOneShot(*hostsFile, *passwordsFile, *sequential, cmd)
		return
	}

	// 交互模式
	runInteractive(*hostsFile, *passwordsFile, *sequential)
}

// isLoginCommand 判断命令是否是登录命令（host: 格式，需要交互式 TTY）
func isLoginCommand(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	return strings.HasSuffix(cmd, ":") && !strings.Contains(cmd, " ")
}

// runMCP 启动 MCP Server 模式，通过 stdio 与客户端通信
func runMCP(hostsFile, passwordsFile string) {
	cfg := config.NewConfig()
	if err := cfg.LoadHosts(hostsFile); err != nil {
		fmt.Fprintf(os.Stderr, "加载hosts文件失败: %v\n", err)
		os.Exit(1)
	}
	if _, err := os.Stat(passwordsFile); err == nil {
		cfg.LoadPasswords(passwordsFile)
	}

	pool := ssh.NewPool()
	defer pool.Close()

	server := mcp.NewServer(cfg, pool)
	if err := server.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "MCP服务器错误: %v\n", err)
		os.Exit(1)
	}
}

// runOneShot 直接执行单次命令（不使用 daemon）
func runOneShot(hostsFile, passwordsFile string, sequential bool, cmd string) {
	cfg := config.NewConfig()
	if err := cfg.LoadHosts(hostsFile); err != nil {
		fmt.Fprintf(os.Stderr, "加载hosts文件失败: %v\n", err)
		os.Exit(1)
	}
	if _, err := os.Stat(passwordsFile); err == nil {
		cfg.LoadPasswords(passwordsFile)
	}

	histDir := filepath.Join(os.Getenv("HOME"), ".mssh")
	hist, _ := history.NewManager(histDir)
	pool := ssh.NewPool()
	defer pool.Close()

	executor := command.NewExecutor(cfg, pool, hist)
	if sequential {
		executor.SetConcurrent(false)
	}

	rl, err := readline.NewEx(&readline.Config{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "初始化readline失败: %v\n", err)
		os.Exit(1)
	}
	defer rl.Close()
	executor.SetReadline(rl)

	if err := executor.Execute(cmd); err != nil {
		if err.Error() != "EXIT" {
			fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		}
		os.Exit(1)
	}
}

// runInteractive 启动交互式 REPL
func runInteractive(hostsFile, passwordsFile string, sequential bool) {
	cfg := config.NewConfig()
	if err := cfg.LoadHosts(hostsFile); err != nil {
		fmt.Fprintf(os.Stderr, "加载hosts文件失败: %v\n", err)
		os.Exit(1)
	}
	if _, err := os.Stat(passwordsFile); err == nil {
		cfg.LoadPasswords(passwordsFile)
	}

	histDir := filepath.Join(os.Getenv("HOME"), ".mssh")
	hist, err := history.NewManager(histDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "初始化历史管理器失败: %v\n", err)
		os.Exit(1)
	}

	pool := ssh.NewPool()
	defer pool.Close()

	executor := command.NewExecutor(cfg, pool, hist)
	if sequential {
		executor.SetConcurrent(false)
	}

	completer := newCompleter(cfg)

	rl, err := readline.NewEx(&readline.Config{
		Prompt:            executor.GetPrompt(),
		HistoryFile:       filepath.Join(histDir, "local_history.txt"),
		AutoComplete:      completer,
		InterruptPrompt:   "^C",
		EOFPrompt:         "exit",
		HistorySearchFold: true,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "初始化readline失败: %v\n", err)
		os.Exit(1)
	}
	defer rl.Close()

	executor.SetReadline(rl)

	hist.SetHost("")
	if commands, err := hist.LoadHistory(); err == nil {
		for _, cmd := range commands {
			rl.SaveHistory(cmd)
		}
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\n正在退出...")
		executor.Cleanup()
		os.Exit(0)
	}()

	fmt.Println("多机SSH客户端 (mssh)")
	fmt.Printf("已加载 %d 个主机, %d 个组\n", len(cfg.Hosts), len(cfg.Groups))
	if executor.IsConcurrent() {
		fmt.Println("模式: 并发")
	} else {
		fmt.Println("模式: 顺序")
	}
	fmt.Println("输入 'help' 查看帮助")
	fmt.Println()

	for {
		rl.SetPrompt(executor.GetPrompt())

		line, err := rl.Readline()
		if err != nil {
			if err == readline.ErrInterrupt {
				continue
			}
			break
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if err := executor.Execute(line); err != nil {
			if err.Error() == "EXIT" {
				break
			}
			fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		}
	}
}

// completer 自动完成
type completer struct {
	cfg *config.Config
}

func newCompleter(cfg *config.Config) *completer {
	return &completer{cfg: cfg}
}

func (c *completer) Do(line []rune, pos int) ([][]rune, int) {
	prefix := string(line[:pos])
	words := strings.Fields(prefix)

	// 命令自动完成
	if len(words) == 0 || (len(words) == 1 && !strings.HasSuffix(prefix, " ")) {
		commands := []string{
			"help", "hosts", "groups", "exit", "quit",
			"concurrent", "sequential", "put", "get",
		}
		for _, host := range c.cfg.GetAllHostNames() {
			commands = append(commands, host+":")
		}
		for _, group := range c.cfg.GetAllGroupNames() {
			commands = append(commands, group+":")
		}
				lastWord := ""
		if len(words) > 0 {
			lastWord = words[len(words)-1]
		}
		return filter(commands, lastWord), len(lastWord)
	}

	// put/get 命令的文件路径补全
	if len(words) >= 1 && (words[0] == "put" || words[0] == "get") {
		// 简单的主机名补全
		if strings.HasSuffix(prefix, " ") || len(words) >= 2 {
			suggestions := [][]rune{}
			for _, host := range c.cfg.GetAllHostNames() {
				suggestions = append(suggestions, []rune(host+":"))
			}
			return suggestions, 0
		}
	}

	return nil, 0
}

func filter(options []string, prefix string) [][]rune {
	var result [][]rune
	for _, opt := range options {
		if strings.HasPrefix(opt, prefix) {
			result = append(result, []rune(opt))
		}
	}
	return result
}
