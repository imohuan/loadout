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

// newTestServiceWithRoutesAndChannels 构造带 SQLite 能力路由 + 渠道表 + cacheDir 的 Service。
// 复刻用户真实场景：workbuddy 渠道组（copilot.tencent.com/v2）的透传路由 + 全渠道附加代理路由。
func newTestServiceWithRoutesAndChannels(t *testing.T, routes []types.CapabilityRoute, channels []db.Channel) *Service {
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
	if err := repo.ReplaceChannels(context.Background(), channels); err != nil {
		t.Fatalf("ReplaceChannels 报错: %v", err)
	}
	svc := NewService(nil, repo, slog.Default())
	svc.cacheDir = t.TempDir()
	return svc
}

// TestWorkbuddyNativePassthroughWithHint 复刻用户问题：workbuddy 渠道组配了原生透传（pos=0，
// channel_base_urls 渠道级）。请求 hy3 经模型路由落到 workbuddy 渠道（无 v2 前缀），
// vision_v2 在 ProxyBeforeAttempt 触发，此时 __current_channel/__current_channel_base_url
// 已写入（渠道确定）。
// 修复前：vision_v2 挂 ProxyBeforeUpstream，入口阶段渠道上下文为空 → pos=0 渠道级约束
// 匹配不到，被 pos=4 全渠道代理抢走，body 被改写。
// 修复后：BeforeAttempt 阶段渠道已定，pos=0 native 命中，body 原样透传。
func TestWorkbuddyNativePassthroughWithHint(t *testing.T) {
	routes := []types.CapabilityRoute{
		{
			Models:          []string{"*"},
			ChannelBaseURLs: []string{"https://copilot.tencent.com/v2"},
			Capability:      capabilityName,
			Route:           types.RouteNative,
		},
		{
			Models:     []string{"deepseek-*", "hy*", "glm-*"},
			Capability: capabilityName,
			Route:      types.RouteProxy,
			ViaOptions: []types.ViaOption{{ViaModel: "doubao-seed-2-0-mini-260428"}},
		},
	}
	channels := []db.Channel{
		{ID: "df3f297543aebb94", Name: "15122841305", ChannelName: "workbuddy", BaseURL: "https://copilot.tencent.com/v2", ManualEnabled: true},
		{ID: "574571079f34a8db", Name: "17341174874", ChannelName: "workbuddy", BaseURL: "https://copilot.tencent.com/v2", ManualEnabled: true},
	}
	svc := newTestServiceWithRoutesAndChannels(t, routes, channels)
	body := `{"model":"hy3","messages":[{"role":"user","content":[` +
		`{"type":"text","text":"hi"},` +
		`{"type":"image_url","image_url":{"url":"` + tinyPNGDataURI + `"}}]}]}`
	pipe := proxyPipe("chat/completions", "hy3", body)
	// 模拟 ProxyBeforeAttempt 阶段：渠道已由模型路由确定（workbuddy key + base_url）
	pipe.Metadata["__current_channel"] = "df3f297543aebb94"
	pipe.Metadata["__current_channel_base_url"] = "https://copilot.tencent.com/v2"

	out, err := svc.HandleProxyBeforeUpstream(pipe)
	if err != nil {
		t.Fatalf("HandleProxyBeforeUpstream 报错: %v", err)
	}
	rp, ok := out.(*modelgateway.ProxyPipeline)
	if !ok {
		t.Fatalf("返回类型 = %T, want *ProxyPipeline", out)
	}
	if string(rp.Request.Body) != body {
		t.Fatalf("workbuddy 原生透传 body 被改写:\n got: %s\nwant: %s", rp.Request.Body, body)
	}
	if strings.Contains(string(rp.Request.Body), "<vision_img_") {
		t.Fatalf("透传 body 含占位符: %s", rp.Request.Body)
	}
	if v, _ := rp.Metadata["__vision_v2_active"].(bool); v {
		t.Fatal("透传请求不应标记 __vision_v2_active")
	}
}

