package requestcapture

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	modelgateway "loadout/plugins/model-gateway"
)

// captureOnce 命中路由后抓一次，返回 request_id。
func captureOnce(t *testing.T, s *Service, requestID string, body []byte) {
	t.Helper()
	pipe := &modelgateway.ProxyPipeline{
		RequestID: requestID,
		Request: &modelgateway.ProxyRequest{
			Method: "POST",
			Path:   "chat/completions",
			Model:  "step-5-preview",
			Stream: true,
			Header: http.Header{
				"Authorization": []string{"Bearer sk-raw-key-should-be-kept"},
				"Content-Type":  []string{"application/json"},
			},
			Body: body,
		},
	}
	if _, err := s.HandleProxyBeforeUpstream(pipe); err != nil {
		t.Fatalf("hook 返回错误: %v", err)
	}
}

// TestListDetailDelete 列表能看到抓包行，详情保留 Authorization 原文与完整 body，删除后消失。
func TestListDetailDelete(t *testing.T) {
	s, _ := newTestService(t)
	writeCaptureRoute(t, s)

	body := []byte(`{"model":"step-5-preview","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgoAAAANSUhEUg=="}}]}]}`)
	captureOnce(t, s, "cap-list-1", body)

	page := s.List(CaptureFilter{}, 1, 20)
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("列表应有 1 条，实际 total=%d len=%d", page.Total, len(page.Items))
	}
	item := page.Items[0]
	if item.RequestID != "cap-list-1" || item.Model != "step-5-preview" {
		t.Fatalf("列表元数据不对: %+v", item)
	}
	if item.Size <= 0 || item.CapturedAt == "" {
		t.Fatalf("列表缺少大小/时间: %+v", item)
	}

	detail, err := s.Detail("cap-list-1")
	if err != nil {
		t.Fatalf("详情失败: %v", err)
	}
	if detail.Body != string(body) {
		t.Fatalf("body 不保真:\n got: %s\nwant: %s", detail.Body, body)
	}
	if got := detail.Headers["Authorization"]; len(got) != 1 || got[0] != "Bearer sk-raw-key-should-be-kept" {
		t.Fatalf("Authorization 应保留原文，实际: %v", got)
	}

	// 搜索：命中 / 未命中
	if s.List(CaptureFilter{RequestID: "cap-list"}, 1, 20).Total != 1 {
		t.Fatal("request_id 搜索应命中")
	}
	if s.List(CaptureFilter{Model: "step-5"}, 1, 20).Total != 1 {
		t.Fatal("model 搜索应命中")
	}
	if s.List(CaptureFilter{Model: "glm"}, 1, 20).Total != 0 {
		t.Fatal("model 搜索不应命中")
	}

	if !s.Delete("cap-list-1") {
		t.Fatal("删除应成功")
	}
	if s.List(CaptureFilter{}, 1, 20).Total != 0 {
		t.Fatal("删除后列表应为空")
	}
	if _, err := os.Stat(filepath.Join(s.dir, "cap-list-1"+captureExt)); !os.IsNotExist(err) {
		t.Fatal("删除后文件应不存在")
	}
}

// TestListPagination 分页边界：总数与当前页条数正确。
func TestListPagination(t *testing.T) {
	s, _ := newTestService(t)
	writeCaptureRoute(t, s)
	for _, id := range []string{"cap-p-1", "cap-p-2", "cap-p-3"} {
		captureOnce(t, s, id, []byte(`{"model":"step-5-preview"}`))
	}
	page := s.List(CaptureFilter{}, 2, 2)
	if page.Total != 3 {
		t.Fatalf("总数应为 3，实际 %d", page.Total)
	}
	if len(page.Items) != 1 {
		t.Fatalf("第 2 页应有 1 条，实际 %d", len(page.Items))
	}
}

