package mcp

import "encoding/json"

// ─── JSON-RPC 2.0 基础类型 ───

// Request 表示 JSON-RPC 请求
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response 表示 JSON-RPC 成功响应
type Response struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      any `json:"id"`
	Result  any `json:"result"`
}

// ErrorResponse 表示 JSON-RPC 错误响应
type ErrorResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      any `json:"id"`
	Error   RPCError    `json:"error"`
}

// RPCError JSON-RPC 错误对象
type RPCError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    any `json:"data,omitempty"`
}

// ─── MCP 协议常量 ───

const (
	ProtocolVersion = "2024-11-05"
	ServerName      = "mssh"
	ServerVersion   = "1.0.0"
)

// ─── MCP Initialize ───

// InitializeRequest 客户端初始化请求
type InitializeRequest struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ClientCapabilities `json:"capabilities"`
	ClientInfo      Implementation     `json:"clientInfo"`
}

// ClientCapabilities 客户端能力声明
type ClientCapabilities struct {
	Roots        *RootsCapability        `json:"roots,omitempty"`
	Sampling     *struct{}               `json:"sampling,omitempty"`
	Elicitation  *struct{}               `json:"elicitation,omitempty"`
	Experimental map[string]any `json:"experimental,omitempty"`
}

// RootsCapability 根目录能力
type RootsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// Implementation 客户端/服务端信息
type Implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitializeResult 服务端初始化响应
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      Implementation     `json:"serverInfo"`
	Instructions    string             `json:"instructions,omitempty"`
}

// ServerCapabilities 服务端能力声明
type ServerCapabilities struct {
	Tools        *ToolsCapability        `json:"tools,omitempty"`
	Resources    *ResourcesCapability    `json:"resources,omitempty"`
	Prompts      *PromptsCapability      `json:"prompts,omitempty"`
	Logging      *struct{}               `json:"logging,omitempty"`
	Experimental map[string]any `json:"experimental,omitempty"`
}

// ToolsCapability 工具能力
type ToolsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// ResourcesCapability 资源能力
type ResourcesCapability struct {
	Subscribe   bool `json:"subscribe,omitempty"`
	ListChanged bool `json:"listChanged,omitempty"`
}

// PromptsCapability 提示词能力
type PromptsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// ─── MCP Tool 相关类型 ───

// Tool 工具定义
type Tool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema InputSchema `json:"inputSchema"`
}

// InputSchema JSON Schema 风格的参数定义
type InputSchema struct {
	Type       string                 `json:"type"`
	Properties map[string]PropertyDef `json:"properties"`
	Required   []string               `json:"required,omitempty"`
}

// PropertyDef 属性定义
type PropertyDef struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

// ToolsListResult tools/list 方法的响应
type ToolsListResult struct {
	Tools []Tool `json:"tools"`
}

// ToolsCallParams tools/call 方法的参数
type ToolsCallParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// ToolsCallResult tools/call 方法的响应
type ToolsCallResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

// ContentBlock MCP 内容块
type ContentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Data     string `json:"data,omitempty"`
}

// ─── MCP Resource 相关类型 ───

// Resource 资源定义
type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// ResourcesListResult resources/list 方法的响应
type ResourcesListResult struct {
	Resources []Resource `json:"resources"`
}

// ReadResourceParams resources/read 方法的参数
type ReadResourceParams struct {
	URI string `json:"uri"`
}

// ReadResourceResult resources/read 方法的响应
type ReadResourceResult struct {
	Contents []ResourceContent `json:"contents"`
}

// ResourceContent 资源内容
type ResourceContent struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

// ─── 辅助函数 ───

// NewTextContent 创建文本内容块
func NewTextContent(text string) ContentBlock {
	return ContentBlock{
		Type: "text",
		Text: text,
	}
}

// NewErrorResult 创建表示错误的工具调用结果
func NewErrorResult(errMsg string) *ToolsCallResult {
	return &ToolsCallResult{
		Content: []ContentBlock{NewTextContent(errMsg)},
		IsError: true,
	}
}
