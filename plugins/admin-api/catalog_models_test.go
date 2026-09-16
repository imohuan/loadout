package adminapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	unifyai "loadout/plugins/unifyai"
)

// TestCatalogModelsRoute 回归：前端「加载配置」下拉调 GET /api/unifyai/catalog-models，
// 该路由此前未注册，请求被 SPA 兜底吞掉，前端只能拿到空列表并提示「更新元数据」。
// 本测试验证路由真实注册且返回后端缓存的模型清单。
func TestCatalogModelsRoute(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "openrouter-models.json")
	content := `[
		{"id":"deepseek/deepseek-v4.1-flash","name":"DeepSeek V4.1 Flash","context":1048576,"output":384000,"vision":true,"reasoning":true},
		{"id":"openai/gpt-4o","name":"GPT-4o","context":128000,"output":16384,"vision":true,"reasoning":false}
	]`
	if err := os.WriteFile(cache, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	oldPath := unifyai.MetadataCachePath
	unifyai.SetMetadataCachePath(func() string { return cache })
	defer unifyai.SetMetadataCachePath(oldPath)

	ts, _, pw := newTestServer(t)
	cookie := login(t, ts, pw)

	resp, data := apiReq(t, ts, http.MethodGet, "/api/unifyai/catalog-models", nil, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET catalog-models 期望 200，实际 %d: %s", resp.StatusCode, data)
	}
	var out struct {
		Models []unifyai.CatalogModel `json:"models"`
		Count  int                    `json:"count"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("解析响应（SPA 兜底会返回 HTML 导致这里失败）: %v (%s)", err, data)
	}
	if out.Count != 2 || len(out.Models) != 2 {
		t.Fatalf("应返回 2 个模型，实际 count=%d models=%+v", out.Count, out.Models)
	}
	if out.Models[0].ID != "deepseek/deepseek-v4.1-flash" || out.Models[0].Context != 1048576 {
		t.Fatalf("首个模型字段不符: %+v", out.Models[0])
	}

	// 无会话 → 401（走 session 中间件，不能公开）。
	respNoAuth, _ := apiReq(t, ts, http.MethodGet, "/api/unifyai/catalog-models", nil, nil)
	if respNoAuth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无会话期望 401，实际 %d", respNoAuth.StatusCode)
	}
}
