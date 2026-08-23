// Package mcpkit 封装官方 MCP go-sdk（github.com/modelcontextprotocol/go-sdk/mcp），
// 提供两类能力：
//   - Upstream：按配置（stdio 或 streamable HTTP）懒连接一个上游 MCP server，
//     统一暴露 ListTools / CallTool / Close；
//   - NewServer：把一组 ServerTool（handler 收 map 参数）注册为 *mcp.Server，
//     供 mcp-hub 端点与 testkit/fake-mcp 复用。
package mcpkit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"loadout/core/cmdutil"
)

// implementationVersion 是注册客户端/服务端实现时使用的默认版本号。
const implementationVersion = "1.0.0"

// ToolInfo 一个工具的元信息（用于 status/get 索引）。
type ToolInfo struct {
	// Name 工具名。
	Name string
	// Description 描述。
	Description string
	// InputSchema inputSchema（JSON Schema）。
	InputSchema map[string]any
}

// ContentPart 工具结果内容片段（v1 主要用 text）。
type ContentPart struct {
	// Type 内容类型（"text"）。
	Type string
	// Text 文本内容。
	Text string
}

// ToolResult 工具调用结果。
type ToolResult struct {
	// Content 结果内容片段。
	Content []ContentPart
	// IsError 是否为错误结果。
	IsError bool
}

// UpstreamConfig 上游 MCP server 连接配置。
type UpstreamConfig struct {
	// Name 名称。
	Name string
	// Transport 传输方式（"stdio" / "http" / "sse"）。
	Transport string
	// Command stdio：可执行文件（如 "npx"）。
	Command string
	// Args stdio：参数。
	Args []string
	// Env stdio：附加环境变量。
	Env map[string]string
	// URL http / sse：服务地址。
	URL string
	// Headers http / sse：附加请求头。
	Headers map[string]string
}

// Upstream 一个上游 MCP 连接（懒连接，首次 ListTools/CallTool 时才建连）。
type Upstream struct {
	mu      sync.Mutex
	cfg     UpstreamConfig
	session *mcp.ClientSession
	// cmd stdio 子进程引用（用于判断进程存活；非 stdio 传输恒为 nil）。
	cmd *exec.Cmd
	// lastErr 最近一次建连失败的错误（供 UI 展示失败原因）。
	lastErr error
}

// NewUpstream 根据配置创建一个懒连接的上游 MCP 客户端。
func NewUpstream(cfg UpstreamConfig) *Upstream {
	return &Upstream{cfg: cfg}
}

// ListTools 列出上游 server 暴露的所有工具；首次调用时建立连接。
func (u *Upstream) ListTools(ctx context.Context) ([]ToolInfo, error) {
	session, err := u.ensureSession(ctx)
	if err != nil {
		return nil, err
	}
	res, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		return nil, err
	}
	infos := make([]ToolInfo, 0, len(res.Tools))
	for _, t := range res.Tools {
		schema, _ := t.InputSchema.(map[string]any)
		infos = append(infos, ToolInfo{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		})
	}
	return infos, nil
}

// CallTool 调用上游 server 的指定工具；首次调用时建立连接。
func (u *Upstream) CallTool(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
	session, err := u.ensureSession(ctx)
	if err != nil {
		return nil, err
	}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return nil, err
	}
	out := &ToolResult{IsError: res.IsError}
	for _, c := range res.Content {
		out.Content = append(out.Content, contentToPart(c))
	}
	return out, nil
}

// Connect 显式建立上游连接（stdio 会拉起子进程并保持运行）；已连接时为空操作。
// 供「开关启动」与「启动自动恢复」使用：调用后进程常驻后台，直到 Close。
func (u *Upstream) Connect(ctx context.Context) error {
	_, err := u.ensureSession(ctx)
	return err
}

// Alive 报告 stdio 子进程是否仍在运行（非 stdio 传输或未连接返回 false）。
// 用于前端展示「运行中 / 已崩溃」状态；崩溃不自动重启，由调用方决定处理。
func (u *Upstream) Alive() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	// Process 非 nil 且 ProcessState 为 nil ⇒ 进程已 Start 且尚未 Wait 结束。
	return u.cmd != nil && u.cmd.Process != nil && u.cmd.ProcessState == nil
}

// LastError 返回最近一次建连失败的错误（无失败记录返回空串）。供 UI 展示失败原因。
func (u *Upstream) LastError() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.lastErr == nil {
		return ""
	}
	return u.lastErr.Error()
}

// Close 关闭上游连接；若尚未建立连接则不做任何事。stdio 下会终止子进程。
// 注意：若建连仍在进行中（session 尚未建立但子进程已 Start），直接 Kill 进程，
// 避免「开关快速切换」留下孤儿 MCP server。
func (u *Upstream) Close() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.session != nil {
		err := u.session.Close()
		u.session = nil
		u.cmd = nil
		u.lastErr = nil
		return err
	}
	if u.cmd != nil && u.cmd.Process != nil {
		_ = u.cmd.Process.Kill()
	}
	u.cmd = nil
	u.lastErr = nil
	return nil
}

// ensureSession 返回当前会话；若尚未建连，则按配置建立连接（并发安全）。
func (u *Upstream) ensureSession(ctx context.Context) (*mcp.ClientSession, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.session != nil {
		u.lastErr = nil
		return u.session, nil
	}
	transport, err := u.buildTransport()
	if err != nil {
		u.lastErr = err
		return nil, err
	}
	client := mcp.NewClient(&mcp.Implementation{
		Name:    upstreamClientName(u.cfg.Name),
		Version: implementationVersion,
	}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		// 建连失败时回收已启动的子进程，避免泄漏一个孤儿 MCP server。
		if u.cmd != nil && u.cmd.Process != nil {
			_ = u.cmd.Process.Kill()
		}
		u.cmd = nil
		u.lastErr = err
		return nil, err
	}
	u.lastErr = nil
	u.session = session
	return session, nil
}

