package multimodalmcp

import (
	"log/slog"
	"testing"

	"loadout/core/store"
	mcphub "loadout/plugins/mcp-hub"
)

// newHubForTest 构造一个基于 JSON 存储的 mcp-hub Service（repo=nil 走 store JSON）。
func newHubForTest(t *testing.T) *mcphub.Service {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	return mcphub.NewService(st, slog.Default(), nil)
}

// hubHasBuiltinServer 判断 hub 的「内置 server」内存注册表里是否存在指定 ID 的内置 server
// （内置 server 不落库，从 BuiltinServers() 查，而非 ListServers）。
func hubHasBuiltinServer(t *testing.T, hub *mcphub.Service, id string) bool {
	t.Helper()
	for _, srv := range hub.BuiltinServers() {
		if srv.ID == id {
			return true
		}
	}
	return false
}

// TestSyncHubRegistrationEnabled 验证：多模态启用时，内置 server 注册进 mcp-hub。
func TestSyncHubRegistrationEnabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	s := newConfigService(t, cfg)
	hub := newHubForTest(t)
	s.SetMcpHub(hub)

	if err := s.syncHubRegistration(); err != nil {
		t.Fatalf("syncHubRegistration: %v", err)
	}
	if !hubHasBuiltinServer(t, hub, builtinServerID) {
		t.Error("启用后内置 server 应注册进 mcp-hub")
	}

	// 工具应进入 $smart 聚合。
	idx, err := hub.BuildIndex(t.Context())
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range idx.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"understand_image", "understand_video", "understand_audio"} {
		if !names[want] {
			t.Errorf("$smart 聚合缺少内置工具 %s", want)
		}
	}
}

// TestSyncHubRegistrationDisabled 验证：多模态关闭时，内置 server 从 mcp-hub 注销。
func TestSyncHubRegistrationDisabled(t *testing.T) {
	// 先启用注册，再关闭注销。
	cfgOn := DefaultConfig()
	cfgOn.Enabled = true
	s := newConfigService(t, cfgOn)
	hub := newHubForTest(t)
	s.SetMcpHub(hub)
	if err := s.syncHubRegistration(); err != nil {
		t.Fatalf("syncHubRegistration(启用): %v", err)
	}
	if !hubHasBuiltinServer(t, hub, builtinServerID) {
		t.Fatal("前置：启用后内置 server 应已注册")
	}

	cfgOn.Enabled = false
	if err := s.saveConfig(cfgOn); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	if err := s.syncHubRegistration(); err != nil {
		t.Fatalf("syncHubRegistration(关闭): %v", err)
	}
	if hubHasBuiltinServer(t, hub, builtinServerID) {
		t.Error("关闭后内置 server 应从 mcp-hub 注销")
	}
}

// TestSyncHubRegistrationNilHub 验证：未注入 hub 时安全跳过（单测/独立场景）。
func TestSyncHubRegistrationNilHub(t *testing.T) {
	s := newConfigService(t, DefaultConfig())
	if err := s.syncHubRegistration(); err != nil {
		t.Fatalf("hub 为 nil 时 syncHubRegistration 应安全跳过，got: %v", err)
	}
}
