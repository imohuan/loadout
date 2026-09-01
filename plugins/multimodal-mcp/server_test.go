package multimodalmcp

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExactRouteBeatsPrefix(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("/mcp/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(201) // 前缀兜底
	}))
	mux.Handle("POST /mcp/multimodal", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(202) // 精确路由应命中
	}))
	req := httptest.NewRequest(http.MethodPost, "/mcp/multimodal", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 202 {
		t.Fatalf("exact /mcp/multimodal should win over /mcp/ prefix, got %d", rec.Code)
	}
	// 其他 /mcp/ 子路径仍走前缀
	req2 := httptest.NewRequest(http.MethodPost, "/mcp/github", nil)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != 201 {
		t.Fatalf("/mcp/github should fall to prefix, got %d", rec2.Code)
	}
}
