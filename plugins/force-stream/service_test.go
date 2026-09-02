package forcestream

import (
	"encoding/json"
	"log/slog"
	"testing"

	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// newTestService 用临时目录建 Store。
func newTestService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	return NewService(st, slog.Default()), st
}

// proxyPipe 构造一个带 JSON body + 指定 path 的透明代理管线。
func proxyPipe(t *testing.T, path string, body any) *modelgateway.ProxyPipeline {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return &modelgateway.ProxyPipeline{
		Request:  &modelgateway.ProxyRequest{Body: raw, Model: "deepseek-chat", Path: path},
		Metadata: map[string]any{},
	}
}

// getStream 读取管线 body 里的 stream 字段（不存在返回 false, false）。
func getStream(t *testing.T, pipe *modelgateway.ProxyPipeline) (bool, bool) {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(pipe.Request.Body, &root); err != nil {
		t.Fatalf("解析 body 失败: %v", err)
	}
	v, ok := root["stream"]
	if !ok {
		return false, false
	}
	b, _ := v.(bool)
	return b, true
}

// writeForceRoute 写入一条 force_stream 的 proxy 路由。
func writeForceRoute(t *testing.T, st *store.Store, route types.CapabilityRoute) {
	t.Helper()
	route.Capability = capabilityName
	if route.Route == "" {
		route.Route = types.RouteProxy
	}
	var existing []types.CapabilityRoute
	_ = st.Read(types.FileCapabilityRoutes, &existing)
	if err := st.Write(types.FileCapabilityRoutes, append(existing, route)); err != nil {
		t.Fatalf("写能力路由表失败: %v", err)
	}
}

// TestHandle_NonStreamChat_Hit 非流式 chat/completions 命中 → body stream:true + 打标记。
func TestHandle_NonStreamChat_Hit(t *testing.T) {
	svc, st := newTestService(t)
	writeForceRoute(t, st, types.CapabilityRoute{Models: []string{"deepseek-chat"}})

	pipe := proxyPipe(t, "chat/completions", map[string]any{"model": "deepseek-chat", "stream": false, "messages": []any{}})
	out, err := svc.HandleProxyBeforeUpstream(pipe)
	if err != nil {
		t.Fatalf("handler 出错: %v", err)
	}
	got, ok := out.(*modelgateway.ProxyPipeline)
	if !ok {
		t.Fatalf("返回值类型错误: %T", out)
	}
	stream, present := getStream(t, got)
	if !present || !stream {
		t.Fatalf("stream 应为 true: present=%v stream=%v", present, stream)
	}
	if v, _ := got.Metadata[modelgateway.MetadataForceStream].(bool); !v {
		t.Fatalf("应打 __force_stream 标记")
	}
}

// TestHandle_AlreadyStream 客户端本来就是 stream:true → 不动（本能力只管非流式客户端）。
func TestHandle_AlreadyStream(t *testing.T) {
	svc, st := newTestService(t)
	writeForceRoute(t, st, types.CapabilityRoute{Models: []string{"deepseek-chat"}})

	pipe := proxyPipe(t, "chat/completions", map[string]any{"model": "deepseek-chat", "stream": true, "messages": []any{}})
	pipe.Request.Stream = true // 真实请求经 sniffRequest 会据此置位
	out, _ := svc.HandleProxyBeforeUpstream(pipe)
	got, _ := out.(*modelgateway.ProxyPipeline)
	if v, _ := got.Metadata[modelgateway.MetadataForceStream].(bool); v {
		t.Fatalf("流式请求不应打标记")
	}
	if s, ok := getStream(t, got); !ok || !s {
		t.Fatalf("流式请求 body 不应被改动")
	}
}

// TestHandle_NoRoute 未命中路由 → 原样透传（不打标记、不改 body）。
func TestHandle_NoRoute(t *testing.T) {
	svc, _ := newTestService(t) // 不写路由
	body := map[string]any{"model": "deepseek-chat", "stream": false, "messages": []any{}}
	pipe := proxyPipe(t, "chat/completions", body)
	rawBefore := append([]byte(nil), pipe.Request.Body...)
	out, err := svc.HandleProxyBeforeUpstream(pipe)
	if err != nil {
		t.Fatalf("handler 出错: %v", err)
	}
	got, _ := out.(*modelgateway.ProxyPipeline)
	if v, _ := got.Metadata[modelgateway.MetadataForceStream].(bool); v {
		t.Fatalf("未命中不应打标记")
	}
	if string(got.Request.Body) != string(rawBefore) {
		t.Fatalf("未命中不应改 body")
	}
}

