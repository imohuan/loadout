package mcpkit

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// init 子进程分支：本测试进程被当作 stdio 子进程重启时（os.Args[0] 作为 Command），
// 按 MCPKIT_STDIO_CHILD 环境变量扮演两种角色：
//   - "server"：跑一个 stdio MCP server（握手成功场景），打 stderr 后退出；
//   - "stderr"：不打 MCP 协议，直接打 stderr 后退出（握手必然失败场景）。
//
// 子进程不经过 testing 框架，init 里直接 os.Exit。
func init() {
	switch os.Getenv("MCPKIT_STDIO_CHILD") {
	case "server":
		srv := NewServer("mcpkit-child", []ServerTool{{
			Name:        "ping",
			Description: "回显",
			InputSchema: map[string]any{"type": "object"},
			Handler: func(_ context.Context, _ map[string]any) (*ToolResult, error) {
				return &ToolResult{Content: []ContentPart{{Type: "text", Text: "pong"}}}, nil
			},
		}})
		if _, err := srv.Connect(context.Background(), &mcp.StdioTransport{}, nil); err != nil {
			os.Stderr.WriteString("child connect error: " + err.Error() + "\n")
			os.Exit(1)
		}
		// 给客户端完成 initialize 握手的时间，再打 stderr 后退出。
		time.Sleep(500 * time.Millisecond)
		os.Stderr.WriteString("first line\n")
		os.Stderr.WriteString("tail-no-newline") // 无换行末行，验证 EOF flush
		os.Exit(0)
	case "stderr":
		os.Stderr.WriteString("first line\n")
		os.Stderr.WriteString("tail-no-newline")
		os.Exit(0)
	}
}

// hookEvent 记录一次 LogHook 回调。
type hookEvent struct {
	kind   string
	fields []any
}

// collectHooks 返回一个并发安全的 LogHook 收集器。
func collectHooks() (func(kind string, fields ...any), *[]hookEvent, *sync.Mutex) {
	var mu sync.Mutex
	var events []hookEvent
	hook := func(kind string, fields ...any) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, hookEvent{kind: kind, fields: fields})
	}
	return hook, &events, &mu
}

// waitStderrLines 轮询直到 stderr 事件累计 n 行（无换行末行依赖 EOF，需等待 flush）。
func waitStderrLines(t *testing.T, events *[]hookEvent, mu *sync.Mutex, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := len(stderrLines(*events))
		mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("等待 stderr %d 行超时，当前 %d 行: %v", n, len(stderrLines(*events)), *events)
}

// waitEvents 轮询直到 events 中出现全部指定 kind（顺序无关），超时返回错误。
func waitEvents(t *testing.T, events *[]hookEvent, mu *sync.Mutex, want ...string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := map[string]bool{}
		for _, e := range *events {
			got[e.kind] = true
		}
		mu.Unlock()
		ok := true
		for _, w := range want {
			if !got[w] {
				ok = false
				break
			}
		}
		if ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("等待 hook 事件超时：want=%v got=%v", want, *events)
}

