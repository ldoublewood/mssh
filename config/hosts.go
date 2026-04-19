package config

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Host 表示单台主机信息
type Host struct {
	Name     string
	User     string
	IP       string
	Port     int
	Password string // 从密码文件加载
}

// Group 表示主机组
type Group struct {
	Name      string
	Hosts     []string // 主机名列表
	SubGroups []string // 子组名列表
}

// Config 保存所有配置信息
type Config struct {
	Hosts  map[string]*Host
	Groups map[string]*Group
}

// NewConfig 创建新的配置对象
func NewConfig() *Config {
	return &Config{
		Hosts:  make(map[string]*Host),
		Groups: make(map[string]*Group),
	}
}

// LoadHosts 从hosts.ini文件加载主机配置
func (c *Config) LoadHosts(filename string) error {
	file, err := os.Open(filename)
	if err != nil {
		return fmt.Errorf("无法打开hosts文件 %s: %v", filename, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var currentGroup string
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// 跳过空行和注释
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}

		// 检查组定义 [group]
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			groupName := strings.Trim(line, "[]")
			if groupName == "" {
				return fmt.Errorf("第%d行: 组名不能为空", lineNum)
			}
			currentGroup = groupName
			if _, exists := c.Groups[groupName]; !exists {
				c.Groups[groupName] = &Group{
					Name:      groupName,
					Hosts:     []string{},
					SubGroups: []string{},
				}
			}
			continue
		}

		// 解析主机定义: name = user@ip:port
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("第%d行: 格式错误，应为 'name = user@ip:port'", lineNum)
		}

		hostName := strings.TrimSpace(parts[0])
		addrStr := strings.TrimSpace(parts[1])

		if hostName == "" {
			return fmt.Errorf("第%d行: 主机名不能为空", lineNum)
		}

		host, err := parseHostAddr(hostName, addrStr)
		if err != nil {
			return fmt.Errorf("第%d行: %v", lineNum, err)
		}

		c.Hosts[hostName] = host

		// 如果有当前组，将主机添加到组中
		if currentGroup != "" {
			group := c.Groups[currentGroup]
			group.Hosts = append(group.Hosts, hostName)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("读取hosts文件失败: %v", err)
	}

	return nil
}

// parseHostAddr 解析地址字符串 user@ip:port
func parseHostAddr(name, addr string) (*Host, error) {
	// 正则匹配 user@ip:port 或 user@ip
	re := regexp.MustCompile(`^([^@]+)@([^:]+)(?::(\d+))?$`)
	matches := re.FindStringSubmatch(addr)

	if matches == nil {
		return nil, fmt.Errorf("地址格式错误，应为 'user@ip:port' 或 'user@ip'")
	}

	user := matches[1]
	ip := matches[2]
	port := 22 // 默认端口

	if matches[3] != "" {
		p, err := strconv.Atoi(matches[3])
		if err != nil || p <= 0 || p > 65535 {
			return nil, fmt.Errorf("端口格式错误: %s", matches[3])
		}
		port = p
	}

	return &Host{
		Name: name,
		User: user,
		IP:   ip,
		Port: port,
	}, nil
}

// LoadPasswords 从密码文件加载密码
func (c *Config) LoadPasswords(filename string) error {
	file, err := os.Open(filename)
	if err != nil {
		if os.IsNotExist(err) {
			// 密码文件不存在是正常的（使用免密登录）
			return nil
		}
		return fmt.Errorf("无法打开密码文件 %s: %v", filename, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// 跳过空行和注释
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}

		// 解析: host = password
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue // 跳过格式错误的行
		}

		hostName := strings.TrimSpace(parts[0])
		password := strings.TrimSpace(parts[1])

		if host, exists := c.Hosts[hostName]; exists {
			host.Password = password
		}
	}

	return scanner.Err()
}

// GetHost 获取指定主机
func (c *Config) GetHost(name string) (*Host, bool) {
	host, exists := c.Hosts[name]
	return host, exists
}

// GetGroup 获取指定组
func (c *Config) GetGroup(name string) (*Group, bool) {
	group, exists := c.Groups[name]
	return group, exists
}

// GetHostsByGroup 递归获取组内的所有主机（展开子组）
func (c *Config) GetHostsByGroup(groupName string) ([]*Host, error) {
	group, exists := c.Groups[groupName]
	if !exists {
		return nil, fmt.Errorf("组 '%s' 不存在", groupName)
	}

	hostMap := make(map[string]*Host)
	c.collectHostsRecursive(group, hostMap)

	hosts := make([]*Host, 0, len(hostMap))
	for _, host := range hostMap {
		hosts = append(hosts, host)
	}
	return hosts, nil
}

// collectHostsRecursive 递归收集组内的所有主机
func (c *Config) collectHostsRecursive(group *Group, hostMap map[string]*Host) {
	// 添加直接的主机
	for _, hostName := range group.Hosts {
		if host, exists := c.Hosts[hostName]; exists {
			hostMap[hostName] = host
		}
	}

	// 递归处理子组
	for _, subGroupName := range group.SubGroups {
		if subGroup, exists := c.Groups[subGroupName]; exists {
			c.collectHostsRecursive(subGroup, hostMap)
		}
	}
}

// GetAllHostNames 获取所有主机名
func (c *Config) GetAllHostNames() []string {
	names := make([]string, 0, len(c.Hosts))
	for name := range c.Hosts {
		names = append(names, name)
	}
	return names
}

// GetAllGroupNames 获取所有组名
func (c *Config) GetAllGroupNames() []string {
	names := make([]string, 0, len(c.Groups))
	for name := range c.Groups {
		names = append(names, name)
	}
	return names
}

// HostExists 检查主机是否存在
func (c *Config) HostExists(name string) bool {
	_, exists := c.Hosts[name]
	return exists
}

// GroupExists 检查组是否存在
func (c *Config) GroupExists(name string) bool {
	_, exists := c.Groups[name]
	return exists
}
