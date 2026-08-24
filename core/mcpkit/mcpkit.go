// Package mcpkit 封装官方 MCP go-sdk（github.com/modelcontextprotocol/go-sdk/mcp），
// 提供两类能力：
//   - Upstream：按配置（stdio 或 streamable HTTP）懒连接一个上游 MCP server，
//     统一暴露 ListTools / CallTool / Close；
//   - NewServer：把一组 ServerTool（handler 收 map 参数）注册为 *mcp.Server，
//     供 mcp-hub 端点与 testkit/fake-mcp 复用。
package mcpkit

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
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
	// LogHook 会话事件回调（kind ∈ connect/connect_ok/connect_fail/disconnect/stderr）。
	// mcp-hub 注入 ServerLog 写入器；nil 表示不采集（现有调用方零影响）。
	// 注意：回调在 Upstream 内部路径触发，实现必须轻量且**不得反向调用本 Upstream 的任何方法**
	//（回调可能持有 u.mu 期间被调用，反向调用会死锁）。
	LogHook func(kind string, fields ...any)
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
	// stderrPr / stderrPw stdio stderr 捕获管道（os.Pipe 方案 B，见 buildTransport）。
	// 仅当配置了 LogHook 且 transport=stdio 时非 nil。
	stderrPr *os.File
	stderrPw *os.File
	// disconnected 防重标记：disconnect 事件（close / process_exit）只发一次。
	disconnected bool
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
		if !u.disconnected {
			u.disconnected = true
			u.hook("disconnect", "reason", "closed")
		}
		return err
	}
	if u.cmd != nil && u.cmd.Process != nil {
		_ = u.cmd.Process.Kill()
	}
	u.cmd = nil
	u.lastErr = nil
	return nil
}

// hook 触发日志回调（LogHook 为 nil 时为空操作）。调用方需保证不持本 Upstream 的锁
// 反向调用（回调实现不得调用 Upstream 方法，见 UpstreamConfig.LogHook 注释）。
func (u *Upstream) hook(kind string, fields ...any) {
	if u.cfg.LogHook != nil {
		u.cfg.LogHook(kind, fields...)
	}
}

// maxFrameBytes 单条 JSON-RPC 帧落日志的最大字节数（超长截断，防止大结果打爆日志行）。
const maxFrameBytes = 1024 * 1024

// frameLogWriter 把官方 LoggingTransport 输出的帧日志行（"read: {...}\n" / "write: {...}\n"）
// 转成 LogHook 事件：read=server→client（frame_in，UI 侧为 FRAME←）、
// write=client→server（frame_out，FRAME→）。io.Writer 的 Write 可能跨行/断行，内部按 \n 缓冲切行。
type frameLogWriter struct {
	u       *Upstream
	pending []byte
}

func newFrameLogWriter(u *Upstream) *frameLogWriter {
	return &frameLogWriter{u: u}
}

func (w *frameLogWriter) Write(p []byte) (int, error) {
	w.pending = append(w.pending, p...)
	for {
		i := bytes.IndexByte(w.pending, '\n')
		if i < 0 {
			break
		}
		line := string(w.pending[:i])
		w.pending = w.pending[i+1:]
		w.emit(line)
	}
	return len(p), nil
}

func (w *frameLogWriter) emit(line string) {
	switch {
	case strings.HasPrefix(line, "read: "):
		w.u.hook("frame_in", "msg", trimFrameMsg(line[len("read: "):]))
	case strings.HasPrefix(line, "write: "):
		w.u.hook("frame_out", "msg", trimFrameMsg(line[len("write: "):]))
	default:
		// read error / 其他诊断行：原样进 frame_in，保证不丢错误信息。
		w.u.hook("frame_in", "msg", trimFrameMsg(line))
	}
}

// trimFrameMsg 单条帧消息截断到 maxFrameBytes，超长加 truncated 标记。
func trimFrameMsg(msg string) string {
	if len(msg) <= maxFrameBytes {
		return msg
	}
	return msg[:maxFrameBytes] + fmt.Sprintf(` ..."(truncated %d bytes)`, len(msg)-maxFrameBytes)
}

