// Package fakemcp 提供一个可编程的内存 MCP 假上游（streamable HTTP），
// 用于在测试中替代真实的上游 MCP server。它基于 httptest.Server 暴露
// /mcp 端点，注册一组指定工具，记录所有工具调用，并返回固定的文本结果。
package fakemcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"loadout/core/mcpkit"
)

// defaultPath 是 streamable HTTP 端点的默认路径。
const defaultPath = "/mcp"

// Tool 一个要注册的工具定义（简化）。
type Tool struct {
	// Name 工具名。
	Name string
	// Description 描述。
	Description string
	// InputSchema 工具的 inputSchema（JSON Schema）。
	InputSchema map[string]any
	// Result 默认返回的文本结果（空则返回 "ok"）。
	Result string
}

// Call 记录一次工具调用。
type Call struct {
	// Name 被调用的工具名。
	Name string
	// Args 调用时收到的参数。
	Args map[string]any
}

// FakeMCP 一个可编程的内存 MCP 假上游（streamable HTTP），记录所有工具调用。
type FakeMCP struct {
	mu     sync.Mutex       // 保护 calls 的并发访问
	server *httptest.Server // 底层测试服务器
	calls  []Call           // 按到达顺序记录的工具调用
}

// New 创建假上游：注册 tools，启动 streamable HTTP server，返回可用的 FakeMCP。
func New(name string, tools []Tool) *FakeMCP {
	f := &FakeMCP{}
	serverTools := make([]mcpkit.ServerTool, 0, len(tools))
	for _, tool := range tools {
		result := tool.Result
		if result == "" {
			result = "ok"
		}
		serverTools = append(serverTools, mcpkit.ServerTool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: tool.InputSchema,
			Handler: func(_ context.Context, args map[string]any) (*mcpkit.ToolResult, error) {
				f.recordCall(tool.Name, args)
				return &mcpkit.ToolResult{
					Content: []mcpkit.ContentPart{{Type: "text", Text: result}},
				}, nil
			},
		})
	}

	server := mcpkit.NewServer(name, serverTools)
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true},
	)
	mux := http.NewServeMux()
	mux.Handle(defaultPath, handler)
	f.server = httptest.NewServer(mux)
	return f
}

// URL 返回 streamable HTTP 端点完整 URL（如 http://127.0.0.1:PORT/mcp）。
// 供 mcpkit.NewUpstream(UpstreamConfig{Transport: "http", URL: ...}) 直接连接。
func (f *FakeMCP) URL() string {
	return f.server.URL + defaultPath
}

// Calls 返回按到达顺序记录的工具调用（副本，可安全只读）。
func (f *FakeMCP) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Call, len(f.calls))
	copy(out, f.calls)
	return out
}

// Close 关闭 httptest server。
func (f *FakeMCP) Close() {
	f.server.Close()
}

// recordCall 线程安全地追加一次工具调用记录；参数做浅拷贝，避免外部改动影响内部状态。
func (f *FakeMCP) recordCall(name string, args map[string]any) {
	cp := make(map[string]any, len(args))
	for k, v := range args {
		cp[k] = v
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, Call{Name: name, Args: cp})
}
