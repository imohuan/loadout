package visionv2

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"loadout/core/db"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// newTestServiceWithRepo 构造带 SQLite 能力路由 + cacheDir 指向 TempDir 的 Service。
func newTestServiceWithRepo(t *testing.T, routes []types.CapabilityRoute) *Service {
	t.Helper()
	ds, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory 报错: %v", err)
	}
	repo, err := db.NewRepository(ds)
	if err != nil {
		t.Fatalf("NewRepository 报错: %v", err)
	}
	if err := repo.ReplaceCapabilityRoutes(context.Background(), routes); err != nil {
		t.Fatalf("ReplaceCapabilityRoutes 报错: %v", err)
	}
	svc := NewService(nil, repo, slog.Default())
	svc.cacheDir = t.TempDir()
	return svc
}

// proxyPipe 构造一条测试代理管线。
func proxyPipe(path, model, body string) *modelgateway.ProxyPipeline {
	return &modelgateway.ProxyPipeline{
		Request: &modelgateway.ProxyRequest{
			Path:  path,
			Model: model,
			Body:  []byte(body),
		},
		Metadata: map[string]any{},
	}
}

// proxyRoute 构造一条全渠道命中的视觉能力路由。
func proxyRoute(models ...string) types.CapabilityRoute {
	return types.CapabilityRoute{
		Models:     models,
		Capability: capabilityName,
		Route:      types.RouteProxy,
		ViaOptions: []types.ViaOption{{ViaModel: "qwen-vl-max"}},
	}
}

