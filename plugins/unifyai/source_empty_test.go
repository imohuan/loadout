package unifyai

import (
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"loadout/core/config"
)

// TestUpdateSourceEmptyAllowsClear 验证空串 source 也能保存（清空语义），
// 与前端失焦保存允许清空、点同步整包写 source 同源的行为保持一致。
func TestUpdateSourceEmptyAllowsClear(t *testing.T) {
	old := syncConfigPath
	defer func() { syncConfigPath = old }()
	p := filepath.Join(t.TempDir(), "sync.json")
	syncConfigPath = func() string { return p }
	os.WriteFile(p, []byte(`{"source":"~/some/path.json"}`), 0o644)

	svc := NewService(slog.Default())
	if _, err := svc.UpdateSource(""); err != nil {
		t.Fatalf("UpdateSource(空串): %v", err)
	}
	cfg, err := svc.SyncConfig()
	if err != nil {
		t.Fatalf("SyncConfig: %v", err)
	}
	if cfg["source"] != "" {
		t.Errorf("source = %q, want 空串", cfg["source"])
	}
	if got := svc.sourceFromSync(); got != "" {
		t.Errorf("sourceFromSync = %q, want 空串（不拼 --source）", got)
	}
}

// TestEmptySourceNotPassedToCLI 验证 sync.json 里 source 为空串时，ListAll 不拼 --source。
func TestEmptySourceNotPassedToCLI(t *testing.T) {
	oldPath := syncConfigPath
	t.Cleanup(func() { syncConfigPath = oldPath })
	dir := t.TempDir()
	syncPath := filepath.Join(dir, "sync.json")
	syncConfigPath = func() string { return syncPath }
	os.WriteFile(syncPath, []byte(`{"source":""}`), 0o644)

	// fake CLI 捕获 argv，与 TestListAllPassesSourceToCLI 同模式
	argsOut := filepath.Join(dir, "args.txt")
	script := filepath.Join(dir, "fake_cli.mjs")
	os.WriteFile(script, []byte("import fs from 'node:fs'; fs.writeFileSync("+
		strconv.Quote(filepath.ToSlash(argsOut))+", JSON.stringify(process.argv.slice(2)));\n"), 0o644)

	oldCmd := config.UnifyaiCmd
	config.UnifyaiCmd = "node " + script
	t.Cleanup(func() { config.UnifyaiCmd = oldCmd })

	svc := NewService(slog.Default())
	if _, err := svc.ListAll(); err != nil {
		t.Fatalf("ListAll(空 source): %v", err)
	}
	raw, err := os.ReadFile(argsOut)
	if err != nil {
		t.Fatalf("读取捕获 args: %v", err)
	}
	got := string(raw)
	t.Logf("CLI 收到的 args: %s", got)
	if strings.Contains(got, `"--source"`) {
		t.Errorf("空 source 不应拼 --source，got %s", got)
	}
}
