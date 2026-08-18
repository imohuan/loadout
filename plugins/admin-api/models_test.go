package adminapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"loadout/core/config"
	"loadout/core/db"
	"loadout/core/store"
	"loadout/plugins/admin-auth"
	"loadout/plugins/gateway-keys"
	"loadout/plugins/skills"
	"loadout/plugins/types"
)

// newDBTestServer 组装带 DB（routing 非 nil）的完整服务，用于测 DB 版渠道模型编辑。
func newDBTestServer(t *testing.T) (*httptest.Server, *store.Store, string) {
	t.Helper()
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
	svc := NewService(st, slog.Default(), authSvc, keys, skillSvc, nil)
	svc.SetRoutingServices(database, routing, nil, nil)

	ts := httptest.NewServer(svc.Handler())
	t.Cleanup(ts.Close)
	return ts, st, string(pw)
}

// TestChannelModelsReplaceDB DB 版编辑接口：添加/禁用/删除 + source 保留。
func TestChannelModelsReplaceDB(t *testing.T) {
	ts, _, pw := newDBTestServer(t)
	cookie := login(t, ts, pw)

	// 创建渠道：base_url 不可达 → 探测失败，Models 为空（正是用户场景）。
	resp, data := apiReq(t, ts, http.MethodPost, "/api/channels", map[string]any{
		"name":     "自定义渠道",
		"base_url": "http://127.0.0.1:1/v1", // 不可达，探测必失败
		"enabled":  true,
	}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("创建渠道失败: %d %s", resp.StatusCode, data)
	}
	var created types.Channel
	if err := json.Unmarshal(data, &created); err != nil {
		t.Fatalf("解析创建响应: %v", err)
	}

	// 编辑接口：添加 2 个手动模型（一个禁用）。
	resp, data = apiReq(t, ts, http.MethodPut, "/api/channels/"+created.ID+"/models", []map[string]any{
		{"model": "my-custom-v1", "enabled": true},
		{"model": "old-model", "enabled": false},
	}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("编辑模型期望 200，实际 %d: %s", resp.StatusCode, data)
	}

	// 渠道详情应含 2 个模型（detail 完整清单：一个启用一个禁用），source=manual。
	_, data = apiReq(t, ts, http.MethodGet, "/api/channels", nil, cookie)
	var list []types.Channel
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatalf("解析渠道列表: %v", err)
	}
	var found *types.Channel
	for i := range list {
		if list[i].ID == created.ID {
			found = &list[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("渠道不存在")
	}
	if len(found.ModelsDetail) != 2 {
		t.Fatalf("detail 应有 2 个模型，实际 %+v", found.ModelsDetail)
	}
	detail := map[string]types.ChannelModelDetail{}
	for _, m := range found.ModelsDetail {
		detail[m.Model] = m
	}
	if detail["my-custom-v1"].Source != "manual" || !detail["my-custom-v1"].Enabled {
		t.Fatalf("my-custom-v1 应为 manual 且启用: %+v", detail["my-custom-v1"])
	}
	if detail["old-model"].Enabled {
		t.Fatalf("old-model 应禁用: %+v", detail["old-model"])
	}
	// 兼容字段 Models 只含启用的。
	if len(found.Models) != 1 || found.Models[0] != "my-custom-v1" {
		t.Fatalf("兼容字段 Models 应只含启用的: %+v", found.Models)
	}

	// 再编辑：删掉 old-model，my-custom-v1 改为启用，新增第三个。
	resp, _ = apiReq(t, ts, http.MethodPut, "/api/channels/"+created.ID+"/models", []map[string]any{
		{"model": "my-custom-v1", "enabled": true},
		{"model": "brand-new", "enabled": true},
	}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("二次编辑期望 200，实际 %d", resp.StatusCode)
	}
	_, data = apiReq(t, ts, http.MethodGet, "/api/channels", nil, cookie)
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatalf("解析渠道列表: %v", err)
	}
	for i := range list {
		if list[i].ID == created.ID {
			found = &list[i]
			break
		}
	}
	if len(found.ModelsDetail) != 2 {
		t.Fatalf("二次编辑后 detail 应有 2 个模型，实际 %+v", found.ModelsDetail)
	}
	models := map[string]bool{}
	for _, m := range found.ModelsDetail {
		models[m.Model] = m.Enabled
	}
	if models["old-model"] {
		t.Fatalf("old-model 应被删除")
	}
	if !models["my-custom-v1"] || !models["brand-new"] {
		t.Fatalf("my-custom-v1/brand-new 应存在且启用: %+v", models)
	}
}

