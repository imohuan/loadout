package mcphub

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"loadout/core/mcpkit"
	"loadout/core/store"
	"loadout/plugins/types"
)

// builtinTestHandler 生成一个固定返回的内置 handler。
func builtinTestHandler(name string) func(context.Context, map[string]any) (*mcpkit.ToolResult, error) {
	return func(_ context.Context, _ map[string]any) (*mcpkit.ToolResult, error) {
		return &mcpkit.ToolResult{Content: []mcpkit.ContentPart{{Type: "text", Text: "builtin:" + name}}}, nil
	}
}

// TestRegisterBuiltinServer 验证：注册内置 server 后，工具进入索引与 $smart 聚合，可被调用。
func TestRegisterBuiltinServer(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	s := NewService(st, slog.Default(), nil)

	srv := types.MCPServer{ID: "builtin-mm", Name: "multimodal", Transport: types.TransportHTTP, Enabled: true, Builtin: true}
	tools := []ToolEntry{
		{Name: "understand_image", Description: "图片", InputSchema: map[string]any{"type": "object"}, BuiltinHandler: builtinTestHandler("img")},
		{Name: "understand_video", Description: "视频", InputSchema: map[string]any{"type": "object"}, BuiltinHandler: builtinTestHandler("vid")},
	}
	if err := s.RegisterBuiltinServer(context.Background(), srv, tools); err != nil {
		t.Fatalf("RegisterBuiltinServer: %v", err)
	}

	idx, err := s.BuildIndex(context.Background())
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	if !hasTool(idx.Tools, "understand_image") || !hasTool(idx.Tools, "understand_video") {
		t.Fatalf("内置工具未进入索引，tools=%v", toolNames(idx.Tools))
	}

	// 经 $smart 聚合调用内置工具 → 走内置 handler（不经上游）。
	out, err := s.Invoke(context.Background(), "/mcp/$smart", map[string]any{"tool": "understand_image"})
	if err != nil {
		t.Fatalf("Invoke understand_image: %v", err)
	}
	if !strings.Contains(out, "builtin:img") {
		t.Errorf("Invoke 结果 = %q, 应包含 builtin:img", out)
	}

	// 内置 server 出现在上游列表（仅内存注册表，不落库）。
	servers := s.BuiltinServers()
	found := false
	for _, srv2 := range servers {
		if srv2.ID == "builtin-mm" && srv2.Builtin {
			found = true
		}
	}
	if !found {
		t.Error("内置 server 未出现在 MCP 服务器列表")
	}

	// 不应落库：readServers 只读数据库/JSON，不包含内置 server。
	if dbServers, err := s.readServers(); err != nil {
		t.Fatal(err)
	} else {
		for _, srv2 := range dbServers {
			if srv2.ID == "builtin-mm" {
				t.Error("内置 server 不应出现在数据库（不落库）")
			}
		}
	}

	// 状态查询能识别内存内置 server（不再因查库失败而误报 stopped）。
	state, err := s.ServerStatus("builtin-mm")
	if err != nil {
		t.Fatalf("ServerStatus: %v", err)
	}
	if state != StateRunning {
		t.Errorf("内置 server 状态 = %q, want %q", state, StateRunning)
	}
}

// TestUnregisterBuiltinServer 验证：注销后工具从索引消失、server 从列表移除。
func TestUnregisterBuiltinServer(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	s := NewService(st, slog.Default(), nil)

	srv := types.MCPServer{ID: "builtin-mm", Name: "multimodal", Transport: types.TransportHTTP, Enabled: true, Builtin: true}
	if err := s.RegisterBuiltinServer(context.Background(), srv, []ToolEntry{
		{Name: "understand_image", Description: "图片", InputSchema: map[string]any{"type": "object"}, BuiltinHandler: builtinTestHandler("img")},
	}); err != nil {
		t.Fatalf("RegisterBuiltinServer: %v", err)
	}
	if err := s.UnregisterBuiltinServer(context.Background(), "builtin-mm"); err != nil {
		t.Fatalf("UnregisterBuiltinServer: %v", err)
	}

	idx, err := s.BuildIndex(context.Background())
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	if hasTool(idx.Tools, "understand_image") {
		t.Error("注销后内置工具不应再出现在索引")
	}

	servers := s.BuiltinServers()
	for _, srv2 := range servers {
		if srv2.ID == "builtin-mm" {
			t.Error("注销后内置 server 不应再出现在列表")
		}
	}
}

// TestRegisterBuiltinServerIdempotent 验证：重复注册同 ID 幂等，不产生重复条目。
func TestRegisterBuiltinServerIdempotent(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	s := NewService(st, slog.Default(), nil)

	srv := types.MCPServer{ID: "builtin-mm", Name: "multimodal", Transport: types.TransportHTTP, Enabled: true, Builtin: true}
	for i := 0; i < 2; i++ {
		if err := s.RegisterBuiltinServer(context.Background(), srv, []ToolEntry{
			{Name: "understand_image", Description: "图片", InputSchema: map[string]any{"type": "object"}, BuiltinHandler: builtinTestHandler("img")},
		}); err != nil {
			t.Fatalf("RegisterBuiltinServer: %v", err)
		}
	}

	servers := s.BuiltinServers()
	count := 0
	for _, srv2 := range servers {
		if srv2.ID == "builtin-mm" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("重复注册后内置 server 条数 = %d, want 1", count)
	}

	// 工具也应只有 1 份。
	idx, err := s.BuildIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, t := range idx.Tools {
		if t.Name == "understand_image" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("重复注册后工具条数 = %d, want 1", n)
	}
}

// TestBuiltinServerDisabled 验证：内置 server 被禁用（enabled=false）时工具不进索引。
func TestBuiltinServerDisabled(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	s := NewService(st, slog.Default(), nil)

	// 先注册 enabled，再改为 disabled 重新注册。
	srv := types.MCPServer{ID: "builtin-mm", Name: "multimodal", Transport: types.TransportHTTP, Enabled: false, Builtin: true}
	if err := s.RegisterBuiltinServer(context.Background(), srv, []ToolEntry{
		{Name: "understand_image", Description: "图片", InputSchema: map[string]any{"type": "object"}, BuiltinHandler: builtinTestHandler("img")},
	}); err != nil {
		t.Fatalf("RegisterBuiltinServer: %v", err)
	}

	idx, err := s.BuildIndex(context.Background())
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	if hasTool(idx.Tools, "understand_image") {
		t.Error("禁用的内置 server 工具不应进索引")
	}
}
