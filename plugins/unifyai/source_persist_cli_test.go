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

// fakeCliScript 生成一个 fake node 脚本：把收到的全部 argv 写入 argsOut（固定路径）。
func fakeCliScript(t *testing.T, dir, argsOut string) string {
	t.Helper()
	script := filepath.Join(dir, "fake_cli.mjs")
	pathLit := strconv.Quote(filepath.ToSlash(argsOut))
	body := "import fs from 'node:fs'; fs.writeFileSync(" + pathLit + ", JSON.stringify(process.argv.slice(2)));\n"
	if err := os.WriteFile(script, []byte(body), 0o644); err != nil {
		t.Fatalf("写 fake 脚本: %v", err)
	}
	return script
}

// setupSourceTest 统一覆盖 syncConfigPath + 预置带 source 的 sync.json + 指向 fake CLI。
func setupSourceTest(t *testing.T) (svc *Service, argsOut string) {
	t.Helper()
	oldPath := syncConfigPath
	t.Cleanup(func() { syncConfigPath = oldPath })
	dir := t.TempDir()
	syncPath := filepath.Join(dir, "sync.json")
	syncConfigPath = func() string { return syncPath }
	if err := os.WriteFile(syncPath, []byte(`{"source":"~/custom/models.json"}`), 0o644); err != nil {
		t.Fatalf("写 sync.json: %v", err)
	}

	argsOut = filepath.Join(dir, "args.txt")
	script := fakeCliScript(t, dir, argsOut)

	oldCmd := config.UnifyaiCmd
	config.UnifyaiCmd = "node " + script
	t.Cleanup(func() { config.UnifyaiCmd = oldCmd })

	return NewService(slog.Default()), argsOut
}

// TestListAllPassesSourceToCLI 验证 ListAll 把 sync.json 里持久化的 source 以 --source 传给 CLI。
func TestListAllPassesSourceToCLI(t *testing.T) {
	svc, argsOut := setupSourceTest(t)
	if _, err := svc.ListAll(); err != nil {
		t.Fatalf("ListAll: %v", err)
	}

	raw, err := os.ReadFile(argsOut)
	if err != nil {
		t.Fatalf("读取捕获 args: %v", err)
	}
	got := string(raw)
	t.Logf("CLI 收到的 args: %s", got)
	if !strings.Contains(got, `"--source"`) || strings.Contains(got, `~`) {
		t.Errorf("args 缺少 --source，got %s", got)
	}
}

// TestOpenCodexModelsPassesSourceToCLI 验证 OpenCodexModels 同样把 source 以 --source 传给 CLI。
func TestOpenCodexModelsPassesSourceToCLI(t *testing.T) {
	svc, argsOut := setupSourceTest(t)
	svc.OpenCodexModels(false)

	raw, err := os.ReadFile(argsOut)
	if err != nil {
		t.Fatalf("读取捕获 args: %v", err)
	}
	got := string(raw)
	t.Logf("CLI 收到的 args: %s", got)
	if !strings.Contains(got, `"--source"`) || strings.Contains(got, `~`) {
		t.Errorf("args 缺少 --source，got %s", got)
	}
}
