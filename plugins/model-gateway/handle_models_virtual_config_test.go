package modelgateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"loadout/core/db"
)

// TestHandleModelsEmitsVirtualModelConfig 虚拟（聚合）模型配了「模型配置」后，
// /v1/models 里该虚拟模型那一行必须带上上下文、最大输出与能力标记——
// Codex / Cursor 就是靠这些字段决定能开多大上下文、能不能发图。
func TestHandleModelsEmitsVirtualModelConfig(t *testing.T) {
	database, err := db.Open(t.TempDir() + "/loadout.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	// 隔离 openrouter 缓存，避免命中本机真实缓存污染断言。
	old := openrouterCachePath
	openrouterCachePath = func() string { return t.TempDir() + "/nonexistent.json" }
	t.Cleanup(func() { openrouterCachePath = old })

	if _, err := database.Exec(`INSERT INTO channels(id, name, base_url, manual_enabled, sync_billing, created_at, updated_at) VALUES
		('c1', 'C1', 'http://u', 1, 1, 'now', 'now')`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := database.Exec(`INSERT INTO channel_models(channel_id, model, source, enabled, context, first_seen_at, last_seen_at) VALUES
		('c1', 'real-a', 'probe', 1, 0, ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	// 虚拟模型 deepseek-auto：配上下文 + 最大输出 + 视觉/推理能力。
	if _, err := database.Exec(`INSERT INTO aggregates(name, enabled, created_at, updated_at, config_json) VALUES
		('deepseek-auto', 1, ?, ?, '{"context_length":1048576,"max_output_tokens":384000,"capabilities":["vision","reasoning"]}')`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO aggregate_targets(aggregate_id, position, model, channel_id, channel_ids_json, channel_base_url) VALUES
		((SELECT id FROM aggregates WHERE name='deepseek-auto'), 0, 'real-a', 'c1', '[]', '')`); err != nil {
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
	virtual, ok := byID["deepseek-auto"]
	if !ok {
		t.Fatalf("虚拟模型未出现在 /v1/models: %v", byID)
	}
	if v := virtual["context_length"]; v != float64(1048576) {
		t.Errorf("context_length = %v, want 1048576", v)
	}
	if v := virtual["max_output_tokens"]; v != float64(384000) {
		t.Errorf("max_output_tokens = %v, want 384000", v)
	}
	if v := virtual["supports_vision"]; v != true {
		t.Errorf("supports_vision = %v, want true", v)
	}
	if v := virtual["supports_reasoning"]; v != true {
		t.Errorf("supports_reasoning = %v, want true", v)
	}
	// 未勾选的能力不应出现（当前只勾了 vision + reasoning）。
	if v, ok := virtual["supports_tool_use"]; ok {
		t.Errorf("未勾选工具调用不应输出 supports_tool_use, got %v", v)
	}
	// 视觉能力同时投影成 input_modalities，客户端据此放开图片上传。
	modalities, ok := virtual["input_modalities"].([]any)
	if !ok || len(modalities) != 2 {
		t.Fatalf("input_modalities = %v, want [text image]", virtual["input_modalities"])
	}
	if modalities[0] != "text" || modalities[1] != "image" {
		t.Errorf("input_modalities = %v, want [text image]", modalities)
	}
}

// TestHandleModelsVirtualWithoutConfigUnchanged 没配模型配置的虚拟模型，
// 那一行仍只有 id / object（与改造前一致），不凭空多出能力字段。
func TestHandleModelsVirtualWithoutConfigUnchanged(t *testing.T) {
	database, err := db.Open(t.TempDir() + "/loadout.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	old := openrouterCachePath
	openrouterCachePath = func() string { return t.TempDir() + "/nonexistent.json" }
	t.Cleanup(func() { openrouterCachePath = old })

	if _, err := database.Exec(`INSERT INTO channels(id, name, base_url, manual_enabled, sync_billing, created_at, updated_at) VALUES
		('c1', 'C1', 'http://u', 1, 1, 'now', 'now')`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := database.Exec(`INSERT INTO channel_models(channel_id, model, source, enabled, context, first_seen_at, last_seen_at) VALUES
		('c1', 'real-a', 'probe', 1, 0, ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO aggregates(name, enabled, created_at, updated_at) VALUES ('plain-auto', 1, ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO aggregate_targets(aggregate_id, position, model, channel_id, channel_ids_json, channel_base_url) VALUES
		((SELECT id FROM aggregates WHERE name='plain-auto'), 0, 'real-a', 'c1', '[]', '')`); err != nil {
		t.Fatal(err)
	}

	svc, _ := newTestService(t)
	svc.SetRoutingServices(database, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	svc.HandleModels(rec, req)
	var resp struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	var virtual map[string]any
	for _, d := range resp.Data {
		if d["id"] == "plain-auto" {
			virtual = d
		}
	}
	if virtual == nil {
		t.Fatal("虚拟模型未出现在 /v1/models")
	}
	if len(virtual) != 2 {
		t.Fatalf("未配置的虚拟模型应只有 id/object, got %v", virtual)
	}
}