// upstreamClientName 返回客户端标识名；空名称时回落到默认值。
func upstreamClientName(name string) string {
	if name != "" {
		return name
	}
	return "loadout-mcpkit"
}

// buildTransport 根据配置构造对应的 MCP 传输实现（记录 stdio 子进程引用供 Alive 判断）。
func (u *Upstream) buildTransport() (mcp.Transport, error) {
	switch u.cfg.Transport {
	case "stdio":
		cmd := exec.Command(u.cfg.Command, u.cfg.Args...)
		// 桌面 exe（windowsgui）下不弹黑色终端框；其他平台为空操作。
		cmdutil.HideWindow(cmd)
		if len(u.cfg.Env) > 0 {
			cmd.Env = append(os.Environ(), mapToEnv(u.cfg.Env)...)
		}
		u.cmd = cmd
		return &mcp.CommandTransport{Command: cmd}, nil
	case "http":
		return &mcp.StreamableClientTransport{
			Endpoint:   u.cfg.URL,
			HTTPClient: newHTTPClient(u.cfg.Headers),
		}, nil
	case "sse":
		return &mcp.SSEClientTransport{
			Endpoint:   u.cfg.URL,
			HTTPClient: newHTTPClient(u.cfg.Headers),
		}, nil
	default:
		return nil, fmt.Errorf("mcpkit: unsupported transport %q (want \"stdio\", \"http\" or \"sse\")", u.cfg.Transport)
	}
}

// mapToEnv 把 map[string]string 转成 "KEY=VALUE" 形式的环境变量切片。
func mapToEnv(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// headerTransport 是一个为每个请求附加固定请求头的 http.RoundTripper。
type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

// RoundTrip 实现 http.RoundTripper：克隆请求并写入附加头后转发。
func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	for k, v := range t.headers {
		clone.Header.Set(k, v)
	}
	return t.base.RoundTrip(clone)
}

// newHTTPClient 构造 streamable HTTP 传输使用的 http.Client，并按需附加请求头。
func newHTTPClient(headers map[string]string) *http.Client {
	c := &http.Client{}
	if len(headers) > 0 {
		c.Transport = &headerTransport{base: http.DefaultTransport, headers: headers}
	}
	return c
}

// contentToPart 把 SDK 的内容片段转换为 ContentPart；文本直接取值，其余类型尽力转成字符串。
func contentToPart(c mcp.Content) ContentPart {
	if tc, ok := c.(*mcp.TextContent); ok {
		return ContentPart{Type: "text", Text: tc.Text}
	}
	if b, err := json.Marshal(c); err == nil {
		return ContentPart{Type: "text", Text: string(b)}
	}
	return ContentPart{Type: "text", Text: fmt.Sprintf("%v", c)}
}

// ServerTool 定义一个由 mcpkit 托管的工具（handler 收 map 参数）。
type ServerTool struct {
	// Name 工具名。
	Name string
	// Description 描述。
	Description string
	// InputSchema inputSchema（JSON Schema，需含 "type": "object"）。
	InputSchema map[string]any
	// Handler 工具实现，收 map 参数。
	Handler func(ctx context.Context, args map[string]any) (*ToolResult, error)
}

// NewServer 构建一个暴露给定工具的 MCP server（供 mcp-hub 端点与 fake-mcp 复用）。
// 内部把每个 ServerTool 注册为 AddTool，handler 里把 req.Params.Arguments 解码为 map 后调用 ServerTool.Handler。
func NewServer(name string, tools []ServerTool) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: name, Version: implementationVersion}, nil)
	for _, tool := range tools {
		server.AddTool(toolToDefinition(tool), makeToolHandler(tool))
	}
	return server
}

// toolToDefinition 把 ServerTool 转成 mcp.Tool 定义（InputSchema 序列化为 json.RawMessage）。
func toolToDefinition(tool ServerTool) *mcp.Tool {
	schema, err := json.Marshal(tool.InputSchema)
	if err != nil {
		panic(fmt.Sprintf("mcpkit: marshal input schema for tool %q: %v", tool.Name, err))
	}
	return &mcp.Tool{
		Name:        tool.Name,
		Description: tool.Description,
		InputSchema: json.RawMessage(schema),
	}
}

// makeToolHandler 把 ServerTool.Handler 适配为 SDK 的 ToolHandler。
func makeToolHandler(tool ServerTool) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := map[string]any{}
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return nil, fmt.Errorf("mcpkit: decoding arguments for tool %q: %w", tool.Name, err)
			}
		}
		if args == nil {
			args = map[string]any{}
		}
		result, err := tool.Handler(ctx, args)
		if err != nil {
			return nil, err
		}
		return resultToMCP(result), nil
	}
}

// resultToMCP 把 mcpkit 的 ToolResult 转成 SDK 的 CallToolResult。
func resultToMCP(result *ToolResult) *mcp.CallToolResult {
	if result == nil {
		return &mcp.CallToolResult{}
	}
	out := &mcp.CallToolResult{IsError: result.IsError}
	for _, part := range result.Content {
		out.Content = append(out.Content, &mcp.TextContent{Text: part.Text})
	}
	return out
}
