package adminapi

// 回归：模型测试把上游的 401 原样透传成管理后台的 401，前端 `api.ts` 只认状态码，
// 于是「某个 Key 失效」「批量测 Key」被当成「你的登录过期」，弹登录页。
// 刷新页面又能进，正是因为会话其实一直是好的。
//
// 契约：/api/test/chat 只有在会话真的无效时才返回 401；上游凭据错误属于上游失败，
// 一律用 502 回带原始错误体（X-Test-Log 的 http_status 仍记录上游真实状态码）。

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestModelTestUpstreamUnauthorizedKeepsSession(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "<html><head><title>401 Authorization Required</title></head></html>")
	}))
	defer upstream.Close()

	ts, _, pw := newRouteLogTestServer(t)
	cookie := login(t, ts, pw)

	resp, data := apiReq(t, ts, http.MethodPost, "/api/test/chat", map[string]any{
		"base_url": upstream.URL + "/v2", "model": "m", "stream": true,
		"messages": []map[string]any{{"role": "user", "content": "ping"}},
	}, cookie)
	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatalf("上游 401 不应透传成管理接口 401（前端会误判为登录过期），body=%s", data)
	}
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("上游 401 期望 502，实际 %d: %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "401 Authorization Required") {
		t.Fatalf("上游原文应原样带回给前端展示，实际: %s", data)
	}
	// 访问摘要仍要记录上游真实状态码，模型状态页靠它显示「auth」类失败。
	summary := extractHeaderSummary(t, resp)
	if summary.HTTPStatus != http.StatusUnauthorized || summary.Result != "failed" {
		t.Fatalf("摘要应记录上游 401 与 failed，实际: %+v", summary)
	}

	// 会话本身必须完好：紧接着的管理接口仍应放行。
	if ov, _ := apiReq(t, ts, http.MethodGet, "/api/overview", nil, cookie); ov.StatusCode != http.StatusOK {
		t.Fatalf("上游 401 不应影响会话，/api/overview 实际 %d", ov.StatusCode)
	}
}

// TestModelTestUpstreamForbiddenPassthrough 上游 403 与登录态无关，保持原样透传。
func TestModelTestUpstreamForbiddenPassthrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"message":"no permission"}}`)
	}))
	defer upstream.Close()

	ts, _, pw := newRouteLogTestServer(t)
	cookie := login(t, ts, pw)
	resp, data := apiReq(t, ts, http.MethodPost, "/api/test/chat", map[string]any{
		"base_url": upstream.URL + "/v1", "model": "m",
		"messages": []map[string]any{{"role": "user", "content": "ping"}},
	}, cookie)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("上游 403 期望透传 403，实际 %d: %s", resp.StatusCode, data)
	}
}
