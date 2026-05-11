package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"mssh/config"
	"mssh/ssh"
	"mssh/transfer"
)

// ToolHandler 工具处理函数类型
type ToolHandler func(args map[string]any) (*ToolsCallResult, error)

// GetTools 返回所有注册的 MCP 工具定义
func GetTools() []Tool {
	return []Tool{
		{
			Name:        "ssh_execute",
			Description: "在远程主机上执行命令。支持单台主机、主机组，并发输出所有结果。",
			InputSchema: InputSchema{
				Type: "object",
				Properties: map[string]PropertyDef{
					"command": {Type: "string", Description: "要执行的 shell 命令"},
					"hosts":   {Type: "string", Description: "目标主机名或组名，多个用逗号分隔"},
					"timeout": {Type: "number", Description: "命令超时时间（秒），默认 30"},
				},
				Required: []string{"command", "hosts"},
			},
		},
		{
			Name:        "ssh_list_hosts",
			Description: "列出当前配置中的所有主机和主机组信息",
			InputSchema: InputSchema{
				Type:       "object",
				Properties: map[string]PropertyDef{},
			},
		},
		{
			Name:        "ssh_upload",
			Description: "上传本地文件到远程主机",
			InputSchema: InputSchema{
				Type: "object",
				Properties: map[string]PropertyDef{
					"local_path":  {Type: "string", Description: "本地文件路径"},
					"remote_path": {Type: "string", Description: "远程目标路径"},
					"hosts":       {Type: "string", Description: "目标主机名或组名，多个用逗号分隔"},
				},
				Required: []string{"local_path", "remote_path", "hosts"},
			},
		},
		{
			Name:        "ssh_download",
			Description: "从远程主机下载文件到本地（仅支持单台主机）",
			InputSchema: InputSchema{
				Type: "object",
				Properties: map[string]PropertyDef{
					"remote_path": {Type: "string", Description: "远程文件路径"},
					"local_path":  {Type: "string", Description: "本地保存路径"},
					"host":        {Type: "string", Description: "源主机名（仅支持单台）"},
				},
				Required: []string{"remote_path", "local_path", "host"},
			},
		},
	}
}

// ToolContext 工具执行所需的依赖
type ToolContext struct {
	Cfg      *config.Config
	Pool     *ssh.Pool
	Transfer *transfer.TransferManager
}

// GetHandler 根据工具名返回对应的处理函数
func (ctx *ToolContext) GetHandler(name string) (ToolHandler, bool) {
	switch name {
	case "ssh_execute":
		return ctx.sshExecute, true
	case "ssh_list_hosts":
		return ctx.sshListHosts, true
	case "ssh_upload":
		return ctx.sshUpload, true
	case "ssh_download":
		return ctx.sshDownload, true
	default:
		return nil, false
	}
}

// sshExecute 在远程主机执行命令
func (ctx *ToolContext) sshExecute(args map[string]any) (*ToolsCallResult, error) {
	command, _ := args["command"].(string)
	hostsStr, _ := args["hosts"].(string)
	if command == "" || hostsStr == "" {
		return NewErrorResult("参数 command 和 hosts 为必填项"), nil
	}

	hosts, err := resolveHosts(ctx.Cfg, hostsStr)
	if err != nil {
		return NewErrorResult(err.Error()), nil
	}

	type hostResult struct {
		Host   string `json:"host"`
		Output string `json:"output"`
		Error  string `json:"error,omitempty"`
	}

	results := make([]hostResult, len(hosts))
	var wg sync.WaitGroup

	for i, host := range hosts {
		wg.Add(1)
		go func(idx int, h *config.Host) {
			defer wg.Done()
			output, execErr := ctx.Pool.ExecuteWithOutput(h, command)
			results[idx] = hostResult{Host: h.Name, Output: output}
			if execErr != nil {
				results[idx].Error = execErr.Error()
			}
		}(i, host)
	}
	wg.Wait()

	resultJSON, _ := json.MarshalIndent(results, "", "  ")
	return &ToolsCallResult{
		Content: []ContentBlock{NewTextContent(string(resultJSON))},
	}, nil
}

// sshListHosts 列出所有主机和组
func (ctx *ToolContext) sshListHosts(args map[string]any) (*ToolsCallResult, error) {
	type hostInfo struct {
		Name string `json:"name"`
		User string `json:"user"`
		IP   string `json:"ip"`
		Port int    `json:"port"`
	}

	type groupInfo struct {
		Name  string   `json:"name"`
		Hosts []string `json:"hosts"`
	}

	var hosts []hostInfo
	for _, name := range ctx.Cfg.GetAllHostNames() {
		h, _ := ctx.Cfg.GetHost(name)
		hosts = append(hosts, hostInfo{
			Name: h.Name, User: h.User, IP: h.IP, Port: h.Port,
		})
	}

	var groups []groupInfo
	for _, name := range ctx.Cfg.GetAllGroupNames() {
		g, _ := ctx.Cfg.GetGroup(name)
		groups = append(groups, groupInfo{Name: g.Name, Hosts: g.Hosts})
	}

	output := struct {
		Hosts  []hostInfo  `json:"hosts"`
		Groups []groupInfo `json:"groups"`
	}{Hosts: hosts, Groups: groups}

	resultJSON, _ := json.MarshalIndent(output, "", "  ")
	return &ToolsCallResult{
		Content: []ContentBlock{NewTextContent(string(resultJSON))},
	}, nil
}