// readStderr 从自建管道读 stdio 子进程的 stderr：按行触发 stderr 事件；
// EOF（进程退出）时若会话已建立且未发过 disconnect，补发 disconnect(process_exit)。
// reader goroutine 不持 u.mu 调 hook（避免与 Close/ensureSession 死锁），
// 仅在判断 EOF 时短暂加锁读 disconnected/session 状态。
func (u *Upstream) readStderr(pr *os.File) {
	defer pr.Close()
	scanner := bufio.NewScanner(pr)
	// MCP server 可能打长行（堆栈/日志）；给足缓冲（默认行 64KB、上限 1MB，超长行截断丢弃）。
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		u.hook("stderr", "line", scanner.Text())
	}
	// EOF：进程已退出（或写端全关）。若会话曾建立且未被 Close 标过，补发 process_exit。
	u.mu.Lock()
	hadSession := u.session != nil
	already := u.disconnected
	u.disconnected = true
	u.mu.Unlock()
	if hadSession && !already {
		u.hook("disconnect", "reason", "process_exit")
	}
}

// ensureSession 返回当前会话；若尚未建连，则按配置建立连接（并发安全）。
// 全程持 u.mu；LogHook 回调（hook）可能在此期间被调用，故 hook 实现不得反向调用
// Upstream 方法（见 UpstreamConfig.LogHook 注释）。
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
	// 配置了 LogHook 时包一层官方 LoggingTransport：帧级记录全部双向 JSON-RPC
	// 通信（initialize/tools/list/tools/call + server push notifications）。
	// 三种 transport（stdio/http/sse）统一 wrap，stdout 协议帧与 stderr 分开记录。
	if u.cfg.LogHook != nil {
		transport = &mcp.LoggingTransport{Transport: transport, Writer: newFrameLogWriter(u)}
	}
	u.hook("connect")
	client := mcp.NewClient(&mcp.Implementation{
		Name:    upstreamClientName(u.cfg.Name),
		Version: implementationVersion,
	}, nil)
	session, err := client.Connect(ctx, transport, nil)
	// 释放父进程持有的 stderr 管道写端（子进程已 Start 继承写端）：
	// 不关则 reader 永远等不到 EOF；失败分支同样要关（子进程可能没起来）。
	if u.stderrPw != nil {
		_ = u.stderrPw.Close()
		u.stderrPw = nil
	}
	if err != nil {
		// 建连失败时回收已启动的子进程，避免泄漏一个孤儿 MCP server。
		if u.cmd != nil && u.cmd.Process != nil {
			_ = u.cmd.Process.Kill()
		}
		u.cmd = nil
		u.lastErr = err
		u.hook("connect_fail", "err", err.Error())
		return nil, err
	}
	pid := 0
	if u.cmd != nil && u.cmd.Process != nil {
		pid = u.cmd.Process.Pid
	}
	u.lastErr = nil
	u.session = session
	u.hook("connect_ok", "pid", pid)
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
// stdio + LogHook 时按方案 B 自建 os.Pipe 捕获 stderr（SDK 的 CommandTransport 只
// StdoutPipe/StdinPipe + cmd.Start，从不碰 cmd.Stderr；自建管道不归 SDK 管理，
// 无 StderrPipe 在进程退出瞬间的尾部丢失竞态）。
func (u *Upstream) buildTransport() (mcp.Transport, error) {
	switch u.cfg.Transport {
	case "stdio":
		cmd := exec.Command(u.cfg.Command, u.cfg.Args...)
		// 桌面 exe（windowsgui）下不弹黑色终端框；其他平台为空操作。
		cmdutil.HideWindow(cmd)
		if len(u.cfg.Env) > 0 {
			cmd.Env = append(os.Environ(), mapToEnv(u.cfg.Env)...)
		}
		if u.cfg.LogHook != nil {
			pr, pw, err := os.Pipe()
			if err != nil {
				return nil, fmt.Errorf("mcpkit: create stderr pipe: %w", err)
			}
			cmd.Stderr = pw
			u.stderrPr = pr
			u.stderrPw = pw
			go u.readStderr(pr)
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
