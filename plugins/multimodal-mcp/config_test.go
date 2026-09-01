package multimodalmcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"loadout/core/store"
)

// newHandlerService 构造带真实 store 的 Service（复用 newConfigService 的临时目录逻辑）。
func newHandlerService(t *testing.T, cfg *MultimodalConfig) *Service {
	t.Helper()
	return newConfigService(t, cfg)
}

// TestHandlerConfigGetDefault 验证：GET /api/multimodal/config 在未保存配置时返回默认配置。
func TestHandlerConfigGetDefault(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	s := &Service{st: st}
	h := s.HandlerConfig()

	req := httptest.NewRequest(http.MethodGet, "/api/multimodal/config", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", rec.Code)
	}
	var got MultimodalConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if got.Enabled {
		t.Error("默认配置端点应关闭")
	}
	if len(got.Tools) != 4 {
		t.Errorf("默认工具数 = %d, want 4", len(got.Tools))
	}
}

// TestHandlerConfigPutThenGet 验证：PUT 保存配置后 GET 能读回。
func TestHandlerConfigPutThenGet(t *testing.T) {
	s := newHandlerService(t, DefaultConfig())
	h := s.HandlerConfig()

	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Tools[0].Model = "doubao-seed-2-1-pro-260628"
	body, _ := json.Marshal(cfg)

	req := httptest.NewRequest(http.MethodPut, "/api/multimodal/config", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", rec.Code)
	}

	// GET 读回
	getReq := httptest.NewRequest(http.MethodGet, "/api/multimodal/config", nil)
	getRec := httptest.NewRecorder()
	h.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", getRec.Code)
	}
	var got MultimodalConfig
	if err := json.Unmarshal(getRec.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if !got.Enabled {
		t.Error("PUT 保存后 enabled 应被读回为 true")
	}
	if got.Tools[0].Model != "doubao-seed-2-1-pro-260628" {
		t.Errorf("读回的模型 = %q, want doubao-seed-2-1-pro-260628", got.Tools[0].Model)
	}
}

// TestHandlerConfigPutBadJSON 验证：PUT 非法 JSON 返回 400。
func TestHandlerConfigPutBadJSON(t *testing.T) {
	s := newHandlerService(t, DefaultConfig())
	h := s.HandlerConfig()

	req := httptest.NewRequest(http.MethodPut, "/api/multimodal/config", bytes.NewReader([]byte("{not-json")))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT 非法 JSON status = %d, want 400", rec.Code)
	}
}
