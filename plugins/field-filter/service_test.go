package fieldfilter

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

func newTestService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	return NewService(st, slog.Default()), st
}

func seedRoute(t *testing.T, st *store.Store, route types.CapabilityRoute) {
	t.Helper()
	if err := st.Write(types.FileCapabilityRoutes, []types.CapabilityRoute{route}); err != nil {
		t.Fatalf("写能力路由表失败: %v", err)
	}
}

func proxyPipe(t *testing.T, body any) *modelgateway.ProxyPipeline {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return &modelgateway.ProxyPipeline{
		Request:  &modelgateway.ProxyRequest{Body: raw, Model: "gpt-4o"},
		Metadata: map[string]any{},
	}
}

func TestHandleProxyBeforeUpstreamStrip(t *testing.T) {
	svc, st := newTestService(t)
	seedRoute(t, st, types.CapabilityRoute{
		Models:     []string{"gpt-4o"},
		Capability: capabilityName,
		Route:      types.RouteProxy,
		FieldRules: &types.FieldRules{RequestStrip: []string{"client_metadata"}},
	})
	pipe := proxyPipe(t, map[string]any{
		"model":           "gpt-4o",
		"messages":        []any{},
		"client_metadata": map[string]any{"app": "codex"},
	})
	out, err := svc.HandleProxyBeforeUpstream(pipe)
	if err != nil {
		t.Fatal(err)
	}
	got := out.(*modelgateway.ProxyPipeline)
	var body map[string]any
	if err := json.Unmarshal(got.Request.Body, &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["client_metadata"]; ok {
		t.Fatalf("client_metadata 未剔除: %s", got.Request.Body)
	}
	if _, ok := body["messages"]; !ok {
		t.Fatal("messages 被误删")
	}
}

func TestHandleProxyBeforeUpstreamKeep(t *testing.T) {
	svc, st := newTestService(t)
	seedRoute(t, st, types.CapabilityRoute{
		Models:     []string{"gpt-4o"},
		Capability: capabilityName,
		Route:      types.RouteProxy,
		FieldRules: &types.FieldRules{RequestKeep: []string{"model", "messages"}},
	})
	pipe := proxyPipe(t, map[string]any{
		"model":           "gpt-4o",
		"messages":        []any{},
		"client_metadata": map[string]any{"app": "codex"},
	})
	out, err := svc.HandleProxyBeforeUpstream(pipe)
	if err != nil {
		t.Fatal(err)
	}
	got := out.(*modelgateway.ProxyPipeline)
	var body map[string]any
	if err := json.Unmarshal(got.Request.Body, &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["client_metadata"]; ok {
		t.Fatalf("client_metadata 应被白名单删除: %s", got.Request.Body)
	}
	if _, ok := body["messages"]; !ok {
		t.Fatal("messages 被误删")
	}
}

func TestHandleProxyBeforeUpstreamNoRoute(t *testing.T) {
	// 未命中路由：原样透传
	svc, _ := newTestService(t)
	pipe := proxyPipe(t, map[string]any{
		"model":           "other-model",
		"client_metadata": map[string]any{"app": "codex"},
	})
	out, err := svc.HandleProxyBeforeUpstream(pipe)
	if err != nil {
		t.Fatal(err)
	}
	got := out.(*modelgateway.ProxyPipeline)
	if string(got.Request.Body) != string(pipe.Request.Body) {
		t.Fatalf("未命中路由应原样: %s", got.Request.Body)
	}
}

func TestHandleProxyBeforeUpstreamNative(t *testing.T) {
	svc, st := newTestService(t)
	seedRoute(t, st, types.CapabilityRoute{
		Models:     []string{"gpt-4o"},
		Capability: capabilityName,
		Route:      types.RouteNative,
		FieldRules: &types.FieldRules{RequestStrip: []string{"client_metadata"}},
	})
	pipe := proxyPipe(t, map[string]any{
		"model":           "gpt-4o",
		"client_metadata": map[string]any{"app": "codex"},
	})
	out, err := svc.HandleProxyBeforeUpstream(pipe)
	if err != nil {
		t.Fatal(err)
	}
	got := out.(*modelgateway.ProxyPipeline)
	if string(got.Request.Body) != string(pipe.Request.Body) {
		t.Fatalf("native 路由应原样: %s", got.Request.Body)
	}
}

func TestHandleProxyBeforeUpstreamNilFieldRules(t *testing.T) {
	// 老配置无 field_rules（nil）：不 panic，原样透传
	svc, st := newTestService(t)
	seedRoute(t, st, types.CapabilityRoute{
		Models:     []string{"gpt-4o"},
		Capability: capabilityName,
		Route:      types.RouteProxy,
	})
	pipe := proxyPipe(t, map[string]any{
		"model":           "gpt-4o",
		"client_metadata": map[string]any{"app": "codex"},
	})
	out, err := svc.HandleProxyBeforeUpstream(pipe)
	if err != nil {
		t.Fatal(err)
	}
	got := out.(*modelgateway.ProxyPipeline)
	if string(got.Request.Body) != string(pipe.Request.Body) {
		t.Fatalf("FieldRules=nil 应原样: %s", got.Request.Body)
	}
}

func TestHandleProxyBeforeUpstreamNonJSON(t *testing.T) {
	svc, st := newTestService(t)
	seedRoute(t, st, types.CapabilityRoute{
		Models:     []string{"gpt-4o"},
		Capability: capabilityName,
		Route:      types.RouteProxy,
		FieldRules: &types.FieldRules{RequestStrip: []string{"client_metadata"}},
	})
	pipe := &modelgateway.ProxyPipeline{
		Request:  &modelgateway.ProxyRequest{Body: []byte(`not json`), Model: "gpt-4o"},
		Metadata: map[string]any{},
	}
	out, err := svc.HandleProxyBeforeUpstream(pipe)
	if err != nil {
		t.Fatal(err)
	}
	got := out.(*modelgateway.ProxyPipeline)
	if string(got.Request.Body) != "not json" {
		t.Fatalf("非 JSON 应原样: %s", got.Request.Body)
	}
}

func TestHandleProxyAfterUpstreamStrip(t *testing.T) {
	svc, st := newTestService(t)
	seedRoute(t, st, types.CapabilityRoute{
		Models:     []string{"gpt-4o"},
		Capability: capabilityName,
		Route:      types.RouteProxy,
		FieldRules: &types.FieldRules{
			ResponseStrip: []string{"usage"},
			ResponseHeaderStrip:   []string{"x-internal"}, // 小写验证大小写不敏感
		},
	})
	pipe := proxyPipe(t, map[string]any{"model": "gpt-4o"})
	after := &modelgateway.AfterUpstreamPayload{
		Pipe: pipe,
		Response: &modelgateway.ProxyResponse{
			StatusCode: 200,
			Header:     http.Header{"X-Internal": {"secret"}, "Content-Type": {"application/json"}},
			Body:       []byte(`{"choices":[{"message":{"content":"hi"}}],"usage":{"total_tokens":10}}`),
		},
	}
	out, err := svc.HandleProxyAfterUpstream(after)
	if err != nil {
		t.Fatal(err)
	}
	got := out.(*modelgateway.AfterUpstreamPayload)
	if got.Response.Header.Get("X-Internal") != "" {
		t.Fatalf("X-Internal 未剔除: %+v", got.Response.Header)
	}
	if got.Response.Header.Get("Content-Type") == "" {
		t.Fatal("Content-Type 被误删")
	}
	var body map[string]any
	if err := json.Unmarshal(got.Response.Body, &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["usage"]; ok {
		t.Fatalf("usage 未剔除: %s", got.Response.Body)
	}
	if _, ok := body["choices"]; !ok {
		t.Fatal("choices 被误删")
	}
}

func TestHandleProxyAfterUpstreamKeep(t *testing.T) {
	svc, st := newTestService(t)
	seedRoute(t, st, types.CapabilityRoute{
		Models:     []string{"gpt-4o"},
		Capability: capabilityName,
		Route:      types.RouteProxy,
		FieldRules: &types.FieldRules{ResponseKeep: []string{"choices"}},
	})
	pipe := proxyPipe(t, map[string]any{"model": "gpt-4o"})
	after := &modelgateway.AfterUpstreamPayload{
		Pipe: pipe,
		Response: &modelgateway.ProxyResponse{
			StatusCode: 200,
			Header:     http.Header{},
			Body:       []byte(`{"choices":[{"message":{"content":"hi"}}],"usage":{"total_tokens":10}}`),
		},
	}
	out, err := svc.HandleProxyAfterUpstream(after)
	if err != nil {
		t.Fatal(err)
	}
	got := out.(*modelgateway.AfterUpstreamPayload)
	var body map[string]any
	if err := json.Unmarshal(got.Response.Body, &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["choices"]; !ok {
		t.Fatal("choices 被误删")
	}
	if _, ok := body["usage"]; ok {
		t.Fatalf("usage 应被白名单删除: %s", got.Response.Body)
	}
}

func TestHandleProxyAfterUpstreamNoRoute(t *testing.T) {
	svc, _ := newTestService(t)
	pipe := proxyPipe(t, map[string]any{"model": "other-model"})
	after := &modelgateway.AfterUpstreamPayload{
		Pipe:     pipe,
		Response: &modelgateway.ProxyResponse{StatusCode: 200, Header: http.Header{}, Body: []byte(`{"usage":{"total_tokens":1}}`)},
	}
	out, err := svc.HandleProxyAfterUpstream(after)
	if err != nil {
		t.Fatal(err)
	}
	got := out.(*modelgateway.AfterUpstreamPayload)
	if string(got.Response.Body) != string(after.Response.Body) {
		t.Fatalf("未命中路由应原样: %s", got.Response.Body)
	}
}

func TestHandleProxyAfterUpstreamNilFieldRules(t *testing.T) {
	svc, st := newTestService(t)
	seedRoute(t, st, types.CapabilityRoute{
		Models:     []string{"gpt-4o"},
		Capability: capabilityName,
		Route:      types.RouteProxy,
	})
	pipe := proxyPipe(t, map[string]any{"model": "gpt-4o"})
	after := &modelgateway.AfterUpstreamPayload{
		Pipe:     pipe,
		Response: &modelgateway.ProxyResponse{StatusCode: 200, Header: http.Header{}, Body: []byte(`{"usage":{"total_tokens":1}}`)},
	}
	out, err := svc.HandleProxyAfterUpstream(after)
	if err != nil {
		t.Fatal(err)
	}
	got := out.(*modelgateway.AfterUpstreamPayload)
	if string(got.Response.Body) != string(after.Response.Body) {
		t.Fatalf("FieldRules=nil 应原样: %s", got.Response.Body)
	}
}

func TestHandleProxyAfterUpstreamReusesBeforeRoute(t *testing.T) {
	// before hook 命中路由后暂存到 Metadata，after hook 应复用而不重新查表。
	svc, st := newTestService(t)
	seedRoute(t, st, types.CapabilityRoute{
		Models:     []string{"gpt-4o"},
		Capability: capabilityName,
		Route:      types.RouteProxy,
		FieldRules: &types.FieldRules{
			ResponseStrip: []string{"usage"},
			ResponseHeaderStrip:   []string{"X-Internal"},
		},
	})
	pipe := proxyPipe(t, map[string]any{"model": "gpt-4o"})
	if _, err := svc.HandleProxyBeforeUpstream(pipe); err != nil {
		t.Fatal(err)
	}
	// 删除路由表：若 after 仍生效，证明走的是 metadata 缓存而非重新查表。
	if err := st.Remove(types.FileCapabilityRoutes); err != nil {
		t.Fatal(err)
	}
	after := &modelgateway.AfterUpstreamPayload{
		Pipe: pipe,
		Response: &modelgateway.ProxyResponse{
			StatusCode: 200,
			Header:     http.Header{"X-Internal": {"secret"}},
			Body:       []byte(`{"choices":[{"message":{"content":"hi"}}],"usage":{"total_tokens":10}}`),
		},
	}
	out, err := svc.HandleProxyAfterUpstream(after)
	if err != nil {
		t.Fatal(err)
	}
	got := out.(*modelgateway.AfterUpstreamPayload)
	if got.Response.Header.Get("X-Internal") != "" {
		t.Fatalf("X-Internal 未剔除: %+v", got.Response.Header)
	}
	if strings.Contains(string(got.Response.Body), "usage") {
		t.Fatalf("usage 未剔除（after 未复用 before 的 route）: %s", got.Response.Body)
	}
}

func TestHandleProxyBeforeUpstreamRequestHeaderStrip(t *testing.T) {
	svc, st := newTestService(t)
	seedRoute(t, st, types.CapabilityRoute{
		Models:     []string{"gpt-4o"},
		Capability: capabilityName,
		Route:      types.RouteProxy,
		FieldRules: &types.FieldRules{RequestHeaderStrip: []string{"x-api-key", "api-key"}},
	})
	pipe := &modelgateway.ProxyPipeline{
		Request: &modelgateway.ProxyRequest{
			Model: "gpt-4o",
			Body:  []byte(`{"model":"gpt-4o","messages":[]}`),
			Header: http.Header{
				"X-Api-Key":    {"client-key"},
				"Api-Key":      {"client-key-2"},
				"Content-Type": {"application/json"},
			},
		},
		Metadata: map[string]any{},
	}
	out, err := svc.HandleProxyBeforeUpstream(pipe)
	if err != nil {
		t.Fatal(err)
	}
	got := out.(*modelgateway.ProxyPipeline)
	if got.Request.Header.Get("X-Api-Key") != "" {
		t.Fatalf("X-Api-Key 未剔除: %+v", got.Request.Header)
	}
	if got.Request.Header.Get("Api-Key") != "" {
		t.Fatalf("Api-Key 未剔除: %+v", got.Request.Header)
	}
	if got.Request.Header.Get("Content-Type") == "" {
		t.Fatal("Content-Type 被误删")
	}
}

// TestDecideRouteChannelLevelExactMatch 渠道级约束：channel_base_urls 必须与
// 渠道 base_url 精确一致（含 /v1 版本段，尾斜杠忽略）。与 vision/sensitive-filter
// 共用 ChannelBaseURLMatches 语义——配错版本段会静默不命中。
func TestDecideRouteChannelLevelExactMatch(t *testing.T) {
	svc, st := newTestService(t)
	seedRoute(t, st, types.CapabilityRoute{
		Models:          []string{"gpt-4o"},
		Capability:      capabilityName,
		Route:           types.RouteProxy,
		ChannelBaseURLs: []string{"https://copilot.tencent.com/v1"},
		FieldRules:      &types.FieldRules{RequestStrip: []string{"client_metadata"}},
	})
	// 普通单 key：渠道表里 base_url 带 /v1（与路由一致）→ 命中
	if err := st.Write(types.FileChannels, []types.Channel{
		{ID: "k1", Name: "腾讯", BaseURL: "https://copilot.tencent.com/v1", Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	pipe := &modelgateway.ProxyPipeline{
		Request:  &modelgateway.ProxyRequest{Model: "gpt-4o", Body: []byte(`{"model":"gpt-4o","client_metadata":{}}`)},
		Metadata: map[string]any{"__current_channel": "k1"},
	}
	routes, err := svc.decideRoutes(pipe, "")
	if err != nil || len(routes) == 0 {
		t.Fatalf("精确匹配（带 /v1）应命中, routes=%v err=%v", routes, err)
	}
}

// TestDecideRouteChannelLevelVersionMismatch 渠道级约束版本段不一致 → 不命中。
// 这是 ChannelBaseURLMatches 精确匹配的既有语义（与 vision/sensitive-filter 一致）：
// 路由配 https://copilot.tencent.com 而渠道是 .../v1，归一化后不相等。
func TestDecideRouteChannelLevelVersionMismatch(t *testing.T) {
	svc, st := newTestService(t)
	seedRoute(t, st, types.CapabilityRoute{
		Models:          []string{"gpt-4o"},
		Capability:      capabilityName,
		Route:           types.RouteProxy,
		ChannelBaseURLs: []string{"https://copilot.tencent.com"}, // 漏写 /v1
		FieldRules:      &types.FieldRules{RequestStrip: []string{"client_metadata"}},
	})
	if err := st.Write(types.FileChannels, []types.Channel{
		{ID: "k1", Name: "腾讯", BaseURL: "https://copilot.tencent.com/v1", Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	pipe := &modelgateway.ProxyPipeline{
		Request:  &modelgateway.ProxyRequest{Model: "gpt-4o", Body: []byte(`{"model":"gpt-4o","client_metadata":{}}`)},
		Metadata: map[string]any{"__current_channel": "k1"},
	}
	routes, err := svc.decideRoutes(pipe, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 0 {
		t.Fatalf("版本段不一致不应命中（需与渠道 base_url 精确一致）: %+v", routes)
	}
}

// TestDecideRoutesProxyStacked 多条 proxy 规则同时命中 → 全部收集（叠加执行）。
// 这是用户要求的核心语义：proxy（附加）路由不再「命中第一条就返回」。
func TestDecideRoutesProxyStacked(t *testing.T) {
	svc, st := newTestService(t)
	if err := st.Write(types.FileCapabilityRoutes, []types.CapabilityRoute{
		{
			Models:     []string{"gpt-4o"},
			Capability: capabilityName,
			Route:      types.RouteProxy,
			FieldRules: &types.FieldRules{RequestStrip: []string{"client_metadata"}},
		},
		{
			Models:     []string{"gpt-4o"},
			Capability: capabilityName,
			Route:      types.RouteProxy,
			FieldRules: &types.FieldRules{RequestStrip: []string{"max_completion_tokens", "max_tokens"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	pipe := proxyPipe(t, map[string]any{
		"model":                "gpt-4o",
		"messages":             []any{},
		"client_metadata":      map[string]any{"app": "codex"},
		"max_completion_tokens": 1048576,
	})
	out, err := svc.HandleProxyBeforeUpstream(pipe)
	if err != nil {
		t.Fatal(err)
	}
	got := out.(*modelgateway.ProxyPipeline)
	var body map[string]any
	if err := json.Unmarshal(got.Request.Body, &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["client_metadata"]; ok {
		t.Fatalf("client_metadata 未剔除（第一条规则未生效）: %s", got.Request.Body)
	}
	if _, ok := body["max_completion_tokens"]; ok {
		t.Fatalf("max_completion_tokens 未剔除（第二条规则未生效）: %s", got.Request.Body)
	}
	if _, ok := body["messages"]; !ok {
		t.Fatal("messages 被误删")
	}
}

// TestDecideRoutesNativeStops 命中 native 路由 → 立即返回并跳过后续匹配。
// 即使后面还有匹配的 proxy 路由，也只执行 native 的语义（透传）。
func TestDecideRoutesNativeStops(t *testing.T) {
	svc, st := newTestService(t)
	if err := st.Write(types.FileCapabilityRoutes, []types.CapabilityRoute{
		{
			Models:     []string{"gpt-4o"},
			Capability: capabilityName,
			Route:      types.RouteNative,
		},
		{
			Models:     []string{"gpt-4o"},
			Capability: capabilityName,
			Route:      types.RouteProxy,
			FieldRules: &types.FieldRules{RequestStrip: []string{"client_metadata"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	pipe := proxyPipe(t, map[string]any{
		"model":           "gpt-4o",
		"messages":        []any{},
		"client_metadata": map[string]any{"app": "codex"},
	})
	out, err := svc.HandleProxyBeforeUpstream(pipe)
	if err != nil {
		t.Fatal(err)
	}
	got := out.(*modelgateway.ProxyPipeline)
	var body map[string]any
	if err := json.Unmarshal(got.Request.Body, &body); err != nil {
		t.Fatal(err)
	}
	// native 命中 → 后续 proxy 规则不执行，client_metadata 保留
	if _, ok := body["client_metadata"]; !ok {
		t.Fatalf("native 命中后 proxy 规则不应执行（client_metadata 被误删）: %s", got.Request.Body)
	}
}

// TestSubRequestSkipSecurityRequest 子请求跳过请求侧安检：带 __sub_request_skip_security
// 的 pipe 即使命中 field_filter 路由（含 strip 规则）也原样透传，不删请求字段——
// 视觉识别 body 是数 MB base64 dataURI，删字段会破坏图片数据。
func TestSubRequestSkipSecurityRequest(t *testing.T) {
	svc, st := newTestService(t)
	seedRoute(t, st, types.CapabilityRoute{
		Models: []string{"deepseek-chat"}, Capability: capabilityName, Route: types.RouteProxy,
		FieldRules: &types.FieldRules{RequestStrip: []string{"messages"}},
	})
	pipe := proxyPipe(t, map[string]any{"model": "deepseek-chat", "messages": []any{map[string]any{"role": "user", "content": "hi"}}})
	pipe.Metadata["__sub_request_skip_security"] = true

	out, err := svc.HandleProxyBeforeUpstream(pipe)
	if err != nil {
		t.Fatalf("skip_security 子请求不应报错: %v", err)
	}
	rp := out.(*modelgateway.ProxyPipeline)
	if string(rp.Request.Body) != string(pipe.Request.Body) {
		t.Fatalf("skip_security 子请求 body 被改:\n got: %s\nwant: %s", rp.Request.Body, pipe.Request.Body)
	}
}

// TestSubRequestSkipSecurityResponse 子请求跳过响应侧安检：响应处理同样透传。
func TestSubRequestSkipSecurityResponse(t *testing.T) {
	svc, st := newTestService(t)
	seedRoute(t, st, types.CapabilityRoute{
		Models: []string{"gpt-4o"}, Capability: capabilityName, Route: types.RouteProxy,
		FieldRules: &types.FieldRules{ResponseStrip: []string{"usage"}},
	})
	pipe := proxyPipe(t, map[string]any{"model": "gpt-4o"})
	pipe.Metadata["__sub_request_skip_security"] = true
	after := &modelgateway.AfterUpstreamPayload{
		Pipe: pipe,
		Response: &modelgateway.ProxyResponse{
			StatusCode: 200,
			Body:       []byte(`{"choices":[{"message":{"content":"hi"}}],"usage":{"total_tokens":10}}`),
		},
	}
	out, err := svc.HandleProxyAfterUpstream(after)
	if err != nil {
		t.Fatalf("skip_security 子请求响应处理不应报错: %v", err)
	}
	got := out.(*modelgateway.AfterUpstreamPayload)
	if string(got.Response.Body) != string(after.Response.Body) {
		t.Fatalf("skip_security 子请求响应被删字段:\n got: %s\nwant: %s", got.Response.Body, after.Response.Body)
	}
}


// TestDecideRoutesVirtualModel 验证虚拟模型（聚合）请求：路由配虚拟前缀 `git-*`，
// 真实模型 gpt-4o 不匹配、但 virtualModel 命中时仍命中；virtualModel 为空时不命中。
func TestDecideRoutesVirtualModel(t *testing.T) {
	svc, st := newTestService(t)
	seedRoute(t, st, types.CapabilityRoute{
		Models:     []string{"git-*"},
		Capability: capabilityName,
		Route:      types.RouteProxy,
		FieldRules: &types.FieldRules{RequestStrip: []string{"client_metadata"}},
	})
	pipe := proxyPipe(t, map[string]any{"model": "gpt-4o", "client_metadata": "x"})
	// 无虚拟模型：真实模型 gpt-4o 不匹配 git-*，不命中。
	if r, _ := svc.decideRoutes(pipe, ""); len(r) != 0 {
		t.Fatalf("virtualModel 为空不应命中: %+v", r)
	}
	// virtualModel=git-xxx 命中。
	r, err := svc.decideRoutes(pipe, "git-xxx")
	if err != nil || len(r) != 1 {
		t.Fatalf("虚拟模型应命中 1 条: %v %v", r, err)
	}
}
