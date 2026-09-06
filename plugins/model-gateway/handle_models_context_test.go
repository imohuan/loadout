package modelgateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"loadout/core/db"
)

// TestHandleModelsDBEmitsContext DB 路径端到端：渠道模型带 context 时，
// /v1/models 应输出 context_length；无 context（0）的模型不输出该字段。
func TestHandleModelsDBEmitsContext(t *testing.T) {
	database, err := db.Open(t.TempDir() + "/loadout.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	// 隔离 openrouter 缓存，避免命中本机真实 ~/.unifyai 缓存污染断言。
	old := openrouterCachePath
	openrouterCachePath = func() string { return t.TempDir() + "/nonexistent.json" }
	t.Cleanup(func() { openrouterCachePath = old })

	if _, err := database.Exec(`INSERT INTO channels(id, name, base_url, manual_enabled, sync_billing, created_at, updated_at) VALUES
		('c1', 'C1', 'http://u', 1, 1, 'now', 'now')`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// ctx-a 带上下文，ctx-b 上下文未知(0)，virtual 走聚合模型名。
	if _, err := database.Exec(`INSERT INTO channel_models(channel_id, model, source, enabled, context, first_seen_at, last_seen_at) VALUES
		('c1', 'ctx-a', 'probe', 1, 1000000, ?, ?),
		('c1', 'ctx-b', 'probe', 1, 0, ?, ?)`, now, now, now, now); err != nil {
		t.Fatal(err)
	}

	svc, _ := newTestService(t)
	svc.SetRoutingServices(database, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	svc.HandleModels(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d", rec.Code)
	}
	var resp struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	byID := map[string]map[string]any{}
	for _, d := range resp.Data {
		id, _ := d["id"].(string)
		byID[id] = d
	}
	if len(byID) != 2 {
		t.Fatalf("模型数 = %d, want 2: %v", len(byID), byID)
	}
	if _, ok := byID["ctx-a"]; !ok {
		t.Fatalf("缺 ctx-a: %v", byID)
	}
	if v, ok := byID["ctx-a"]["context_length"]; !ok || v != float64(1000000) {
		t.Fatalf("ctx-a 应输出 context_length=1000000, got %v", byID["ctx-a"])
	}
	if _, ok := byID["ctx-b"]["context_length"]; ok {
		t.Fatalf("ctx-b 上下文未知(0)不应输出 context_length: %v", byID["ctx-b"])
	}
}
