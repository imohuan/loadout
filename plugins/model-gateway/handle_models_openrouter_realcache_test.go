package modelgateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"loadout/core/db"
)

// TestHandleModelsRealCacheEndToEnd 用与本机 ~/.unifyai 缓存同构的数据
// 验证 /v1/models 最终输出形状：真实模型带全字段，虚拟模型带配置字段。
// 这是用户可见行为的最终锚点——线上请求就长这样。
func TestHandleModelsRealCacheEndToEnd(t *testing.T) {
	writeOpenRouterCache(t, `[
	  {"id":"zai/glm-5.2","name":"GLM 5.2","context":1048576,"output":131072,"vision":false,"reasoning":true},
	  {"id":"deepseek/deepseek-v4.1-flash","name":"DeepSeek V4.1 Flash","context":1048576,"output":384000,"vision":true,"reasoning":true}
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
	// glm-5.2 渠道探测没探到上下文（真实环境常见），深度依赖缓存兜底。
	if _, err := database.Exec(`INSERT INTO channel_models(channel_id, model, source, enabled, first_seen_at, last_seen_at) VALUES
		('c1', 'glm-5.2', 'probe', 1, 'now', 'now'),
		('c1', 'deepseek-v4.1-flash', 'probe', 1, 'now', 'now')`); err != nil {
		t.Fatal(err)
	}
	// 虚拟模型：手动配置了上下文（用户上一轮配的 deepseek-auto 场景）。
	if _, err := database.Exec(`INSERT INTO aggregates(name, enabled, created_at, updated_at, config_json) VALUES
		('deepseek-auto', 1, 'now', 'now', '{"context_length":1048576,"capabilities":["vision","reasoning"]}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO aggregate_targets(aggregate_id, position, model, channel_id, channel_ids_json, channel_base_url) VALUES
		((SELECT id FROM aggregates WHERE name='deepseek-auto'), 0, 'glm-5.2', 'c1', '[]', '')`); err != nil {
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
	byID := map[string]map[string]any{}
	for _, d := range resp.Data {
		id, _ := d["id"].(string)
		byID[id] = d
	}

	// glm-5.2：缓存兜底上下文 + 推理声明；非视觉模型如实输出 supports_vision=false。
	glm := byID["glm-5.2"]
	if glm == nil {
		t.Fatalf("缺 glm-5.2: %v", byID)
	}
	if v := glm["context_length"]; v != float64(1048576) {
		t.Errorf("glm context_length = %v", v)
	}
	if v := glm["max_output_tokens"]; v != float64(131072) {
		t.Errorf("glm max_output_tokens = %v", v)
	}
	if v := glm["supports_reasoning"]; v != true {
		t.Errorf("glm supports_reasoning = %v", v)
	}
	if v := glm["supports_vision"]; v != false {
		t.Errorf("glm supports_vision = %v, want false（非视觉模型如实输出）", v)
	}
	if v, ok := glm["supports_tool_use"]; ok {
		t.Errorf("glm 不应输出 supports_tool_use, got %v", v)
	}

	// deepseek-v4.1-flash：视觉模型 → [text image]。
	ds := byID["deepseek-v4.1-flash"]
	if ds == nil {
		t.Fatalf("缺 deepseek-v4.1-flash: %v", byID)
	}
	if v := ds["supports_vision"]; v != true {
		t.Errorf("ds supports_vision = %v", v)
	}
	if v := ds["max_output_tokens"]; v != float64(384000) {
		t.Errorf("ds max_output_tokens = %v", v)
	}

	// 虚拟模型：手动配置的上下文 + 能力声明。
	auto := byID["deepseek-auto"]
	if auto == nil {
		t.Fatalf("缺虚拟模型 deepseek-auto: %v", byID)
	}
	if v := auto["context_length"]; v != float64(1048576) {
		t.Errorf("auto context_length = %v", v)
	}
	if v := auto["supports_vision"]; v != true {
		t.Errorf("auto supports_vision = %v", v)
	}
	if _, ok := auto["max_output_tokens"]; ok {
		t.Error("auto 未配置最大输出，不应输出该字段")
	}
}