// stderrLines 返回 events 中全部 stderr 事件的 line 字段值。
func stderrLines(events []hookEvent) []string {
	var out []string
	for _, e := range events {
		if e.kind != "stderr" {
			continue
		}
		for i := 0; i+1 < len(e.fields); i += 2 {
			if e.fields[i] == "line" {
				if s, ok := e.fields[i+1].(string); ok {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

// TestUpstreamStdioStderrCaptured 验证 stdio stderr 被 LogHook 逐行捕获：
// 子进程打两行（含无换行末行）后退出，Connect 必然失败（无 MCP 响应），
// 但 stderr 两行都应收齐（方案 B 的 EOF flush 保证末行不丢），并收到 connect_fail。
func TestUpstreamStdioStderrCaptured(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	hook, events, mu := collectHooks()
	up := NewUpstream(UpstreamConfig{
		Name:    "stderr-child",
		Transport: "stdio",
		Command: exe,
		Env:     map[string]string{"MCPKIT_STDIO_CHILD": "stderr"},
		LogHook: hook,
	})
	defer up.Close()

	if _, err := up.ListTools(context.Background()); err == nil {
		t.Fatal("stderr-only 子进程不应握手成功，ListTools 应返回错误")
	}

	// connect_fail 出现即说明 pw 已 Close；无换行末行要等 EOF 才被 scanner flush，
	// 需轮询等 2 行 stderr（不能只在首行出现时就断言）。
	waitEvents(t, events, mu, "connect", "connect_fail", "stderr")
	waitStderrLines(t, events, mu, 2)

	mu.Lock()
	defer mu.Unlock()
	lines := stderrLines(*events)
	if len(lines) != 2 {
		t.Fatalf("stderr 行数 = %d (%q)，期望 2（含无换行末行）", len(lines), lines)
	}
	if lines[0] != "first line" {
		t.Fatalf("stderr 首行 = %q，期望 %q", lines[0], "first line")
	}
	if lines[1] != "tail-no-newline" {
		t.Fatalf("stderr 末行 = %q，期望 %q（无换行末行不得丢失）", lines[1], "tail-no-newline")
	}
}

// TestUpstreamLoggingTransportFrames 验证 LoggingTransport 包装：握手成功时
// LogHook 收到全部双向 JSON-RPC 帧（frame_out=client→server，frame_in=server→client），
// 且 initialize 请求/响应都在其中——三种 transport 统一由 buildTransport 包装。
func TestUpstreamLoggingTransportFrames(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	hook, events, mu := collectHooks()
	up := NewUpstream(UpstreamConfig{
		Name:      "stdio-server",
		Transport: "stdio",
		Command:   exe,
		Env:       map[string]string{"MCPKIT_STDIO_CHILD": "server"},
		LogHook:   hook,
	})
	defer up.Close()

	if err := up.Connect(context.Background()); err != nil {
		t.Fatalf("stdio server Connect 应成功：%v", err)
	}

	waitEvents(t, events, mu, "frame_out", "frame_in")

	mu.Lock()
	defer mu.Unlock()
	var outMsgs, inMsgs []string
	for _, e := range *events {
		if e.kind != "frame_out" && e.kind != "frame_in" {
			continue
		}
		for i := 0; i+1 < len(e.fields); i += 2 {
			if e.fields[i] == "msg" {
				if s, ok := e.fields[i+1].(string); ok {
					if e.kind == "frame_out" {
						outMsgs = append(outMsgs, s)
					} else {
						inMsgs = append(inMsgs, s)
					}
				}
			}
		}
	}
	if len(outMsgs) == 0 || len(inMsgs) == 0 {
		t.Fatalf("帧日志缺失：out=%d in=%d", len(outMsgs), len(inMsgs))
	}
	joinedOut := strings.Join(outMsgs, "\n")
	// SDK v1.7.0 握手先发 server/discover（SEP-2575）再 initialize；至少要有握手请求帧。
	if !strings.Contains(joinedOut, `"server/discover"`) && !strings.Contains(joinedOut, `"initialize"`) {
		t.Fatalf("frame_out 应含握手请求（server/discover / initialize），实际:\n%s", joinedOut)
	}
	// 所有帧都是合法 JSON 且是 JSON-RPC 2.0
	for _, m := range append(outMsgs, inMsgs...) {
		var v map[string]any
		if err := json.Unmarshal([]byte(m), &v); err != nil {
			t.Fatalf("帧不是合法 JSON: %v (%s)", err, m)
		}
		if v["jsonrpc"] != "2.0" {
			t.Fatalf("帧缺 jsonrpc=2.0: %s", m)
		}
	}
}

// TestUpstreamStdioConnectDisconnect 验证握手成功后：
// connect_ok 触发、子进程退出 → stderr 行 + disconnect(process_exit) 补发一次。
func TestUpstreamStdioConnectDisconnect(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	hook, events, mu := collectHooks()
	up := NewUpstream(UpstreamConfig{
		Name:    "stdio-server",
		Transport: "stdio",
		Command: exe,
		Env:     map[string]string{"MCPKIT_STDIO_CHILD": "server"},
		LogHook: hook,
	})
	defer up.Close()

	if err := up.Connect(context.Background()); err != nil {
		t.Fatalf("stdio server Connect 应成功：%v", err)
	}

	waitEvents(t, events, mu, "connect_ok", "stderr", "disconnect")

	mu.Lock()
	defer mu.Unlock()
	lines := stderrLines(*events)
	if len(lines) != 2 {
		t.Fatalf("stderr 行数 = %d (%q)，期望 2", len(lines), lines)
	}
	var disconnectReasons []string
	for _, e := range *events {
		if e.kind == "disconnect" {
			for i := 0; i+1 < len(e.fields); i += 2 {
				if e.fields[i] == "reason" {
					if s, ok := e.fields[i+1].(string); ok {
						disconnectReasons = append(disconnectReasons, s)
					}
				}
			}
		}
	}
	// 进程退出触发一次 process_exit；之后 defer Close 因 disconnected 已标记不再重复发。
	if len(disconnectReasons) != 1 || disconnectReasons[0] != "process_exit" {
		t.Fatalf("disconnect 事件 = %v，期望恰好一次 process_exit", disconnectReasons)
	}
}

// TestNewServerToolRegistrationAndCall 验证 NewServer 注册的工具能被客户端列出并调用，
// 且 handler 能收到 args map 并返回文本结果。
func TestNewServerToolRegistrationAndCall(t *testing.T) {
	ctx := context.Background()

	gotArgsCh := make(chan map[string]any, 1)
	server := NewServer("mcpkit-test", []ServerTool{
		{
			Name:        "echo",
			Description: "回显传入的 message",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"message": map[string]any{"type": "string"}}},
			Handler: func(_ context.Context, args map[string]any) (*ToolResult, error) {
				gotArgsCh <- args
				msg, _ := args["message"].(string)
				return &ToolResult{Content: []ContentPart{{Type: "text", Text: "echo:" + msg}}}, nil
			},
		},
		{
			Name:        "add",
			Description: "返回固定文本",
			InputSchema: map[string]any{"type": "object"},
			Handler: func(_ context.Context, _ map[string]any) (*ToolResult, error) {
				return &ToolResult{Content: []ContentPart{{Type: "text", Text: "sum"}}}, nil
			},
		},
	})

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	defer serverSession.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "mcpkit-test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer session.Close()

	tools, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools.Tools) != 2 {
		t.Fatalf("ListTools 返回 %d 个工具，期望 2", len(tools.Tools))
	}
	byName := map[string]bool{}
	for _, tool := range tools.Tools {
		byName[tool.Name] = true
	}
	if !byName["echo"] {
		t.Fatalf("未注册 echo 工具，实际工具：%v", byName)
	}
	if !byName["add"] {
		t.Fatalf("未注册 add 工具，实际工具：%v", byName)
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"message": "你好"}})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatal("CallTool 返回了错误结果")
	}
	if len(result.Content) != 1 {
		t.Fatalf("结果内容片段数 = %d，期望 1", len(result.Content))
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("结果内容类型 = %T，期望 *mcp.TextContent", result.Content[0])
	}
	if text.Text != "echo:你好" {
		t.Fatalf("结果文本 = %q，期望 %q", text.Text, "echo:你好")
	}

	gotArgs := <-gotArgsCh
	if gotArgs == nil {
		t.Fatal("handler 未收到参数 map")
	}
	if gotArgs["message"] != "你好" {
		t.Fatalf("handler 收到 message = %v，期望 %q", gotArgs["message"], "你好")
	}
}

// TestUpstreamInvalidTransportReturnsError 验证非法 Transport 返回错误而不 panic。
func TestUpstreamInvalidTransportReturnsError(t *testing.T) {
	upstream := NewUpstream(UpstreamConfig{Name: "bad", Transport: "bogus"})
	defer upstream.Close()

	if _, err := upstream.ListTools(context.Background()); err == nil {
		t.Fatal("非法 Transport 的 ListTools 应返回错误，实际返回 nil")
	}
	if _, err := upstream.CallTool(context.Background(), "x", nil); err == nil {
		t.Fatal("非法 Transport 的 CallTool 应返回错误，实际返回 nil")
	}
}