func TestRewriteChat(t *testing.T) {
	svc := newTestServiceWithRepo(t, []types.CapabilityRoute{proxyRoute("deepseek-chat")})
	body := `{"model":"deepseek-chat","messages":[{"role":"user","content":[` +
		`{"type":"text","text":"hi"},` +
		`{"type":"image_url","image_url":{"url":"` + tinyPNGDataURI + `"}}]}]}`
	pipe := proxyPipe("chat/completions", "deepseek-chat", body)

	out, err := svc.HandleProxyBeforeUpstream(pipe)
	if err != nil {
		t.Fatalf("HandleProxyBeforeUpstream 报错: %v", err)
	}
	rp, ok := out.(*modelgateway.ProxyPipeline)
	if !ok {
		t.Fatalf("返回类型 = %T, want *ProxyPipeline", out)
	}
	got := string(rp.Request.Body)
	if !strings.Contains(got, "vision_img_") {
		t.Fatalf("body 不含占位符: %s", got)
	}
	if strings.Contains(got, "image_url") {
		t.Fatalf("body 仍含 image_url: %s", got)
	}
	if v, _ := rp.Metadata["__vision_v2_active"].(bool); !v {
		t.Fatal("metadata 未标记 __vision_v2_active")
	}
	if v, _ := rp.Metadata["__vision_v2_format"].(string); v != "chat" {
		t.Fatalf("metadata __vision_v2_format = %q, want chat", v)
	}
	entries, err := os.ReadDir(svc.imageFilesDir())
	if err != nil {
		t.Fatalf("读取 files/ 目录报错: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("files/ 下没有落盘文件")
	}
}

func TestRewriteClaude(t *testing.T) {
	svc := newTestServiceWithRepo(t, []types.CapabilityRoute{proxyRoute("claude-sonnet-4")})
	// tinyPNGDataURI 去掉 data: 前缀与 meta 后就是 base64 payload。
	base64png := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	body := `{"model":"claude-sonnet-4","messages":[{"role":"user","content":[` +
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + base64png + `"}}]}]}`
	pipe := proxyPipe("messages", "claude-sonnet-4", body)

	out, err := svc.HandleProxyBeforeUpstream(pipe)
	if err != nil {
		t.Fatalf("HandleProxyBeforeUpstream 报错: %v", err)
	}
	got := string(out.(*modelgateway.ProxyPipeline).Request.Body)
	if !strings.Contains(got, "vision_img_") {
		t.Fatalf("body 不含占位符: %s", got)
	}
	if strings.Contains(got, `"type":"image"`) {
		t.Fatalf("body 仍含 type=image 块: %s", got)
	}
}

func TestRewriteResponses(t *testing.T) {
	svc := newTestServiceWithRepo(t, []types.CapabilityRoute{proxyRoute("gpt-5")})
	body := `{"model":"gpt-5","input":[{"type":"message","role":"user","content":[` +
		`{"type":"input_text","text":"hi"},` +
		`{"type":"input_image","image_url":"` + tinyPNGDataURI + `"}]}]}`
	pipe := proxyPipe("responses", "gpt-5", body)

	out, err := svc.HandleProxyBeforeUpstream(pipe)
	if err != nil {
		t.Fatalf("HandleProxyBeforeUpstream 报错: %v", err)
	}
	got := string(out.(*modelgateway.ProxyPipeline).Request.Body)
	if !strings.Contains(got, "vision_img_") {
		t.Fatalf("body 不含占位符: %s", got)
	}
	if strings.Contains(got, "input_image") {
		t.Fatalf("body 仍含 input_image: %s", got)
	}
}

func TestNativePassthrough(t *testing.T) {
	svc := newTestServiceWithRepo(t, []types.CapabilityRoute{{
		Models:     []string{"deepseek-chat"},
		Capability: capabilityName,
		Route:      types.RouteNative,
	}})
	body := `{"model":"deepseek-chat","messages":[{"role":"user","content":[` +
		`{"type":"image_url","image_url":{"url":"` + tinyPNGDataURI + `"}}]}]}`
	pipe := proxyPipe("chat/completions", "deepseek-chat", body)

	out, err := svc.HandleProxyBeforeUpstream(pipe)
	if err != nil {
		t.Fatalf("HandleProxyBeforeUpstream 报错: %v", err)
	}
	rp := out.(*modelgateway.ProxyPipeline)
	if string(rp.Request.Body) != body {
		t.Fatalf("native 路由 body 被改写:\n got: %s\nwant: %s", rp.Request.Body, body)
	}
	if strings.Contains(string(rp.Request.Body), "<vision_img_") {
		t.Fatalf("native 路由 body 含占位符: %s", rp.Request.Body)
	}
}

func TestNonVisionPath(t *testing.T) {
	svc := newTestServiceWithRepo(t, []types.CapabilityRoute{proxyRoute("deepseek-chat")})
	body := `{"model":"deepseek-chat","messages":[{"role":"user","content":[` +
		`{"type":"image_url","image_url":{"url":"` + tinyPNGDataURI + `"}}]}]}`
	pipe := proxyPipe("other/path", "deepseek-chat", body)

	out, err := svc.HandleProxyBeforeUpstream(pipe)
	if err != nil {
		t.Fatalf("HandleProxyBeforeUpstream 报错: %v", err)
	}
	rp := out.(*modelgateway.ProxyPipeline)
	if string(rp.Request.Body) != body {
		t.Fatalf("非视觉路径 body 被改写:\n got: %s\nwant: %s", rp.Request.Body, body)
	}
}

func TestURLImageFallback(t *testing.T) {
	// URL 图下载失败（500）→ 该块保留原样；data URI 图仍被替换。
	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer errSrv.Close()

	svc := newTestServiceWithRepo(t, []types.CapabilityRoute{proxyRoute("deepseek-chat")})
	body := `{"model":"deepseek-chat","messages":[{"role":"user","content":[` +
		`{"type":"image_url","image_url":{"url":"` + errSrv.URL + `/a.png"}},` +
		`{"type":"image_url","image_url":{"url":"` + tinyPNGDataURI + `"}}]}]}`
	pipe := proxyPipe("chat/completions", "deepseek-chat", body)

	out, err := svc.HandleProxyBeforeUpstream(pipe)
	if err != nil {
		t.Fatalf("HandleProxyBeforeUpstream 报错: %v", err)
	}
	got := string(out.(*modelgateway.ProxyPipeline).Request.Body)
	if !strings.Contains(got, "vision_img_") {
		t.Fatalf("body 不含占位符（data URI 图未替换）: %s", got)
	}
	if !strings.Contains(got, "image_url") {
		t.Fatalf("URL 图应保留原样但被移除: %s", got)
	}
}
