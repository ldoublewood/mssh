package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"mssh/config"
	"mssh/ssh"
	"mssh/transfer"
)

// Server MCP 服务端，通过 stdio 与客户端通信
type Server struct {
	cfg      *config.Config
	pool     *ssh.Pool
	transfer *transfer.TransferManager
	toolCtx  *ToolContext
	reader   *bufio.Reader
	writer   io.Writer
	logger   io.Writer
}

// NewServer 创建 MCP Server
func NewServer(cfg *config.Config, pool *ssh.Pool) *Server {
	tm := transfer.NewTransferManager(pool)
	return &Server{
		cfg:      cfg,
		pool:     pool,
		transfer: tm,
		toolCtx:  &ToolContext{Cfg: cfg, Pool: pool, Transfer: tm},
		reader:   bufio.NewReader(os.Stdin),
		writer:   os.Stdout,
		logger:   os.Stderr,
	}
}

// Run 启动 MCP Server（阻塞），从 stdin 读取请求，写入 stdout
func (s *Server) Run() error {
	scanner := bufio.NewScanner(s.reader)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if err := s.handleMessage(line); err != nil {
			fmt.Fprintf(s.logger, "[mcp] 处理消息失败: %v\n", err)
		}
	}
	return scanner.Err()
}

// handleMessage 处理单条 JSON-RPC 消息
func (s *Server) handleMessage(data []byte) error {
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		return fmt.Errorf("解析请求失败: %w", err)
	}

	if req.JSONRPC != "2.0" {
		return fmt.Errorf("不支持的 JSON-RPC 版本: %s", req.JSONRPC)
	}

	if req.ID == nil {
		s.handleNotification(req.Method, req.Params)
		return nil
	}

	result, err := s.dispatch(req.Method, req.Params)
	if err != nil {
		return s.sendError(req.ID, -32603, err.Error())
	}
	return s.sendResponse(req.ID, result)
}

// dispatch 根据 method 路由到对应的处理器
func (s *Server) dispatch(method string, params json.RawMessage) (any, error) {
	switch method {
	case "initialize":
		return s.handleInitialize(params)
	case "tools/list":
		return s.handleToolsList()
	case "tools/call":
		return s.handleToolsCall(params)
	case "resources/list":
		return s.handleResourcesList()
	case "resources/read":
		return s.handleResourcesRead(params)
	default:
		return nil, fmt.Errorf("未知方法: %s", method)
	}
}

// handleInitialize MCP 协议握手
func (s *Server) handleInitialize(_ json.RawMessage) (any, error) {
	return InitializeResult{
		ProtocolVersion: ProtocolVersion,
		Capabilities: ServerCapabilities{
			Tools:     &ToolsCapability{},
			Resources: &ResourcesCapability{},
		},
		ServerInfo: Implementation{
			Name:    ServerName,
			Version: ServerVersion,
		},
		Instructions: "mssh MCP 服务器 — 用于远程服务器管理和命令执行",
	}, nil
}

// handleToolsList 返回工具列表
func (s *Server) handleToolsList() (any, error) {
	return ToolsListResult{Tools: GetTools()}, nil
}

// handleToolsCall 执行工具调用
func (s *Server) handleToolsCall(params json.RawMessage) (any, error) {
	var p ToolsCallParams
	if err := json.Unmarshal(params, &p); err != nil {
		return NewErrorResult(fmt.Sprintf("解析参数失败: %v", err)), nil
	}

	handler, ok := s.toolCtx.GetHandler(p.Name)
	if !ok {
		return NewErrorResult(fmt.Sprintf("未知工具: %s", p.Name)), nil
	}

	if p.Arguments == nil {
		p.Arguments = make(map[string]any)
	}

	return handler(p.Arguments)
}

// handleResourcesList 返回资源列表
func (s *Server) handleResourcesList() (any, error) {
	return ResourcesListResult{Resources: GetResources()}, nil
}

// handleResourcesRead 读取资源内容
func (s *Server) handleResourcesRead(params json.RawMessage) (any, error) {
	var p ReadResourceParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("解析参数失败: %w", err)
	}
	return s.toolCtx.ReadResource(p.URI)
}

// handleNotification 处理通知
func (s *Server) handleNotification(method string, _ json.RawMessage) {
	switch method {
	case "notifications/initialized":
		fmt.Fprintf(s.logger, "[mcp] MCP 服务器已就绪\n")
	case "notifications/cancelled":
		fmt.Fprintf(s.logger, "[mcp] 收到取消通知\n")
	}
}

// sendResponse 发送成功响应
func (s *Server) sendResponse(id json.RawMessage, result any) error {
	resp := Response{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
	return s.writeJSON(resp)
}

// sendError 发送错误响应
func (s *Server) sendError(id json.RawMessage, code int, message string) error {
	resp := ErrorResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   RPCError{Code: code, Message: message},
	}
	return s.writeJSON(resp)
}

// writeJSON 将对象序列化为 JSON 并写入 stdout
func (s *Server) writeJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("序列化响应失败: %w", err)
	}
	if _, err := fmt.Fprintf(s.writer, "%s\n", data); err != nil {
		return fmt.Errorf("写入响应失败: %w", err)
	}
	return nil
}
