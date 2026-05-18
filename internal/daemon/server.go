package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"mssh/command"
	"mssh/config"
	"mssh/history"
	"mssh/ssh"
)

// Server daemon 服务端，持有 SSH 连接池并处理客户端请求
type Server struct {
	cfg      *config.Config
	pool     *ssh.Pool
	executor *command.Executor

	socketPath  string
	idleTimeout time.Duration
	hostsFile   string
	passFile    string
	lockFile    *os.File

	mu         sync.Mutex
	listener   net.Listener
	lastActive time.Time
	stopCh     chan struct{}
}

// NewServer 创建 daemon 服务端
func NewServer(hostsFile, passwordsFile string, sequential bool, idleTimeout time.Duration) (*Server, error) {
	cfg := config.NewConfig()
	if err := cfg.LoadHosts(hostsFile); err != nil {
		return nil, fmt.Errorf("加载hosts文件失败: %v", err)
	}
	if _, err := os.Stat(passwordsFile); err == nil {
		if err := cfg.LoadPasswords(passwordsFile); err != nil {
			return nil, fmt.Errorf("加载密码文件失败: %v", err)
		}
	}

	pool := ssh.NewPool()
	histDir := os.Getenv("HOME") + "/.mssh_history"
	hist, err := history.NewManager(histDir)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("初始化历史管理器失败: %v", err)
	}

	executor := command.NewExecutor(cfg, pool, hist)
	if sequential {
		executor.SetConcurrent(false)
	}

	// 获取 PID 文件上的排他锁，防止多个 daemon 同时运行
	pidPath := PidPath()
	os.MkdirAll(filepath.Dir(pidPath), 0700)
	lockFile, err := os.OpenFile(pidPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("打开PID文件失败: %v", err)
	}
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lockFile.Close()
		pool.Close()
		return nil, fmt.Errorf("另一个daemon已在运行")
	}

	return &Server{
		cfg:         cfg,
		pool:        pool,
		executor:    executor,
		socketPath:  SocketPath(),
		idleTimeout: idleTimeout,
		hostsFile:   hostsFile,
		passFile:    passwordsFile,
		lockFile:    lockFile,
		stopCh:      make(chan struct{}),
	}, nil
}

// Run 启动 daemon，阻塞直到超时或收到停止信号
func (s *Server) Run() error {
	os.Remove(s.socketPath)

	var err error
	s.listener, err = net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("监听socket失败: %v", err)
	}
	defer os.Remove(s.socketPath)
	defer s.listener.Close()

	pidData := fmt.Appendf(nil, "%d\n%s\n%s", os.Getpid(), s.hostsFile, s.passFile)
	s.lockFile.Truncate(0)
	s.lockFile.Seek(0, 0)
	s.lockFile.Write(pidData)

	s.lastActive = time.Now()
	go s.idleChecker()

	for {
		select {
		case <-s.stopCh:
			return nil
		default:
		}

		s.listener.(*net.UnixListener).SetDeadline(time.Now().Add(time.Second))
		conn, err := s.listener.Accept()
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			select {
			case <-s.stopCh:
				return nil
			default:
			}
			return fmt.Errorf("接受连接失败: %v", err)
		}

		go s.handleConn(conn)
	}
}

// handleConn 处理单个客户端连接
func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()

	s.mu.Lock()
	s.lastActive = time.Now()
	s.mu.Unlock()

	var req Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		resp := Response{Error: fmt.Sprintf("解析请求失败: %v", err), ExitCode: 1}
		json.NewEncoder(conn).Encode(&resp)
		return
	}

	if req.Command == "__STOP__" {
		s.Stop()
		return
	}

	output, execErr := s.executeCaptured(req.Command)

	resp := Response{Output: output}
	if execErr != nil && execErr.Error() != "EXIT" {
		resp.Error = execErr.Error()
		resp.ExitCode = 1
	}
	json.NewEncoder(conn).Encode(&resp)
}

// executeCaptured 执行命令并捕获 stdout/stderr 输出，每个连接独立 buffer 无竞态
func (s *Server) executeCaptured(cmd string) (string, error) {
	var outBuf, errBuf bytes.Buffer
	execErr := s.executor.ExecuteTo(cmd, &outBuf, &errBuf)

	output := outBuf.String()
	if errBuf.Len() > 0 {
		if output != "" {
			output += "\n"
		}
		output += errBuf.String()
	}

	return output, execErr
}

// idleChecker 空闲超时检查，超时后关闭 daemon
func (s *Server) idleChecker() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.mu.Lock()
			elapsed := time.Since(s.lastActive)
			s.mu.Unlock()
			if elapsed >= s.idleTimeout {
				s.Stop()
				return
			}
		}
	}
}

// Stop 停止 daemon
func (s *Server) Stop() {
	select {
	case <-s.stopCh:
		return
	default:
		close(s.stopCh)
	}
	if s.listener != nil {
		s.listener.Close()
	}
	s.pool.Close()
	if s.lockFile != nil {
		s.lockFile.Close()
	}
}

// CheckConfig 检查 daemon 的配置是否与请求匹配
func CheckConfig(hostsFile, passwordsFile string) bool {
	pidFile := PidPath()
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return false
	}
	lines := bytes.Split(data, []byte("\n"))
	if len(lines) < 3 {
		return false
	}
	return string(lines[1]) == hostsFile && string(lines[2]) == passwordsFile
}
