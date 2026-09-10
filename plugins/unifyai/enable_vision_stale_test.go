package unifyai

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loadout/core/config"
)

// TestQueryIgnoresStaleEnableVisionInSyncConfig 验证模型查询不被 sync.json 里的旧
// enableVision 影响。
//
// 回归背景（用户实际踩到）：某次同步把 `enableVision: true` 写进了 sync.json；
// CLI 的 OpenCodex 代理探测依赖 `--enable-vision`，于是之后每次进页面查询都
// 落到「代理关着」的分支，模型列表恒为空 → 「OpenCodex 模型」显示 0 个，
// 而且和页面上「强制视觉」开关的状态完全对不上。
// 修复：查询用的 enableVision 一律由调用方（页面开关）显式传入，不从 sync.json 读。
func TestQueryIgnoresStaleEnableVisionInSyncConfig(t *testing.T) {
	argsOut := setupQueryCapture(t, `{"mode":"all","all":true,"enableVision":true,"source":"~/custom/config.json"}`)

	// 页面开关关闭 → 查询不应带 --enable-vision，尽管 sync.json 里是 true。
	NewService(slog.Default()).OpenCodexModels(false)
	got := readArgs(t, argsOut)
	if strings.Contains(got, "--enable-vision") {
		t.Fatalf("页面开关关闭，查询却带了 --enable-vision（被 sync.json 旧值影响）: %s", got)
	}
	if !strings.Contains(got, `"--source"`) {
		t.Errorf("查询应仍带 --source: %s", got)
	}
}

// TestQueryHonorsExplicitEnableVision 验证显式传入的开关会生效（页面开关打开 → 带上参数）。
func TestQueryHonorsExplicitEnableVision(t *testing.T) {
	argsOut := setupQueryCapture(t, `{"mode":"all","source":"~/custom/config.json"}`)
	NewService(slog.Default()).OpenCodexModels(true)
	got := readArgs(t, argsOut)
	if !strings.Contains(got, "--enable-vision") {
		t.Fatalf("显式开启强制视觉，查询却没带 --enable-vision: %s", got)
	}
}

// TestListAllIgnoresStaleEnableVision 同上，覆盖 --list all（页面初始化走这条）。
func TestListAllIgnoresStaleEnableVision(t *testing.T) {
	argsOut := setupQueryCapture(t, `{"mode":"all","enableVision":true,"source":"~/custom/config.json"}`)
	NewService(slog.Default()).ListAll(false)
	got := readArgs(t, argsOut)
	if strings.Contains(got, "--enable-vision") {
		t.Fatalf("--list all 被 sync.json 旧值影响: %s", got)
	}
}

// setupQueryCapture 覆盖 syncConfigPath + 用 fake node 脚本捕获 CLI 收到的 argv。
func setupQueryCapture(t *testing.T, syncJSON string) (argsOut string) {
	t.Helper()
	oldPath := syncConfigPath
	t.Cleanup(func() { syncConfigPath = oldPath })
	dir := t.TempDir()
	syncPath := filepath.Join(dir, "sync.json")
	syncConfigPath = func() string { return syncPath }
	if err := os.WriteFile(syncPath, []byte(syncJSON), 0o644); err != nil {
		t.Fatalf("写 sync.json: %v", err)
	}

	argsOut = filepath.Join(dir, "args.txt")
	script := fakeCliScript(t, dir, argsOut)

	oldCmd := config.UnifyaiCmd
	config.UnifyaiCmd = "node " + script
	t.Cleanup(func() { config.UnifyaiCmd = oldCmd })

	return argsOut
}

func readArgs(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取捕获 args: %v", err)
	}
	return string(raw)
}
