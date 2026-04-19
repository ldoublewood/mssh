package shell

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Executor 本地shell执行器
// 使用非交互式shell执行命令，但在mssh内部维护状态（当前目录、环境变量等）
type Executor struct {
	currentDir string
	envVars    map[string]string
	aliases    map[string]string // 存储从配置文件加载的别名
	shell      string
	homeDir    string
	username   string
	hostname   string
	mu         sync.Mutex
}

// NewExecutor 创建本地shell执行器
func NewExecutor() *Executor {
	// 获取当前用户信息
	usr, _ := user.Current()
	username := usr.Username
	homeDir := usr.HomeDir
	
	// 获取主机名
	hostname, _ := os.Hostname()
	
	// 获取当前目录
	currentDir, _ := os.Getwd()
	if currentDir == "" {
		currentDir = homeDir
	}
	
	// 获取用户shell
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}
	
	e := &Executor{
		currentDir: currentDir,
		envVars:    make(map[string]string),
		aliases:    make(map[string]string),
		shell:      shell,
		homeDir:    homeDir,
		username:   username,
		hostname:   hostname,
	}

	// 加载用户别名
	e.loadAliases()

	return e
}

// loadAliases 从用户的shell配置文件中加载别名
func (e *Executor) loadAliases() {
	shellName := "bash"
	if strings.Contains(e.shell, "zsh") {
		shellName = "zsh"
	}

	var rcFile string
	if shellName == "zsh" {
		rcFile = filepath.Join(e.homeDir, ".zshrc")
	} else {
		rcFile = filepath.Join(e.homeDir, ".bashrc")
	}

	file, err := os.Open(rcFile)
	if err != nil {
		return // 文件不存在，忽略
	}
	defer file.Close()

	// 解析别名定义: alias name='value' 或 alias name="value"
	aliasRegex := regexp.MustCompile(`^alias\s+([^=]+)=['"]([^'"]*)['"]`)
	aliasRegex2 := regexp.MustCompile(`^alias\s+([^=]+)=(\S+)`)

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// 跳过注释和空行
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// 尝试匹配 alias name='value' 格式
		if matches := aliasRegex.FindStringSubmatch(line); len(matches) == 3 {
			name := strings.TrimSpace(matches[1])
			value := matches[2]
			e.aliases[name] = value
			continue
		}

		// 尝试匹配 alias name=value 格式（无引号）
		if matches := aliasRegex2.FindStringSubmatch(line); len(matches) == 3 {
			name := strings.TrimSpace(matches[1])
			value := matches[2]
			e.aliases[name] = value
		}
	}
}

// Execute 执行命令
func (e *Executor) Execute(input string) error {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil
	}

	// 在mssh内部进行别名替换
	input = e.expandAlias(input)

	// 解析命令
	args := parseCommand(input)
	if len(args) == 0 {
		return nil
	}

	cmdName := args[0]

	// 检查是否需要特殊处理的内置命令
	switch cmdName {
	case "cd":
		return e.handleCd(args[1:])
	case "pwd":
		return e.handlePwd()
	case "export":
		return e.handleExport(args[1:])
	case "unset":
		return e.handleUnset(args[1:])
	case "env":
		return e.handleEnv()
	case "alias":
		return e.handleAlias(args[1:])
	}

	// 其他命令通过shell执行
	return e.executeWithShell(input)
}

// expandAlias 在mssh内部展开别名
func (e *Executor) expandAlias(input string) string {
	e.mu.Lock()
	defer e.mu.Unlock()

	// 获取命令名（第一个词）
	args := parseCommand(input)
	if len(args) == 0 {
		return input
	}

	cmdName := args[0]

	// 检查是否是别名
	if aliasCmd, ok := e.aliases[cmdName]; ok {
		// 替换命令名
		if len(args) > 1 {
			// 有别名 + 参数
			return aliasCmd + " " + strings.Join(args[1:], " ")
		}
		// 只有别名
		return aliasCmd
	}

	return input
}

