package unifyai

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// TestSyncConfigEmptyWhenMissing 验证 sync.json 不存在时 SyncConfig 返回空 map 不报错。
func TestSyncConfigEmptyWhenMissing(t *testing.T) {
	old := syncConfigPath
	defer func() { syncConfigPath = old }()
	p := filepath.Join(t.TempDir(), "sync.json")
	syncConfigPath = func() string { return p }

	svc := NewService(slog.Default())
	cfg, err := svc.SyncConfig()
	if err != nil {
		t.Fatalf("SyncConfig 不应报错: %v", err)
	}
	if len(cfg) != 0 {
		t.Errorf("cfg = %v, want 空 map", cfg)
	}
}

// TestUpdateSourcePersistsAndPreservesOtherFields 验证 UpdateSource 只改 source 字段，
// 其他字段原样保留，且能读回。
func TestUpdateSourcePersistsAndPreservesOtherFields(t *testing.T) {
	old := syncConfigPath
	defer func() { syncConfigPath = old }()
	p := filepath.Join(t.TempDir(), "sync.json")
	syncConfigPath = func() string { return p }

	os.WriteFile(p, []byte(`{"mode":"all","platforms":["opencode"],"forceMcp":true,"source":"old"}`), 0o644)

	svc := NewService(slog.Default())
	if _, err := svc.UpdateSource("~/custom/config.json"); err != nil {
		t.Fatalf("UpdateSource: %v", err)
	}

	cfg, err := svc.SyncConfig()
	if err != nil {
		t.Fatalf("SyncConfig: %v", err)
	}
	if cfg["source"] != "~/custom/config.json" {
		t.Errorf("source = %v, want ~/custom/config.json", cfg["source"])
	}
	if cfg["mode"] != "all" {
		t.Errorf("mode = %v, want all（应保留）", cfg["mode"])
	}
	if cfg["forceMcp"] != true {
		t.Errorf("forceMcp = %v, want true（应保留）", cfg["forceMcp"])
	}
}

// TestUpdateSourceCreatesFile 验证文件不存在时 UpdateSource 能新建。
func TestUpdateSourceCreatesFile(t *testing.T) {
	old := syncConfigPath
	defer func() { syncConfigPath = old }()
	p := filepath.Join(t.TempDir(), "sync.json")
	syncConfigPath = func() string { return p }

	svc := NewService(slog.Default())
	if _, err := svc.UpdateSource("~/x/config.json"); err != nil {
		t.Fatalf("UpdateSource(新建): %v", err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读取文件: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("解析: %v", err)
	}
	if doc["source"] != "~/x/config.json" {
		t.Errorf("source = %v, want ~/x/config.json", doc["source"])
	}
}

// TestSourceFromSync 验证 sourceFromSync 从 sync.json 正确取 source，未配置返回空串。
func TestSourceFromSync(t *testing.T) {
	old := syncConfigPath
	defer func() { syncConfigPath = old }()
	p := filepath.Join(t.TempDir(), "sync.json")
	syncConfigPath = func() string { return p }
	svc := NewService(slog.Default())

	if got := svc.sourceFromSync(); got != "" {
		t.Errorf("未配置 sourceFromSync = %q, want 空串", got)
	}

	svc.UpdateSource("~/ok/config.json")
	if got := svc.sourceFromSync(); got != "~/ok/config.json" {
		t.Errorf("配置后 sourceFromSync = %q, want ~/ok/config.json", got)
	}
}
