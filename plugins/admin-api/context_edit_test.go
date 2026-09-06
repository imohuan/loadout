package adminapi

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"loadout/core/config"
	"loadout/core/db"
	"loadout/core/store"
	"loadout/plugins/admin-auth"
	"loadout/plugins/gateway-keys"
	"loadout/plugins/skills"
	unifyai "loadout/plugins/unifyai"
)

// TestChannelModelsReplaceKeepsContext 手动编辑已探测目录时不得清空已持久化的 context。
// 回归：之前 handleChannelModelsReplaceDB 重建 values 只透传 source、漏了 context，
// 而 ReplaceChannelModels 是 DELETE + 全量重插，导致一次手动编辑把探测到的上下文窗口清 0。
func TestChannelModelsReplaceKeepsContext(t *testing.T) {
	// 自建 DB 版服务，持有 repository 以便直接断言库里的 context。
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	old := config.AdminPasswordFile
	config.AdminPasswordFile = filepath.Join(dir, "admin-password")
	t.Cleanup(func() { config.AdminPasswordFile = old })
	authSvc := adminauth.NewService(st, slog.Default())
	if _, err := authSvc.EnsureFirstRun(); err != nil {
		t.Fatalf("EnsureFirstRun: %v", err)
	}
	pw, err := os.ReadFile(config.AdminPasswordFile)
	if err != nil {
		t.Fatalf("读取初始密码: %v", err)
	}
	database, err := db.OpenForStore(st)
	if err != nil {
		t.Fatalf("db.OpenForStore: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	routing, err := db.NewRepository(database)
	if err != nil {
		t.Fatalf("db.NewRepository: %v", err)
	}
	keys := gatewaykeys.NewManager(st)
	skillSvc := skills.NewService(st, slog.Default(), t.TempDir(), t.TempDir())
	svc := NewService(st, slog.Default(), authSvc, keys, skillSvc, nil, unifyai.NewService(slog.Default()))
	svc.SetRoutingServices(database, routing, nil, nil)
	ts := httptest.NewServer(svc.Handler())
	t.Cleanup(ts.Close)
	cookie := login(t, ts, string(pw))

	// 直接造一个渠道 + 一个探测来源、带 context 的模型（模拟渠道探测已落库）。
	now := time.Now().UTC().Format(time.RFC3339Nano)
	channelID := "ctx-chan"
	if _, err := database.Exec(`INSERT INTO channels(id, name, base_url, manual_enabled, sync_billing, created_at, updated_at)
		VALUES (?, 'ctx-chan', 'http://upstream/v1', 1, 1, ?, ?)`, channelID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO channel_models(channel_id, model, source, enabled, context, first_seen_at, last_seen_at)
		VALUES (?, 'probed-model', 'probe', 1, 500000, ?, ?)`, channelID, now, now); err != nil {
		t.Fatal(err)
	}

	// 手动编辑：保留 probed-model 并新增一个模型。
	resp, data := apiReq(t, ts, http.MethodPut, "/api/channels/"+channelID+"/models", []map[string]any{
		{"model": "probed-model", "enabled": true},
		{"model": "fresh-model", "enabled": true},
	}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("编辑模型期望 200，实际 %d: %s", resp.StatusCode, data)
	}

	// 断言库里 probed-model 的 context 被保留，新增的 fresh-model 无 context(0) 且 source=manual。
	got, err := routing.ListChannelModels(t.Context(), channelID)
	if err != nil {
		t.Fatalf("ListChannelModels: %v", err)
	}
	byName := map[string]db.ChannelModel{}
	for _, m := range got {
		byName[m.Model] = m
	}
	if len(byName) != 2 {
		t.Fatalf("编辑后应有 2 个模型，实际 %d: %+v", len(byName), byName)
	}
	if c := byName["probed-model"]; c.Context != 500000 {
		t.Fatalf("probed-model context 应保留 500000，实际 %d（一次手动编辑不应清空已探测的上下文）", c.Context)
	}
	if c := byName["fresh-model"]; c.Context != 0 || c.Source != "manual" {
		t.Fatalf("fresh-model 应为新手动模型、无 context，实际 context=%d source=%q", c.Context, c.Source)
	}
}
