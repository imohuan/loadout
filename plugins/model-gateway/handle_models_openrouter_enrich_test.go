package modelgateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"loadout/core/db"
)

// writeOpenRouterCache 写一份 OpenRouter 元数据缓存并把全局路径指过去。
func writeOpenRouterCache(t *testing.T, content string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "openrouter-models.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	old := openrouterCachePath
	openrouterCachePath = func() string { return path }
	t.Cleanup(func() { openrouterCachePath = old })
}

// TestHandleModelsEnrichesRealModelsFromOpenRouter 真实（渠道）模型也要接入
// OpenRouter 元数据的全部字段：上下文之外还有最大输出、视觉、输入模态、推理。
// 此前只有 context_length 有 openrouter 兜底，其余能力字段只有虚拟模型配置才有——
// 客户端（opencodex/Cursor）读 /v1/models 时拿不到这些声明，图片/思考开关全靠猜。
func TestHandleModelsEnrichesRealModelsFromOpenRouter(t *testing.T) {
	writeOpenRouterCache(t, `[
	  {"id":"zai/glm-5.2","name":"GLM 5.2","context":2000000,"output":128000,"vision":true,"reasoning":true},
	  {"id":"text/text-only-x","name":"Text Only","context":32000,"output":4096,"vision":false,"reasoning":false}
	]`)

	database, err := db.Open(t.TempDir() + "/loadout.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if _, err := database.Exec(`INSERT INTO channels(id, name, base_url, manual_enabled, sync_billing, created_at, updated_at) VALUES
		('c1', 'C1', 'http://u', 1, 1, 'now', 'now')`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// glm-5.2 渠道探测已有上下文（渠道值优先）；text-only-x 探测无上下文（缓存兜底）。
	if _, err := database.Exec(`INSERT INTO channel_models(channel_id, model, source, enabled, context, first_seen_at, last_seen_at) VALUES
		('c1', 'glm-5.2', 'probe', 1, 1048576, ?, ?),
		('c1', 'text-only-x', 'probe', 1, 0, ?, ?)`, now, now, now, now); err != nil {
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

	// 渠道探测的上下文优先，openrouter 只补其余字段。
	glm := byID["glm-5.2"]
	if glm == nil {
		t.Fatalf("缺 glm-5.2: %v", byID)
	}
	if v := glm["context_length"]; v != float64(1048576) {
		t.Errorf("context_length = %v, want 渠道值 1048576", v)
	}
	if v := glm["max_output_tokens"]; v != float64(128000) {
		t.Errorf("max_output_tokens = %v, want 128000", v)
	}
	if v := glm["supports_vision"]; v != true {
		t.Errorf("supports_vision = %v, want true", v)
	}
	if v := glm["supports_reasoning"]; v != true {
		t.Errorf("supports_reasoning = %v, want true", v)
	}
	modalities, ok := glm["input_modalities"].([]any)
	if !ok || len(modalities) != 2 || modalities[0] != "text" || modalities[1] != "image" {
		t.Errorf("input_modalities = %v, want [text image]", glm["input_modalities"])
	}

	// 探测无上下文的模型：上下文从缓存兜底，能力为 false 也要如实输出。
	textOnly := byID["text-only-x"]
	if textOnly == nil {
		t.Fatalf("缺 text-only-x: %v", byID)
	}
	if v := textOnly["context_length"]; v != float64(32000) {
		t.Errorf("context_length = %v, want 缓存值 32000", v)
	}
	if v := textOnly["max_output_tokens"]; v != float64(4096) {
		t.Errorf("max_output_tokens = %v, want 4096", v)
	}
	if v := textOnly["supports_vision"]; v != false {
		t.Errorf("supports_vision = %v, want false", v)
	}
	if v := textOnly["supports_reasoning"]; v != false {
		t.Errorf("supports_reasoning = %v, want false", v)
	}
	modalities, ok = textOnly["input_modalities"].([]any)
	if !ok || len(modalities) != 1 || modalities[0] != "text" {
		t.Errorf("input_modalities = %v, want [text]", textOnly["input_modalities"])
	}
	// openrouter 缓存没有工具调用字段：真实模型不得凭空声明 supports_tool_use。
	if v, ok := glm["supports_tool_use"]; ok {
		t.Errorf("真实模型不应输出 supports_tool_use, got %v", v)
	}
}
