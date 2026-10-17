package requestcapture

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// newTestService 构造带临时目录的服务。
func newTestService(t *testing.T) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	return NewService(st, slog.Default(), filepath.Join(dir, "captures")), dir
}

// writeCaptureRoute 写一条命中 step-* 的 request_capture proxy 路由。
func writeCaptureRoute(t *testing.T, s *Service) {
	t.Helper()
	if s.repo != nil {
		return // repo 模式走 DB，测试里用 store
	}
	if err := s.st.Write(types.FileCapabilityRoutes, []types.CapabilityRoute{
		{Capability: capabilityName, Route: types.RouteProxy, Models: []string{"step-*"}},
	}); err != nil {
		t.Fatalf("写能力路由失败: %v", err)
	}
}

// TestCaptureWritesRawBody 命中路由：完整原始 body（含 base64）被打包落盘，内容字节级保真。
func TestCaptureWritesRawBody(t *testing.T) {
	s, _ := newTestService(t)
	writeCaptureRoute(t, s)

	rawBody := `{"model":"step-5-preview","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgo="}}]}]}`
	pipe := &modelgateway.ProxyPipeline{
		RequestID: "cap-test-001",
		Request: &modelgateway.ProxyRequest{
			Method: "POST",
			Path:   "chat/completions",
			Model:  "step-5-preview",
			Body:   []byte(rawBody),
		},
	}
	if _, err := s.HandleProxyBeforeUpstream(pipe); err != nil {
		t.Fatalf("hook 返回错误: %v", err)
	}

	path := filepath.Join(s.dir, "cap-test-001.cap.json.gz")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("捕获文件不存在: %v", err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("gzip 打开失败: %v", err)
	}
	raw, _ := io.ReadAll(gz)
	gz.Close()

	var env captureEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("解析 envelope 失败: %v", err)
	}
	if env.Body != rawBody {
		t.Fatalf("body 不保真:\n got: %s\nwant: %s", env.Body, rawBody)
	}
	if env.RequestID != "cap-test-001" || env.Method != "POST" || env.Path != "chat/completions" {
		t.Fatalf("元数据不完整: %+v", env)
	}
}

// TestCaptureMissModel 未命中路由：不落盘、不报错。
func TestCaptureMissModel(t *testing.T) {
	s, _ := newTestService(t)
	writeCaptureRoute(t, s)

	pipe := &modelgateway.ProxyPipeline{
		RequestID: "cap-miss",
		Request:   &modelgateway.ProxyRequest{Method: "POST", Path: "chat/completions", Model: "glm-5", Body: []byte("{}")},
	}
	if _, err := s.HandleProxyBeforeUpstream(pipe); err != nil {
		t.Fatalf("hook 返回错误: %v", err)
	}
	entries, _ := os.ReadDir(s.dir)
	if len(entries) != 0 {
		t.Fatalf("未命中路由不应落盘，实际 %d 个文件", len(entries))
	}
}

// TestCaptureNeverErrors 坏路由表/空 request_id：hook 绝不返回 error（fail-open 铁律）。
func TestCaptureNeverErrors(t *testing.T) {
	s, dir := newTestService(t)
	s.dir = filepath.Join(dir, "captures")
	pipe := &modelgateway.ProxyPipeline{
		Request: &modelgateway.ProxyRequest{Method: "POST", Path: "chat/completions", Model: "step-5", Body: []byte("{}")},
	}
	if _, err := s.HandleProxyBeforeUpstream(pipe); err != nil {
		t.Fatalf("无 request_id 也不该报错，实际: %v", err)
	}
	_ = io.Discard
}

