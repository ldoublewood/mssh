package shell

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
)

// Executor 本地shell执行器
// 维护shell状态（当前目录、环境变量等）
type Executor struct {
	currentDir string
	envVars    map[string]string
	aliases    map[string]string
	mu         sync.RWMutex
	shell      string
	homeDir    string
	username   string
	hostname   string
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
	
	return &Executor{
		currentDir: currentDir,
		envVars:    make(map[string]string),
		aliases:    make(map[string]string),
		shell:      shell,
		homeDir:    homeDir,
		username:   username,
		hostname:   hostname,
	}
}

// Execute 执行命令
func (e *Executor) Execute(input string) error {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil
	}
	
	// 解析命令
	args := parseCommand(input)
	if len(args) == 0 {
		return nil
	}
	
	cmdName := args[0]
	
	// 检查是否是内置命令需要特殊处理
	if handler, ok := e.getBuiltinHandler(cmdName); ok {
		return handler(args[1:])
	}
	
	// 使用shell执行命令
	return e.executeWithShell(input)
}

// executeWithShell 使用用户的shell执行命令
func (e *Executor) executeWithShell(command string) error {
	e.mu.RLock()
	currentDir := e.currentDir
	envVars := make(map[string]string)
	for k, v := range e.envVars {
		envVars[k] = v
	}
	e.mu.RUnlock()
	
	// 创建命令
	cmd := exec.Command(e.shell, "-c", command)
	
	// 设置工作目录
	cmd.Dir = currentDir
	
	// 设置环境变量
	cmd.Env = os.Environ()
	for k, v := range envVars {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}
	
	// 设置标准IO
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	
	return cmd.Run()
}

// getBuiltinHandler 获取内置命令处理器
func (e *Executor) getBuiltinHandler(name string) (func([]string) error, bool) {
	builtins := map[string]func([]string) error{
		"cd":   e.handleCd,
		"pwd":  e.handlePwd,
		"export": e.handleExport,
		"unset":  e.handleUnset,
		"env":    e.handleEnv,
		"set":    e.handleSet,
		"alias":  e.handleAlias,
		"unalias": e.handleUnalias,
		"echo":   e.handleEcho,
		"printf": e.handlePrintf,
		"type":   e.handleType,
		"which":  e.handleWhich,
		"source": e.handleSource,
		".":      e.handleSource, // source 的别名
	}
	
	handler, ok := builtins[name]
	return handler, ok
}

// handleCd 处理cd命令
func (e *Executor) handleCd(args []string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	
	var targetDir string
	if len(args) == 0 {
		// 没有参数时切换到HOME目录
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
func (e *Executor) handlePwd(args []string) error {
	e.mu.RLock()
	defer e.mu.RUnlock()
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
	
	// 解析 VAR=value 格式
	for _, arg := range args {
		parts := strings.SplitN(arg, "=", 2)
		key := parts[0]
		var value string
		if len(parts) == 2 {
			value = parts[1]
			// 去除可能的引号
			value = strings.Trim(value, "\"'")
		}
		e.envVars[key] = value
		// 同时设置到os.Environ，以便子进程继承
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
func (e *Executor) handleEnv(args []string) error {
	// 显示所有环境变量
	for _, env := range os.Environ() {
		fmt.Println(env)
	}
	return nil
}

// handleSet 处理set命令
func (e *Executor) handleSet(args []string) error {
	// 简单实现：显示所有变量
	return e.handleEnv(args)
}

// handleAlias 处理alias命令
func (e *Executor) handleAlias(args []string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	
	if len(args) == 0 {
		// 显示所有别名
		for name, value := range e.aliases {
			fmt.Printf("alias %s='%s'\n", name, value)
		}
		return nil
	}
	
	// 解析 name='value' 格式
	for _, arg := range args {
		parts := strings.SplitN(arg, "=", 2)
		name := parts[0]
		if len(parts) == 2 {
			value := parts[1]
			value = strings.Trim(value, "\"'")
			e.aliases[name] = value
		} else {
			// 查询特定别名
			if value, ok := e.aliases[name]; ok {
				fmt.Printf("alias %s='%s'\n", name, value)
			} else {
				return fmt.Errorf("alias: %s: 未找到", name)
			}
		}
	}
	
	return nil
}

// handleUnalias 处理unalias命令
func (e *Executor) handleUnalias(args []string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	
	for _, name := range args {
		delete(e.aliases, name)
	}
	return nil
}

// handleEcho 处理echo命令
func (e *Executor) handleEcho(args []string) error {
	output := make([]string, len(args))
	for i, arg := range args {
		// 展开变量
		arg = e.expandVariables(arg)
		output[i] = arg
	}
	fmt.Println(strings.Join(output, " "))
	return nil
}

// handlePrintf 处理printf命令
func (e *Executor) handlePrintf(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("printf: 缺少格式参数")
	}
	format := e.expandVariables(args[0])
	values := make([]interface{}, len(args)-1)
	for i, arg := range args[1:] {
		values[i] = e.expandVariables(arg)
	}
	fmt.Printf(format, values...)
	return nil
}

// handleType 处理type命令
func (e *Executor) handleType(args []string) error {
	for _, name := range args {
		// 检查是否是内置命令
		if _, ok := e.getBuiltinHandler(name); ok {
			fmt.Printf("%s 是 shell 内置命令\n", name)
			continue
		}
		
		// 检查是否是别名
		e.mu.RLock()
		if alias, ok := e.aliases[name]; ok {
			fmt.Printf("%s 是别名 %s\n", name, alias)
			e.mu.RUnlock()
			continue
		}
		e.mu.RUnlock()
		
		// 检查外部命令
		if path, err := exec.LookPath(name); err == nil {
			fmt.Printf("%s 是 %s\n", name, path)
		} else {
			fmt.Printf("bash: type: %s: 未找到\n", name)
		}
	}
	return nil
}

// handleWhich 处理which命令
func (e *Executor) handleWhich(args []string) error {
	return e.handleType(args)
}

// handleSource 处理source命令
func (e *Executor) handleSource(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("source: 需要文件名参数")
	}
	
	file := args[0]
	if !filepath.IsAbs(file) {
		file = filepath.Join(e.currentDir, file)
	}
	
	// 读取文件内容
	content, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("source: %s: %v", args[0], err)
	}
	
	// 逐行执行
	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if err := e.Execute(line); err != nil {
			return err
		}
	}
	
	return nil
}

// expandVariables 展开变量
func (e *Executor) expandVariables(s string) string {
	// 展开 $VAR 和 ${VAR}
	result := os.Expand(s, func(key string) string {
		e.mu.RLock()
		defer e.mu.RUnlock()
		if value, ok := e.envVars[key]; ok {
			return value
		}
		return os.Getenv(key)
	})
	return result
}

// GetPrompt 获取提示符
func (e *Executor) GetPrompt() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	
	// 构建类似 bash 的提示符: [mssh] user@hostname:current_dir$
	// 简化当前目录显示
	displayDir := e.currentDir
	if strings.HasPrefix(displayDir, e.homeDir) {
		displayDir = "~" + strings.TrimPrefix(displayDir, e.homeDir)
	}
	
	return fmt.Sprintf("[mssh] %s@%s:%s$ ", e.username, e.hostname, displayDir)
}

// GetCurrentDir 获取当前目录
func (e *Executor) GetCurrentDir() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.currentDir
}

// parseCommand 解析命令（简单实现，处理引号）
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