// TestKeepBase64 抓包不脱敏：图片 base64 原样保留（与 request-log 的差异点）。
func TestKeepBase64(t *testing.T) {
	s, _ := newTestService(t)
	writeCaptureRoute(t, s)
	payload := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJ"
	captureOnce(t, s, "cap-b64", []byte(`{"input":"`+payload+`"}`))

	detail, err := s.Detail("cap-b64")
	if err != nil {
		t.Fatalf("详情失败: %v", err)
	}
	if !contains(detail.Body, payload) {
		t.Fatalf("base64 应完整保留，实际: %s", detail.Body)
	}
}

// TestSubRequestSkipped 子请求不抓包（避免把插件内部的识别请求写进调试目录）。
func TestSubRequestSkipped(t *testing.T) {
	s, _ := newTestService(t)
	writeCaptureRoute(t, s)
	pipe := &modelgateway.ProxyPipeline{
		RequestID: "cap-sub",
		Request: &modelgateway.ProxyRequest{
			Method: "POST", Path: "chat/completions", Model: "step-5-preview", Body: []byte("{}"),
		},
		Metadata: map[string]any{"__sub_request": true},
	}
	if _, err := s.HandleProxyBeforeUpstream(pipe); err != nil {
		t.Fatalf("hook 不应报错: %v", err)
	}
	if entries, _ := os.ReadDir(s.dir); len(entries) != 0 {
		t.Fatalf("子请求不应落盘，实际 %d 个文件", len(entries))
	}
}

// TestRetentionDeletesOldest 保留策略按容量从最旧开始删。
func TestRetentionDeletesOldest(t *testing.T) {
	s, _ := newTestService(t)
	writeCaptureRoute(t, s)
	for _, id := range []string{"cap-r-1", "cap-r-2", "cap-r-3"} {
		captureOnce(t, s, id, []byte(`{"model":"step-5-preview"}`))
	}
	// 人为把抓包时间拉开，确保顺序稳定。
	metas := []struct {
		id string
		at string
	}{
		{"cap-r-1", "2020-01-01T00:00:00Z"},
		{"cap-r-2", "2020-01-02T00:00:00Z"},
		{"cap-r-3", "2020-01-03T00:00:00Z"},
	}
	for _, m := range metas {
		if err := os.WriteFile(filepath.Join(s.dir, m.id+metaExt),
			[]byte(`{"request_id":"`+m.id+`","model":"step-5-preview","captured_at":"`+m.at+`","size":1}`), 0o644); err != nil {
			t.Fatalf("写 meta 失败: %v", err)
		}
	}
	s.SetRetention(1, 0) // 只保留 1 天：三条 2020 年的都过期
	if n := s.ApplyRetention(); n != 3 {
		t.Fatalf("应清理 3 条，实际 %d", n)
	}
	if s.List(CaptureFilter{}, 1, 20).Total != 0 {
		t.Fatal("清理后列表应为空")
	}
}

// TestClearRemovesAll 清空删除全部捕获与 meta。
func TestClearRemovesAll(t *testing.T) {
	s, _ := newTestService(t)
	writeCaptureRoute(t, s)
	for _, id := range []string{"cap-c-1", "cap-c-2"} {
		captureOnce(t, s, id, []byte("{}"))
	}
	if n := s.Clear(); n != 2 {
		t.Fatalf("应清空 2 条，实际 %d", n)
	}
	entries, _ := os.ReadDir(s.dir)
	if len(entries) != 0 {
		t.Fatalf("清空后目录应为空，实际剩余 %d", len(entries))
	}
}

// TestHandleListHTTP 列表 HTTP 接口返回 JSON 分页结构。
func TestHandleListHTTP(t *testing.T) {
	s, _ := newTestService(t)
	writeCaptureRoute(t, s)
	captureOnce(t, s, "cap-http", []byte("{}"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/request-captures?page=1&pageSize=20", nil)
	s.handleList(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码应为 200，实际 %d", rec.Code)
	}
	if !contains(rec.Body.String(), `"total":1`) {
		t.Fatalf("响应缺少 total: %s", rec.Body.String())
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})())
}