// TestWorkbuddyPassthroughWithHintV2Prefix 同场景的 v2 前缀变体：model=workbuddy/hy3 时
// 入口阶段 __channel_hint 已写（hint 兜底路径）。注意：生产 BeforeAttempt 阶段
// __current_channel 恒已写入（model-gateway/proxy.go:289），hint 兜底只在「无渠道」分支
// （proxy.go:507 聚合 model 为空路径）生效——本测试覆盖的是该防御兜底分支。
func TestWorkbuddyPassthroughWithHintV2Prefix(t *testing.T) {
	routes := []types.CapabilityRoute{
		{
			Models:          []string{"*"},
			ChannelBaseURLs: []string{"https://copilot.tencent.com/v2"},
			Capability:      capabilityName,
			Route:           types.RouteNative,
		},
		{
			Models:     []string{"deepseek-*", "hy*", "glm-*"},
			Capability: capabilityName,
			Route:      types.RouteProxy,
			ViaOptions: []types.ViaOption{{ViaModel: "doubao-seed-2-0-mini-260428"}},
		},
	}
	channels := []db.Channel{
		{ID: "df3f297543aebb94", Name: "15122841305", ChannelName: "workbuddy", BaseURL: "https://copilot.tencent.com/v2", ManualEnabled: true},
	}
	svc := newTestServiceWithRoutesAndChannels(t, routes, channels)
	body := `{"model":"hy3","messages":[{"role":"user","content":[` +
		`{"type":"image_url","image_url":{"url":"` + tinyPNGDataURI + `"}}]}]}`
	pipe := proxyPipe("chat/completions", "hy3", body)
	pipe.Metadata["__channel_hint"] = "workbuddy"

	out, err := svc.HandleProxyBeforeUpstream(pipe)
	if err != nil {
		t.Fatalf("HandleProxyBeforeUpstream 报错: %v", err)
	}
	rp := out.(*modelgateway.ProxyPipeline)
	if string(rp.Request.Body) != body {
		t.Fatalf("v2 前缀 workbuddy 透传 body 被改写:\n got: %s\nwant: %s", rp.Request.Body, body)
	}
}

// TestWorkbuddyHintNoMatchOtherChannel 反向场景：hint 是别的渠道（非 workbuddy）时，
// 透传路由（copilot base_url）不命中，应落回全渠道代理（body 被改写）。
func TestWorkbuddyHintNoMatchOtherChannel(t *testing.T) {
	routes := []types.CapabilityRoute{
		{
			Models:          []string{"*"},
			ChannelBaseURLs: []string{"https://copilot.tencent.com/v2"},
			Capability:      capabilityName,
			Route:           types.RouteNative,
		},
		{
			Models:     []string{"hy*"},
			Capability: capabilityName,
			Route:      types.RouteProxy,
			ViaOptions: []types.ViaOption{{ViaModel: "qwen-vl-max"}},
		},
	}
	channels := []db.Channel{
		{ID: "ch-other", Name: "key1", ChannelName: "newapi", BaseURL: "https://newapi.example/v1", ManualEnabled: true},
	}
	svc := newTestServiceWithRoutesAndChannels(t, routes, channels)
	body := `{"model":"hy3","messages":[{"role":"user","content":[` +
		`{"type":"image_url","image_url":{"url":"` + tinyPNGDataURI + `"}}]}]}`
	pipe := proxyPipe("chat/completions", "hy3", body)
	pipe.Metadata["__channel_hint"] = "newapi"

	out, err := svc.HandleProxyBeforeUpstream(pipe)
	if err != nil {
		t.Fatalf("HandleProxyBeforeUpstream 报错: %v", err)
	}
	rp := out.(*modelgateway.ProxyPipeline)
	if !strings.Contains(string(rp.Request.Body), "vision_img_") {
		t.Fatalf("非 workbuddy 渠道应走附加代理（含占位符），实际: %s", rp.Request.Body)
	}
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


// TestDecideRouteScopeVirtualModel 验证虚拟模型（聚合）请求：路由配虚拟前缀 `git-*`，
// 真实模型 gpt-4o 不匹配、但 virtualModel 命中时仍命中；virtualModel 为空时不命中。
func TestDecideRouteScopeVirtualModel(t *testing.T) {
	svc := newTestServiceWithRepo(t, []types.CapabilityRoute{{
		Models:     []string{"git-*"},
		Capability: capabilityName,
		Route:      types.RouteProxy,
		ViaOptions: []types.ViaOption{{ViaModel: "qwen-vl-max"}},
	}})
	scope := types.ChannelRequestScope{}
	// 真实模型 gpt-4o 不匹配 git-*，virtualModel 为空 -> 不命中。
	if r, _ := svc.DecideRouteScope("gpt-4o", "", scope); r != nil {
		t.Fatalf("virtualModel 为空不应命中: %+v", r)
	}
	// virtualModel=git-xxx 命中。
	r, err := svc.DecideRouteScope("gpt-4o", "git-xxx", scope)
	if err != nil || r == nil {
		t.Fatalf("虚拟模型应命中: %v %v", r, err)
	}
	if r.Route != types.RouteProxy {
		t.Fatalf("应命中 proxy 路由: %+v", r)
	}
}