// sshUpload 上传文件
func (ctx *ToolContext) sshUpload(args map[string]any) (*ToolsCallResult, error) {
	localPath, _ := args["local_path"].(string)
	remotePath, _ := args["remote_path"].(string)
	hostsStr, _ := args["hosts"].(string)
	if localPath == "" || remotePath == "" || hostsStr == "" {
		return NewErrorResult("参数 local_path, remote_path 和 hosts 为必填项"), nil
	}

	hosts, err := resolveHosts(ctx.Cfg, hostsStr)
	if err != nil {
		return NewErrorResult(err.Error()), nil
	}

	if err := ctx.Transfer.Upload(hosts, localPath, remotePath, true); err != nil {
		return NewErrorResult(fmt.Sprintf("上传失败: %v", err)), nil
	}

	return &ToolsCallResult{
		Content: []ContentBlock{NewTextContent(
			fmt.Sprintf("成功上传 %s 到 %d 台主机", localPath, len(hosts)),
		)},
	}, nil
}

// sshDownload 下载文件
func (ctx *ToolContext) sshDownload(args map[string]any) (*ToolsCallResult, error) {
	remotePath, _ := args["remote_path"].(string)
	localPath, _ := args["local_path"].(string)
	hostName, _ := args["host"].(string)
	if remotePath == "" || localPath == "" || hostName == "" {
		return NewErrorResult("参数 remote_path, local_path 和 host 为必填项"), nil
	}

	host, ok := ctx.Cfg.GetHost(hostName)
	if !ok {
		return NewErrorResult(fmt.Sprintf("主机 '%s' 不存在", hostName)), nil
	}

	if err := ctx.Transfer.Download(host, remotePath, localPath); err != nil {
		return NewErrorResult(fmt.Sprintf("下载失败: %v", err)), nil
	}

	return &ToolsCallResult{
		Content: []ContentBlock{NewTextContent(
			fmt.Sprintf("成功从 %s 下载 %s 到 %s", hostName, remotePath, localPath),
		)},
	}, nil
}

// resolveHosts 解析主机名字符串，返回主机列表
func resolveHosts(cfg *config.Config, hostsStr string) ([]*config.Host, error) {
	var result []*config.Host
	seen := make(map[string]bool)

	for _, name := range strings.Split(hostsStr, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if cfg.GroupExists(name) {
			hosts, err := cfg.GetHostsByGroup(name)
			if err != nil {
				return nil, err
			}
			for _, h := range hosts {
				if !seen[h.Name] {
					seen[h.Name] = true
					result = append(result, h)
				}
			}
		} else if cfg.HostExists(name) {
			if !seen[name] {
				seen[name] = true
				h, _ := cfg.GetHost(name)
				result = append(result, h)
			}
		} else {
			return nil, fmt.Errorf("主机或组 '%s' 不存在", name)
		}
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("未找到匹配的主机")
	}
	return result, nil
}

// GetResources 返回所有资源定义
func GetResources() []Resource {
	return []Resource{
		{
			URI:         "mssh://hosts",
			Name:        "主机清单",
			Description: "所有已配置的远程主机列表（名称、用户、IP、端口）",
			MimeType:    "application/json",
		},
		{
			URI:         "mssh://groups",
			Name:        "主机组清单",
			Description: "所有已配置的主机组及其成员",
			MimeType:    "application/json",
		},
	}
}

// ReadResource 读取指定 URI 的资源内容
func (ctx *ToolContext) ReadResource(uri string) (*ReadResourceResult, error) {
	switch uri {
	case "mssh://hosts":
		return readHostsResource(ctx)
	case "mssh://groups":
		return readGroupsResource(ctx)
	default:
		return nil, fmt.Errorf("未知资源: %s", uri)
	}
}

func readHostsResource(ctx *ToolContext) (*ReadResourceResult, error) {
	var items []map[string]any
	for _, name := range ctx.Cfg.GetAllHostNames() {
		h, _ := ctx.Cfg.GetHost(name)
		items = append(items, map[string]any{
			"name": h.Name, "user": h.User, "ip": h.IP, "port": h.Port,
		})
	}
	text, _ := json.MarshalIndent(items, "", "  ")
	return &ReadResourceResult{
		Contents: []ResourceContent{
			{URI: "mssh://hosts", MimeType: "application/json", Text: string(text)},
		},
	}, nil
}

func readGroupsResource(ctx *ToolContext) (*ReadResourceResult, error) {
	var items []map[string]any
	for _, name := range ctx.Cfg.GetAllGroupNames() {
		g, _ := ctx.Cfg.GetGroup(name)
		items = append(items, map[string]any{
			"name": g.Name, "hosts": g.Hosts,
		})
	}
	text, _ := json.MarshalIndent(items, "", "  ")
	return &ReadResourceResult{
		Contents: []ResourceContent{
			{URI: "mssh://groups", MimeType: "application/json", Text: string(text)},
		},
	}, nil
}
