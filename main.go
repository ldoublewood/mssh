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
	"mssh/ssh"
)

const (
	defaultHostsFile     = "hosts.ini"
	defaultPasswordsFile = "passwords.ini"
)

func main() {
	var (
		hostsFile     = flag.String("c", defaultHostsFile, "主机配置文件路径")
		passwordsFile = flag.String("p", defaultPasswordsFile, "密码配置文件路径")
		sequential    = flag.Bool("s", false, "使用顺序模式（默认并发）")
	)
	flag.Parse()

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
		fmt.Println("\n  3. 运行: mssh -c hosts.ini")
		os.Exit(1)
	}

	// 加载配置
	cfg := config.NewConfig()
	if err := cfg.LoadHosts(*hostsFile); err != nil {
		fmt.Fprintf(os.Stderr, "加载hosts文件失败: %v\n", err)
		os.Exit(1)
	}

	// 加载密码（可选）
	if _, err := os.Stat(*passwordsFile); err == nil {
		if err := cfg.LoadPasswords(*passwordsFile); err != nil {
			fmt.Fprintf(os.Stderr, "加载密码文件失败: %v\n", err)
			os.Exit(1)
		}
	}

	// 初始化历史管理器
	histDir := filepath.Join(os.Getenv("HOME"), ".mssh_history")
	hist, err := history.NewManager(histDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "初始化历史管理器失败: %v\n", err)
		os.Exit(1)
	}

	// 初始化SSH连接池
	pool := ssh.NewPool()
	defer pool.Close()

	// 初始化命令执行器
	executor := command.NewExecutor(cfg, pool, hist)
	if *sequential {
		executor.SetConcurrent(false)
	}

	// 设置自动完成
	completer := newCompleter(cfg)

	// 初始化readline
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

	// 加载历史
	hist.SetHost("")
	if commands, err := hist.LoadHistory(); err == nil {
		for _, cmd := range commands {
			rl.SaveHistory(cmd)
		}
	}

	// 设置信号处理
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\n正在退出...")
		executor.Cleanup()
		os.Exit(0)
	}()

	// 欢迎信息
	fmt.Println("多机SSH客户端 (mssh)")
	fmt.Printf("已加载 %d 个主机, %d 个组\n", len(cfg.Hosts), len(cfg.Groups))
	if executor.IsConcurrent() {
		fmt.Println("模式: 并发")
	} else {
		fmt.Println("模式: 顺序")
	}
	fmt.Println("输入 'help' 查看帮助")
	fmt.Println()

	// 主循环
	for {
		// 更新提示符
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
		return filter(commands, words[len(words)-1]), len(words[len(words)-1])
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
