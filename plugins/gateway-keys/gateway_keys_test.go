package gatewaykeys

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"loadout/core/auth"
	"loadout/core/store"
	"loadout/plugins/types"
)

// newTestManager 用临时目录建 Store 与 Manager，供测试复用。
func newTestManager(t *testing.T) *Manager {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	return NewManager(st)
}

func TestCreateAndListAPIKey(t *testing.T) {
	m := newTestManager(t)
	full, prefix, err := m.CreateAPIKey("本机", []string{"*"})
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	if !strings.HasPrefix(full, "sk-") {
		t.Fatalf("full 应以 sk- 开头: %q", full)
	}
	if prefix != full[:6] {
		t.Errorf("prefix 应为 full 前 6 字符: %q != %q", prefix, full[:6])
	}

	keys, err := m.ListAPIKeys()
	if err != nil {
		t.Fatalf("ListAPIKeys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("应有 1 把 key，实际 %d", len(keys))
	}
	k := keys[0]
	if k.Hash != auth.HashSecretKey(full) {
		t.Errorf("落盘哈希应与完整 key 的 sha256 一致")
	}
	if k.Prefix != prefix {
		t.Errorf("落盘前缀应为 %q，实际 %q", prefix, k.Prefix)
	}
	if !k.Enabled {
		t.Errorf("新建 key 应默认启用")
	}
	if strings.Contains(k.Hash, full) {
		t.Errorf("落盘记录不应包含完整 key")
	}

	// 落盘序列化后也不应包含完整 key。
	data, err := json.Marshal(keys)
	if err != nil {
		t.Fatalf("Marshal keys: %v", err)
	}
	if strings.Contains(string(data), full) {
		t.Errorf("序列化结果不应包含完整 key")
	}
}

func TestDeleteAndSetEnabled(t *testing.T) {
	m := newTestManager(t)
	if _, _, err := m.CreateAPIKey("a", []string{"*"}); err != nil {
		t.Fatalf("CreateAPIKey a: %v", err)
	}
	if _, _, err := m.CreateAPIKey("b", []string{"*"}); err != nil {
		t.Fatalf("CreateAPIKey b: %v", err)
	}

	keys, err := m.ListAPIKeys()
	if err != nil {
		t.Fatalf("ListAPIKeys: %v", err)
	}
	id1, id2 := keys[0].ID, keys[1].ID

	// 禁用生效。
	if err := m.SetAPIKeyEnabled(id1, false); err != nil {
		t.Fatalf("SetAPIKeyEnabled: %v", err)
	}
	keys, _ = m.ListAPIKeys()
	for _, k := range keys {
		if k.ID == id1 && k.Enabled {
			t.Errorf("id1 应被禁用")
		}
	}

	// 删除生效。
	if err := m.DeleteAPIKey(id1); err != nil {
		t.Fatalf("DeleteAPIKey: %v", err)
	}
	keys, _ = m.ListAPIKeys()
	if len(keys) != 1 || keys[0].ID != id2 {
		t.Errorf("删除后应只剩 id2")
	}

	// 操作不存在的 id 应报错。
	if err := m.DeleteAPIKey("nope"); err == nil {
		t.Errorf("删除不存在的 id 应返回错误")
	}
	if err := m.SetAPIKeyEnabled("nope", true); err == nil {
		t.Errorf("启用不存在的 id 应返回错误")
	}
}

func TestSkKeyMiddleware(t *testing.T) {
	m := newTestManager(t)
	full, _, err := m.CreateAPIKey("本机", []string{"*"})
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	var gotKey any
	h := m.SkKeyMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Context().Value(ctxAPIKey)
		w.WriteHeader(http.StatusOK)
	}))

	// 正确 Bearer → 200，且 context 存有命中的 key 记录。
	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+full)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("正确 key 应 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	if k, ok := gotKey.(types.APIKey); !ok || k.Hash != auth.HashSecretKey(full) {
		t.Errorf("context 应存有命中的 key 记录")
	}

	// 错误 key → 401。
	req = httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer sk-wrong")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("错误 key 应 401，实际 %d", rec.Code)
	}
	assertAuthErrorBody(t, rec)

	// 缺失 header → 401。
	req = httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("缺失 key 应 401，实际 %d", rec.Code)
	}

	// 禁用后 → 401。
	keys, _ := m.ListAPIKeys()
	if err := m.SetAPIKeyEnabled(keys[0].ID, false); err != nil {
		t.Fatalf("SetAPIKeyEnabled: %v", err)
	}
	req = httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+full)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("禁用后应 401，实际 %d", rec.Code)
	}
}

func TestMCPKeyRoundTrip(t *testing.T) {
	m := newTestManager(t)
	if m.MCPKeyEnabled("/mcp/group1") {
		t.Errorf("初始应未开启认证")
	}

	full, err := m.SetMCPKey("/mcp/group1")
	if err != nil {
		t.Fatalf("SetMCPKey: %v", err)
	}
	if full == "" {
		t.Errorf("SetMCPKey 应返回完整 key")
	}
	if !m.MCPKeyEnabled("/mcp/group1") {
		t.Errorf("设置后应开启认证")
	}

	// 重置应生成新 key。
	full2, err := m.SetMCPKey("/mcp/group1")
	if err != nil {
		t.Fatalf("重置 SetMCPKey: %v", err)
	}
	if full2 == "" || full2 == full {
		t.Errorf("重置应生成新的 key")
	}

	if err := m.DisableMCPKey("/mcp/group1"); err != nil {
		t.Fatalf("DisableMCPKey: %v", err)
	}
	if m.MCPKeyEnabled("/mcp/group1") {
		t.Errorf("关闭后应未开启认证")
	}

	// 无记录时 Disable 返回 nil。
	if err := m.DisableMCPKey("/mcp/none"); err != nil {
		t.Errorf("无记录时 Disable 应返回 nil，实际 %v", err)
	}
}

func TestMCPKeyMiddleware(t *testing.T) {
	m := newTestManager(t)
	h := m.MCPKeyMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// 无记录端点 → 放行。
	req := httptest.NewRequest(http.MethodGet, "/mcp/open", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("无记录端点应放行 200，实际 %d", rec.Code)
	}

	full, err := m.SetMCPKey("/mcp/secured")
	if err != nil {
		t.Fatalf("SetMCPKey: %v", err)
	}

	// 有记录 + 正确 header → 放行。
	req = httptest.NewRequest(http.MethodGet, "/mcp/secured", nil)
	req.Header.Set("X-Loadout-Key", full)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("正确 header 应放行 200，实际 %d: %s", rec.Code, rec.Body.String())
	}

	// 有记录 + 错误 header → 401。
	req = httptest.NewRequest(http.MethodGet, "/mcp/secured", nil)
	req.Header.Set("X-Loadout-Key", "wrong")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("错误 header 应 401，实际 %d", rec.Code)
	}

	// 有记录 + 缺失 header → 401。
	req = httptest.NewRequest(http.MethodGet, "/mcp/secured", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("缺失 header 应 401，实际 %d", rec.Code)
	}
}

// assertAuthErrorBody 校验 401 响应体的错误 JSON 结构与类型。
func assertAuthErrorBody(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	var out struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析错误响应 JSON: %v", err)
	}
	if out.Error.Type != "invalid_request_error" {
		t.Errorf("错误类型应为 invalid_request_error，实际 %q", out.Error.Type)
	}
	if out.Error.Message == "" {
		t.Errorf("错误消息不应为空")
	}
}