// TestHandle_NotChatPath 命中但 path 非 chat/completions → 原样透传（首版只做 chat/completions）。
func TestHandle_NotChatPath(t *testing.T) {
	svc, st := newTestService(t)
	writeForceRoute(t, st, types.CapabilityRoute{Models: []string{"deepseek-chat"}})

	pipe := proxyPipe(t, "responses", map[string]any{"model": "deepseek-chat", "stream": false, "input": []any{}})
	rawBefore := append([]byte(nil), pipe.Request.Body...)
	out, _ := svc.HandleProxyBeforeUpstream(pipe)
	got, _ := out.(*modelgateway.ProxyPipeline)
	if v, _ := got.Metadata[modelgateway.MetadataForceStream].(bool); v {
		t.Fatalf("非 chat/completions 不应打标记")
	}
	if string(got.Request.Body) != string(rawBefore) {
		t.Fatalf("非 chat/completions 不应改 body")
	}
}

// TestHandle_NativeRoute native 路由 → 原样透传。
func TestHandle_NativeRoute(t *testing.T) {
	svc, st := newTestService(t)
	writeForceRoute(t, st, types.CapabilityRoute{Models: []string{"deepseek-chat"}, Route: types.RouteNative})

	pipe := proxyPipe(t, "chat/completions", map[string]any{"model": "deepseek-chat", "stream": false})
	out, _ := svc.HandleProxyBeforeUpstream(pipe)
	got, _ := out.(*modelgateway.ProxyPipeline)
	if v, _ := got.Metadata[modelgateway.MetadataForceStream].(bool); v {
		t.Fatalf("native 路由不应打标记")
	}
}

// TestHandle_IdempotentRetry 二次触发（failover）不应重复处理：标记已在则早退。
func TestHandle_IdempotentRetry(t *testing.T) {
	svc, st := newTestService(t)
	writeForceRoute(t, st, types.CapabilityRoute{Models: []string{"deepseek-chat"}})

	body := map[string]any{"model": "deepseek-chat", "stream": false, "messages": []any{}}
	pipe := proxyPipe(t, "chat/completions", body)
	// 首次
	out, _ := svc.HandleProxyBeforeUpstream(pipe)
	got1, _ := out.(*modelgateway.ProxyPipeline)
	if s, _ := getStream(t, got1); !s {
		t.Fatalf("首次应改 stream=true")
	}
	bodyAfterFirst := append([]byte(nil), got1.Request.Body...)
	// 二次（模拟 failover 同 pipe 再触发）
	out2, _ := svc.HandleProxyBeforeUpstream(got1)
	got2, _ := out2.(*modelgateway.ProxyPipeline)
	// 二次应早退，body 字节保持不变（不再 JSON 重排）
	if string(got2.Request.Body) != string(bodyAfterFirst) {
		t.Fatalf("二次触发不应再改 body")
	}
}

// TestSetStreamTrue_PresentFalse stream:false → true。
func TestSetStreamTrue_PresentFalse(t *testing.T) {
	out, err := setStreamTrue([]byte(`{"model":"m","stream":false}`))
	if err != nil {
		t.Fatalf("setStreamTrue 出错: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		t.Fatalf("结果非法 JSON: %v", err)
	}
	if v, _ := root["stream"].(bool); !v {
		t.Fatalf("stream 应改为 true: %v", root["stream"])
	}
}

// TestSetStreamTrue_Absent 无 stream 字段 → 补 true。
func TestSetStreamTrue_Absent(t *testing.T) {
	out, err := setStreamTrue([]byte(`{"model":"m"}`))
	if err != nil {
		t.Fatalf("setStreamTrue 出错: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		t.Fatalf("结果非法 JSON: %v", err)
	}
	if v, _ := root["stream"].(bool); !v {
		t.Fatalf("应补 stream=true")
	}
}

// TestSetStreamTrue_AlreadyTrue stream 已是 true → 原样返回（幂等）。
func TestSetStreamTrue_AlreadyTrue(t *testing.T) {
	in := []byte(`{"stream":true,"model":"m"}`)
	out, err := setStreamTrue(in)
	if err != nil {
		t.Fatalf("setStreamTrue 出错: %v", err)
	}
	if string(out) != string(in) {
		t.Fatalf("已是 true 时应原样返回，实际: %s", out)
	}
}

// TestSetStreamTrue_NonJSON 非 JSON → 报错。
func TestSetStreamTrue_NonJSON(t *testing.T) {
	if _, err := setStreamTrue([]byte(`not-json`)); err == nil {
		t.Fatalf("非 JSON 应报错")
	}
}
