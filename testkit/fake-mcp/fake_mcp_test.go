package fakemcp

import (
	"context"
	"testing"

	"loadout/core/mcpkit"
)

// TestUpstreamListAndCall 验证通过 mcpkit.NewUpstream 连接假上游后，
// ListTools 能看到注册的工具，CallTool 返回正确文本，且调用被记录。
func TestUpstreamListAndCall(t *testing.T) {
	ctx := context.Background()

	f := New("fake", []Tool{
		{
			Name:        "echo",
			Description: "回显消息",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"message": map[string]any{"type": "string"}}},
			Result:      "echo-ok",
		},
		{
			Name:        "noop",
			Description: "无返回文本，应回落为 ok",
			InputSchema: map[string]any{"type": "object"},
		},
	})
	defer f.Close()

	upstream := mcpkit.NewUpstream(mcpkit.UpstreamConfig{
		Name:      "fake",
		Transport: "http",
		URL:       f.URL(),
	})
	defer upstream.Close()

	tools, err := upstream.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("ListTools 返回 %d 个工具，期望 2", len(tools))
	}
	byName := map[string]bool{}
	for _, tool := range tools {
		byName[tool.Name] = true
	}
	if !byName["echo"] || !byName["noop"] {
		t.Fatalf("缺少注册的工具，实际：%v", byName)
	}

	res, err := upstream.CallTool(ctx, "echo", map[string]any{"message": "你好"})
	if err != nil {
		t.Fatalf("CallTool(echo): %v", err)
	}
	if res.IsError {
		t.Fatal("CallTool(echo) 返回了错误结果")
	}
	if len(res.Content) != 1 || res.Content[0].Type != "text" || res.Content[0].Text != "echo-ok" {
		t.Fatalf("CallTool(echo) 结果 = %+v，期望文本 echo-ok", res.Content)
	}

	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("Calls 记录数 = %d，期望 1", len(calls))
	}
	if calls[0].Name != "echo" {
		t.Fatalf("记录的工具名 = %q，期望 echo", calls[0].Name)
	}
	if calls[0].Args["message"] != "你好" {
		t.Fatalf("记录的参数 message = %v，期望 你好", calls[0].Args["message"])
	}

	// 调用 noop：验证空 Result 回落为 "ok"，并记录第二次调用。
	res2, err := upstream.CallTool(ctx, "noop", nil)
	if err != nil {
		t.Fatalf("CallTool(noop): %v", err)
	}
	if len(res2.Content) != 1 || res2.Content[0].Text != "ok" {
		t.Fatalf("CallTool(noop) 结果 = %+v，期望文本 ok", res2.Content)
	}
	calls = f.Calls()
	if len(calls) != 2 {
		t.Fatalf("Calls 记录数 = %d，期望 2", len(calls))
	}
	if calls[1].Name != "noop" {
		t.Fatalf("第二条记录的工具名 = %q，期望 noop", calls[1].Name)
	}
}

// TestDefaultResultEmpty 验证单个工具、空 Result 时返回 "ok"。
func TestDefaultResultEmpty(t *testing.T) {
	ctx := context.Background()

	f := New("fake", []Tool{{Name: "ping", Description: "心跳", InputSchema: map[string]any{"type": "object"}}})
	defer f.Close()

	upstream := mcpkit.NewUpstream(mcpkit.UpstreamConfig{Name: "fake", Transport: "http", URL: f.URL()})
	defer upstream.Close()

	res, err := upstream.CallTool(ctx, "ping", nil)
	if err != nil {
		t.Fatalf("CallTool(ping): %v", err)
	}
	if len(res.Content) != 1 || res.Content[0].Text != "ok" {
		t.Fatalf("CallTool(ping) 结果 = %+v，期望文本 ok", res.Content)
	}
}
