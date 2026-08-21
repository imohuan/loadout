package mcpkit

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

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
