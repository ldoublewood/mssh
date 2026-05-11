# AGENTS.md - 编码代理指南

## 使用语言

- 使用中文编写文档和代码注释
- 交互对话也采用中文

## 构建命令

```bash
make
```

## 测试命令

```bash
make test
```

## 代码风格指南

### 格式化规范

- 使用 `gofmt` 或 `goimports` 进行格式化
- 行长度：建议不超过 100 个字符
- 使用制表符缩进
- 文件末尾保留空行

### 命名规范

- **导出成员**：大驼峰命名
- **未导出成员**：小驼峰命名
- **接口命名**：使用 `Handler`、`Device`、`Manager` 等后缀
- **私有字段**：以小写字母开头
- **常量命名**：导出常量使用大驼峰或全大写

### 类型和结构体

- 使用结构体标签支持 JSON/YAML/mapstructure：`json:"field_name" yaml:"field_name" mapstructure:"field_name"`
- 所有导出类型和字段必须包含中文注释
- 修改状态的方法使用指针接收器
- 只读方法使用值接收器

### 错误处理

- 始终使用上下文包装错误：`fmt.Errorf("...: %w", err)`
- 使用提前返回减少嵌套层级
- 在适当的日志级别记录错误（Debug、Info、Warn、Error）
- 解引用前检查 nil 指针

### 日志记录

- 使用 `go.uber.org/zap` 进行结构化日志记录
- 使用类型化字段：`zap.String()`、`zap.Int()`、`zap.Error()`
- 日志级别：Debug（详细）、Info（正常）、Warn（问题）、Error（故障）


### 上下文使用

- 将 `context.Context` 作为第一个参数传递
- 长时间运行的操作在结构体中存储上下文
- 使用 `context.WithCancel()` 实现优雅关闭
- 始终调用 `cancel()` 防止协程泄漏

### 并发处理

- 使用 `sync.RWMutex` 保护共享状态
- 使用通道在协程间通信
- 始终在协程中处理上下文取消
- 使用互斥锁保护结构体字段

### 测试规范

- 测试文件：`*_test.go` 或 `example_test.go`
- 适当使用表驱动测试
- 模拟外部依赖
- 致命错误使用 `t.Fatalf()`，非致命错误使用 `t.Errorf()`
- 测试成功和错误两种情况

Go 版本：1.25.0