// TestMergeManualModels 探测合并：manual 保留，probe 全量替换。
func TestMergeManualModels(t *testing.T) {
	now := "2026-08-18T00:00:00Z"
	existing := []db.ChannelModel{
		{Model: "m-manual", Source: "manual", Enabled: true},
		{Model: "m-probe-old", Source: "probe", Enabled: true},
	}
	probed := []db.ChannelModel{
		{Model: "m-probe-old", Source: "probe", Enabled: true},
		{Model: "m-probe-new", Source: "probe", Enabled: true},
	}
	merged := mergeManualModels(existing, probed, now)

	names := map[string]bool{}
	for _, m := range merged {
		names[m.Model] = true
	}
	if !names["m-manual"] {
		t.Fatalf("手动模型应保留: %+v", names)
	}
	if !names["m-probe-old"] || !names["m-probe-new"] {
		t.Fatalf("探测模型应全量保留: %+v", names)
	}
	if len(merged) != 3 {
		t.Fatalf("合并后应 3 个模型，实际 %d: %+v", len(merged), merged)
	}
}

// TestMergeManualModelsProbeFailed 探测失败（空结果）时手动模型保留。
func TestMergeManualModelsProbeFailed(t *testing.T) {
	existing := []db.ChannelModel{
		{Model: "m-manual", Source: "manual", Enabled: true},
		{Model: "m-probe", Source: "probe", Enabled: true},
	}
	merged := mergeManualModels(existing, nil, "2026-08-18T00:00:00Z")
	if len(merged) != 1 || merged[0].Model != "m-manual" {
		t.Fatalf("探测失败应只保留 manual: %+v", merged)
	}
}

// TestOverviewChannelsDB DB 模式下概览渠道数应来自 sqlite 而非 channels.json。
// 回归：handleOverview 曾只读 JSON 文件，渠道迁库后恒为 0。
func TestOverviewChannelsDB(t *testing.T) {
	ts, st, pw := newDBTestServer(t)
	cookie := login(t, ts, pw)

	// DB 中创建 2 个渠道（走 handleChannelCreateDB，仅写入 sqlite，不落 channels.json）。
	for i := 0; i < 2; i++ {
		resp, data := apiReq(t, ts, http.MethodPost, "/api/channels", map[string]any{
			"name":     "db-channel-" + strconv.Itoa(i),
			"base_url": "http://127.0.0.1:1/v1", // 不可达，探测失败但创建成功
			"enabled":  true,
		}, cookie)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("创建渠道失败: %d %s", resp.StatusCode, data)
		}
	}

	// channels.json 应不存在（数据全在 DB）。
	if err := st.Read(types.FileChannels, &struct{}{}); err == nil {
		t.Fatal("DB 模式下 channels.json 不应存在")
	}

	// 概览渠道数应为 2。
	resp, data := apiReq(t, ts, http.MethodGet, "/api/overview", nil, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("overview 期望 200，实际 %d: %s", resp.StatusCode, data)
	}
	var overview struct {
		Channels int `json:"channels"`
	}
	if err := json.Unmarshal(data, &overview); err != nil {
		t.Fatalf("解析 overview: %v", err)
	}
	if overview.Channels != 2 {
		t.Fatalf("DB 模式概览渠道数应 2，实际 %d", overview.Channels)
	}
}
