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
	route, err := svc.decideRoute(pipe)
	if err != nil || route == nil {
		t.Fatalf("精确匹配（带 /v1）应命中, route=%v err=%v", route, err)
	}
}