// executeWithShell 使用shell执行命令
func (e *Executor) executeWithShell(command string) error {
	e.mu.Lock()
	currentDir := e.currentDir
	envVars := make(map[string]string)
	for k, v := range e.envVars {
		envVars[k] = v
	}
	shell := e.shell
	homeDir := e.homeDir
	e.mu.Unlock()

	// 构建环境变量设置
	envSetup := ""
	for k, v := range envVars {
		escaped := strings.ReplaceAll(v, "'", "'\"'\"'")
		envSetup += fmt.Sprintf("export %s='%s'; ", k, escaped)
	}

	// 构建完整命令：加载配置 + 执行命令
	// 注意：别名已经在mssh内部展开
	var rcLoad string
	if strings.Contains(shell, "zsh") {
		rcLoad = fmt.Sprintf("source '%s/.zshrc' 2>/dev/null; ", homeDir)
	} else {
		rcLoad = fmt.Sprintf("source '%s/.bashrc' 2>/dev/null; ", homeDir)
	}

	fullCommand := rcLoad + envSetup + command

	// 执行命令
	cmd := exec.Command(shell, "-c", fullCommand)
	cmd.Dir = currentDir
	cmd.Env = os.Environ()
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// handleCd 处理cd命令
func (e *Executor) handleCd(args []string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	
	var targetDir string
	if len(args) == 0 {
		targetDir = e.homeDir
	} else {
		targetDir = args[0]
		// 处理 ~ 展开
		if strings.HasPrefix(targetDir, "~") {
			targetDir = e.homeDir + targetDir[1:]
		}
	}
	
	// 转换为绝对路径
	if !filepath.IsAbs(targetDir) {
		targetDir = filepath.Join(e.currentDir, targetDir)
	}
	
	// 清理路径
	targetDir = filepath.Clean(targetDir)
	
	// 检查目录是否存在
	info, err := os.Stat(targetDir)
	if err != nil {
		return fmt.Errorf("cd: %s: 没有那个文件或目录", args[0])
	}
	if !info.IsDir() {
		return fmt.Errorf("cd: %s: 不是目录", args[0])
	}
	
	e.currentDir = targetDir
	return nil
}

// handlePwd 处理pwd命令
func (e *Executor) handlePwd() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	fmt.Println(e.currentDir)
	return nil
}

// handleExport 处理export命令
func (e *Executor) handleExport(args []string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	
	if len(args) == 0 {
		// 显示所有环境变量
		for k, v := range e.envVars {
			fmt.Printf("declare -x %s=\"%s\"\n", k, v)
		}
		return nil
	}
	
	for _, arg := range args {
		parts := strings.SplitN(arg, "=", 2)
		key := parts[0]
		var value string
		if len(parts) == 2 {
			value = parts[1]
			value = strings.Trim(value, "\"'")
		}
		e.envVars[key] = value
		os.Setenv(key, value)
	}
	
	return nil
}

// handleUnset 处理unset命令
func (e *Executor) handleUnset(args []string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	
	for _, key := range args {
		delete(e.envVars, key)
		os.Unsetenv(key)
	}
	return nil
}

// handleEnv 处理env命令
func (e *Executor) handleEnv() error {
	for _, env := range os.Environ() {
		fmt.Println(env)
	}
	return nil
}

// handleAlias 处理alias命令
func (e *Executor) handleAlias(args []string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if len(args) == 0 {
		// 显示所有已加载的别名
		if len(e.aliases) == 0 {
			fmt.Println("没有加载到别名")
			return nil
		}
		fmt.Println("已加载的别名:")
		for name, value := range e.aliases {
			fmt.Printf("  %s='%s'\n", name, value)
		}
		return nil
	}

	// 设置新别名
	for _, arg := range args {
		parts := strings.SplitN(arg, "=", 2)
		name := parts[0]
		if len(parts) == 2 {
			value := parts[1]
			value = strings.Trim(value, "\"'")
			e.aliases[name] = value
		} else {
			// 查询别名
			if value, ok := e.aliases[name]; ok {
				fmt.Printf("%s='%s'\n", name, value)
			} else {
				fmt.Printf("alias: %s: 未找到\n", name)
			}
		}
	}
	return nil
}

// GetPrompt 获取提示符
func (e *Executor) GetPrompt() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	
	// 构建提示符: [mssh] user@hostname:current_dir$
	displayDir := e.currentDir
	if strings.HasPrefix(displayDir, e.homeDir) {
		displayDir = "~" + strings.TrimPrefix(displayDir, e.homeDir)
	}
	
	return fmt.Sprintf("[mssh] %s@%s:%s$ ", e.username, e.hostname, displayDir)
}

// GetCurrentDir 获取当前目录
func (e *Executor) GetCurrentDir() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.currentDir
}

// Close 清理资源
func (e *Executor) Close() error {
	return nil
}

// parseCommand 解析命令
func parseCommand(input string) []string {
	var args []string
	var current strings.Builder
	inQuote := false
	quoteChar := rune(0)
	
	for _, ch := range input {
		switch {
		case !inQuote && (ch == '"' || ch == '\''):
			inQuote = true
			quoteChar = ch
		case inQuote && ch == quoteChar:
			inQuote = false
			quoteChar = 0
		case !inQuote && ch == ' ':
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(ch)
		}
	}
	
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	
	return args
}
