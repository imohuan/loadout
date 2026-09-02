package forcestream

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"loadout/core/plugin"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// mockCtx 最小 plugin.Context：支持 On/Waterfall，其余方法空实现（与 field-filter 集成测试同构）。
type mockCtx struct {
	handlers map[string][]plugin.Handler
}

func newMockCtx() *mockCtx { return &mockCtx{handlers: map[string][]plugin.Handler{}} }

func (m *mockCtx) Get(name string) any                      { return nil }
func (m *mockCtx) Set(name string, svc any) plugin.Disposer { return func() {} }
func (m *mockCtx) On(event string, h plugin.Handler) plugin.Disposer {
	m.handlers[event] = append(m.handlers[event], h)
	return func() {}
}
func (m *mockCtx) Emit(event string, payload any) {
	for _, h := range m.handlers[event] {
		_, _ = h(payload)
	}
}
func (m *mockCtx) Waterfall(event string, payload any) (any, error) {
	cur := payload
	for _, h := range m.handlers[event] {
		next, err := h(cur)
		if err != nil {
			return nil, err
		}
		cur = next
	}
	return cur, nil
}
func (m *mockCtx) Effect(fn func()) plugin.Disposer                    { return func() {} }
func (m *mockCtx) Logger() *slog.Logger                                { return slog.Default() }
func (m *mockCtx) RegisterCheck(name string, fn func() []plugin.Issue) {}
func (m *mockCtx) RegisterRoute(spec plugin.RouteSpec) plugin.Disposer { return func() {} }

// TestForceStreamE2E 完整链路：客户端非流式 chat/completions → 命中 force_stream 路由 →
// 插件改 body stream:true 打标记 → model-gateway 缓冲上游 SSE → 整包非流式 JSON 返回客户端。
// 同时验证：上游收到的 body 带 stream:true；客户端响应是单包 application/json，content 拼接正确。
func TestForceStreamE2E(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// 记录上游收到的 body（验证 stream 是否被改写为 true）
	var upstreamBody string
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		upstreamBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		blocks := []string{
			"data: {\"id\":\"chatcmpl-e2e\",\"object\":\"chat.completion.chunk\",\"created\":1710000000,\"model\":\"deepseek-chat\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n",
			"data: {\"id\":\"chatcmpl-e2e\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":null}]}\n",
			"data: {\"id\":\"chatcmpl-e2e\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" World\"},\"finish_reason\":null}]}\n",
			"data: {\"id\":\"chatcmpl-e2e\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n",
			"data: {\"id\":\"chatcmpl-e2e\",\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2,\"total_tokens\":7}}\n",
			"data: [DONE]\n",
		}
		for _, ev := range blocks {
			_, _ = w.Write([]byte(ev))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	defer echo.Close()
	if err := st.Write(types.FileChannels, []types.Channel{
		{ID: "echo", Name: "回显", BaseURL: echo.URL, Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Write(types.FileCapabilityRoutes, []types.CapabilityRoute{{
		Models:     []string{"deepseek-chat"},
		Capability: capabilityName,
		Route:      types.RouteProxy,
	}}); err != nil {
		t.Fatal(err)
	}

	ctx := newMockCtx()
	svc := NewService(st, slog.Default())
	ctx.On(modelgateway.ProxyBeforeAttempt, svc.HandleProxyBeforeUpstream) // 与 plugin.go 一致
	gw := modelgateway.NewService(st, slog.Default(), ctx)

	// 客户端非流式请求
	body := `{"model":"deepseek-chat","stream":false,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()
	gw.HandleProxy(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("状态码 = %d: %s", rr.Code, rr.Body.String())
	}
	// 客户端应收到单包 application/json（非 SSE）
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q, 期望 application/json", ct)
	}
	// 上游收到 stream:true
	var up struct {
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal([]byte(upstreamBody), &up); err != nil {
		t.Fatalf("上游 body 非法 JSON: %v", err)
	}
	if !up.Stream {
		t.Fatalf("上游 body stream 应为 true: %s", upstreamBody)
	}
	// 客户端响应为整包 chat.completion
	var resp struct {
		Object  string `json:"object"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("客户端响应非法 JSON（可能仍是 SSE 流）: %v\n%s", err, rr.Body.String())
	}
	if resp.Object != "chat.completion" {
		t.Fatalf("object = %q, 期望 chat.completion", resp.Object)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "Hello World" {
		t.Fatalf("content 拼接错误: %+v", resp.Choices)
	}
	if resp.Choices[0].FinishReason != "stop" {
		t.Fatalf("finish_reason = %q, 期望 stop", resp.Choices[0].FinishReason)
	}
	if resp.Usage.TotalTokens != 7 {
		t.Fatalf("usage.total_tokens = %d, 期望 7", resp.Usage.TotalTokens)
	}
}

// TestForceStreamE2ENoRoute 未命中 force_stream 路由 → 全程原样透传，不破坏透明代理语义。
// 上游按非流式返回，客户端收到原样 JSON。
func TestForceStreamE2ENoRoute(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var upstreamBody string
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		upstreamBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer echo.Close()
	if err := st.Write(types.FileChannels, []types.Channel{
		{ID: "echo", Name: "回显", BaseURL: echo.URL, Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	// 不写 force_stream 路由

	ctx := newMockCtx()
	svc := NewService(st, slog.Default())
	ctx.On(modelgateway.ProxyBeforeAttempt, svc.HandleProxyBeforeUpstream)
	gw := modelgateway.NewService(st, slog.Default(), ctx)

	body := `{"model":"deepseek-chat","stream":false,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()
	gw.HandleProxy(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("状态码 = %d: %s", rr.Code, rr.Body.String())
	}
	// 未命中：上游 body 原样透传 stream:false
	if strings.Contains(upstreamBody, `"stream":true`) {
		t.Fatalf("未命中不应改 stream 为 true: %s", upstreamBody)
	}
	// 客户端收到非流式 JSON 原样透传
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应非法 JSON: %v\n%s", err, rr.Body.String())
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "ok" {
		t.Fatalf("未命中应原样透传响应: %+v", resp)
	}
}
