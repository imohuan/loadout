package modelgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"loadout/core/db"
	"loadout/plugins/contracts"
	gatewaykeys "loadout/plugins/gateway-keys"
	"loadout/plugins/types"
	routelog "loadout/plugins/route-log"
)

// echoRecord 回显服务器记录的一次请求。
type echoRecord struct {
	Method  string
	Path    string
	Query   string
	Body    []byte
	Headers http.Header
}

// newEchoServer 起一个回显上游：记录请求细节，按配置响应。
// respBody 为响应体；status 非 0 时返回该状态码；sse 非 nil 时按 SSE 逐段写出。
func newEchoServer(t *testing.T, respBody string, status int, sse []string) (*httptest.Server, func() []echoRecord) {
	t.Helper()
	var mu sync.Mutex
	var records []echoRecord
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		records = append(records, echoRecord{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: body, Headers: r.Header.Clone()})
		mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(respBody))
			return
		}
		if sse != nil {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, ev := range sse {
				_, _ = w.Write([]byte(ev))
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(respBody))
	}))
	return srv, func() []echoRecord {
		mu.Lock()
		defer mu.Unlock()
		return append([]echoRecord(nil), records...)
	}
}

// writeEchoChannel 写入一条指向 echo server 的渠道（模型列表为空 = 未知渠道）。
func writeEchoChannel(t *testing.T, svc *Service, baseURL string) {
	t.Helper()
	if err := svc.st.Write(types.FileChannels, []types.Channel{
		{ID: "echo", Name: "回显渠道", BaseURL: baseURL, Enabled: true},
	}); err != nil {
		t.Fatalf("写渠道表失败: %v", err)
	}
}

// doProxy 构造请求打到 HandleProxy。
func doProxy(t *testing.T, svc *Service, method, path, query, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/v1/"+path+"?"+query, strings.NewReader(body))
	rr := httptest.NewRecorder()
	svc.HandleProxy(rr, req)
	return rr
}

// TestHandleProxyTransparent 任意路径原样转发：路径/query/body 逐字节透传。
func TestHandleProxyTransparent(t *testing.T) {
	svc, _ := newTestService(t)
	echo, getRecords := newEchoServer(t, `{"id":"resp_1","object":"response","output":[]}`, 0, nil)
	defer echo.Close()
	writeEchoChannel(t, svc, echo.URL)

	body := `{"model":"gpt-5","input":[{"role":"user","content":"hi"}],"max_output_tokens":999,"extra_field":"keep-me"}`
	rr := doProxy(t, svc, "POST", "responses", "x=1&y=2", body)

	if rr.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200", rr.Code)
	}
	if got := rr.Body.String(); !strings.Contains(got, "resp_1") {
		t.Fatalf("响应体未透传: %s", got)
	}
	recs := getRecords()
	if len(recs) != 1 {
		t.Fatalf("上游收到 %d 个请求, 期望 1", len(recs))
	}
	rec := recs[0]
	if rec.Path != "/responses" {
		t.Fatalf("上游路径 = %s, 期望 /responses（base_url 不再自动补 /v1）", rec.Path)
	}
	if rec.Query != "x=1&y=2" {
		t.Fatalf("上游 query = %s, 期望 x=1&y=2", rec.Query)
	}
	if string(rec.Body) != body {
		t.Fatalf("body 未原样透传:\n上游: %s\n原值: %s", rec.Body, body)
	}
}

// TestHandleProxyAnyMethod 任意 HTTP 方法原样转发（GET 等非对话端点）。
func TestHandleProxyAnyMethod(t *testing.T) {
	svc, _ := newTestService(t)
	echo, getRecords := newEchoServer(t, `{"object":"list","data":[]}`, 0, nil)
	defer echo.Close()
	writeEchoChannel(t, svc, echo.URL)

	rr := doProxy(t, svc, "GET", "files", "limit=5", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200", rr.Code)
	}
	recs := getRecords()
	if len(recs) != 1 || recs[0].Method != "GET" || recs[0].Path != "/files" {
		t.Fatalf("GET /files 未原样转发（base_url 不再自动补 /v1）: %+v", recs)
	}
}

// TestHandleProxyStripAltAuthOnCopilot 腾讯 copilot 网关定向剔除 x-api-key/api-key：
// 渠道 base_url 指向 copilot.tencent.com 时，客户端透传的 x-api-key/api-key 不得
// 转发到上游（否则覆盖渠道 Authorization 导致 401）；其他平台保持原样透传。
func TestHandleProxyStripAltAuthOnCopilot(t *testing.T) {
	svc, _ := newTestService(t)
	echo, getRecords := newEchoServer(t, `{"choices":[{"message":{"content":"ok"}}]}`, 0, nil)
	defer echo.Close()

	// 渠道 base_url 使用 copilot.tencent.com（真实上游不可达，但代理只按 host 判断剔除头，
	// 请求会因 DNS/连接失败而失败——因此这里让回显服务器与 copilot.tencent.com 同 host 不现实，
	// 改为：先用普通渠道验证透传行为，再单测 isCopilotTencentBaseURL 判定函数。）
	_ = echo

	// 1) 普通渠道（非 copilot）：x-api-key 应原样透传（透明代理语义）。
	if err := svc.st.Write(types.FileChannels, []types.Channel{
		{ID: "echo", Name: "回显渠道", BaseURL: echo.URL, Enabled: true},
	}); err != nil {
		t.Fatalf("写渠道表失败: %v", err)
	}
	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("x-api-key", "sk-client-key")
	req.Header.Set("api-key", "sk-client-key-2")
	rr := httptest.NewRecorder()
	svc.HandleProxy(rr, req)
	recs := getRecords()
	if len(recs) != 1 {
		t.Fatalf("普通渠道：上游收到 %d 个请求, 期望 1", len(recs))
	}
	if got := recs[0].Headers.Get("X-Api-Key"); got != "sk-client-key" {
		t.Fatalf("普通渠道 x-api-key 应透传，实际 %q", got)
	}
	if got := recs[0].Headers.Get("Api-Key"); got != "sk-client-key-2" {
		t.Fatalf("普通渠道 api-key 应透传，实际 %q", got)
	}

	// 2) 判定函数：copilot.tencent.com 命中，其他地址不命中。
	if !isCopilotTencentBaseURL("https://copilot.tencent.com/v2") {
		t.Fatal("isCopilotTencentBaseURL 应命中 https://copilot.tencent.com/v2")
	}
	if isCopilotTencentBaseURL(echo.URL) {
		t.Fatal("isCopilotTencentBaseURL 不应命中普通回显地址")
	}
	if isCopilotTencentBaseURL("") {
		t.Fatal("isCopilotTencentBaseURL 不应命中空地址")
	}
}

// TestHandleProxyTransparentClientMetadata 普通渠道（非 copilot）body 带
// client_metadata 应原样透传（透明代理语义不因该字段破坏）。
func TestHandleProxyTransparentClientMetadata(t *testing.T) {
	svc, _ := newTestService(t)
	echo, getRecords := newEchoServer(t, `{"choices":[{"message":{"content":"ok"}}]}`, 0, nil)
	defer echo.Close()
	writeEchoChannel(t, svc, echo.URL)

	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"client_metadata":{"app":"yuantao","version":"1.0"}}`
	rr := doProxy(t, svc, "POST", "chat/completions", "", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200", rr.Code)
	}
	recs := getRecords()
	if len(recs) != 1 {
		t.Fatalf("上游收到 %d 个请求, 期望 1", len(recs))
	}
	if string(recs[0].Body) != body {
		t.Fatalf("普通渠道 body 应原样透传:\n上游: %s\n原值: %s", recs[0].Body, body)
	}
}

// TestStripCopilotClientMetadata 腾讯 copilot 网关定向剔除 client_metadata：
// 有该字段 → 剔除且其余字段保留；无该字段/非 JSON/空 body → 原字节返回。
func TestStripCopilotClientMetadata(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want func(string) bool // 断言函数：true 表示通过
	}{
		{
			name: "剔除 client_metadata 并保留其余字段",
			in:   `{"model":"gpt-4o","client_metadata":{"app":"yuantao"},"messages":[]}`,
			want: func(out string) bool {
				var obj map[string]json.RawMessage
				if err := json.Unmarshal([]byte(out), &obj); err != nil {
					return false
				}
				if _, ok := obj["client_metadata"]; ok {
					return false
				}
				if _, ok := obj["model"]; !ok {
					return false
				}
				if _, ok := obj["messages"]; !ok {
					return false
				}
				return true
			},
		},
		{
			name: "仅 client_metadata 一个字段时输出空对象",
			in:   `{"client_metadata":{"app":"yuantao"}}`,
			want: func(out string) bool { return out == `{}` },
		},
		{
			name: "无该字段时原字节返回",
			in:   `{"model":"gpt-4o","messages":[]}`,
			want: func(out string) bool { return out == `{"model":"gpt-4o","messages":[]}` },
		},
		{
			name: "非 JSON body 原样返回",
			in:   `not json at all`,
			want: func(out string) bool { return out == `not json at all` },
		},
		{
			name: "空 body 原样返回",
			in:   ``,
			want: func(out string) bool { return out == `` },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if out := stripCopilotClientMetadata([]byte(tt.in)); !tt.want(string(out)) {
				t.Fatalf("输入 %q 清洗后 = %q, 断言未通过", tt.in, out)
			}
		})
	}
}

// TestHandleProxyStream SSE 流式透传。
func TestHandleProxyStream(t *testing.T) {
	svc, _ := newTestService(t)
	sse := []string{"data: {\"choices\":[{\"delta\":{\"content\":\"你\"}}]}\n\n", "data: {\"choices\":[{\"delta\":{\"content\":\"好\"}}]}\n\n", "data: [DONE]\n\n"}
	echo, _ := newEchoServer(t, "", 0, sse)
	defer echo.Close()
	writeEchoChannel(t, svc, echo.URL)

	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stream":true}`
	rr := doProxy(t, svc, "POST", "chat/completions", "", body)

	got := rr.Body.String()
	for _, want := range []string{"你", "好", "[DONE]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("流式响应缺少 %q: %s", want, got)
		}
	}
}

// TestProxyStreamDoneHangsUpstream 回归测试：上游发完 [DONE] 后保持连接不关闭
// （模拟火山引擎 keep-alive 行为），代理必须在 [DONE] 处主动退出，
// 不能阻塞等 EOF（旧实现会卡到连接 idle 超时 ~90s，导致"上游已成功但请求卡进行中"）。
func TestProxyStreamDoneHangsUpstream(t *testing.T) {
	svc, _ := newTestService(t)
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// 模拟 keep-alive：写完 [DONE] 后不返回、不关闭连接，等客户端主动断开。
		<-r.Context().Done()
		close(done)
	}))
	defer srv.Close()
	writeEchoChannel(t, svc, srv.URL)

	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stream":true}`
	start := time.Now()
	rr := doProxy(t, svc, "POST", "chat/completions", "", body)
	elapsed := time.Since(start)

	if !strings.Contains(rr.Body.String(), "[DONE]") {
		t.Fatalf("响应应包含 [DONE]: %s", rr.Body.String())
	}
	// 代理应在 [DONE] 处立即返回，绝不等上游挂起的连接（阈值放宽到 2s，CI 也稳）。
	if elapsed > 2*time.Second {
		t.Fatalf("代理在 [DONE] 后未主动退出，耗时 %v（预期 <2s，疑似卡等 EOF）", elapsed)
	}
	// 确认上游 handler 确实没有自行返回（证明返回不是靠 EOF，而是 [DONE] 检测）。
	select {
	case <-done:
		t.Fatal("上游 handler 不应自行返回（它等的是客户端断开），代理却先返回了？")
	default:
	}
}

// TestHandleProxyStreamContentType 流式响应透传上游 Content-Type（text/event-stream），
// 防止 Go 嗅探成 text/plain 导致 SSE 客户端解析失败。
func TestHandleProxyStreamContentType(t *testing.T) {
	svc, _ := newTestService(t)
	// 回显服务器设置 SSE Content-Type。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Custom", "keep-me")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: {\"x\":1}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer srv.Close()
	writeEchoChannel(t, svc, srv.URL)

	body := `{"model":"gpt-4o","messages":[],"stream":true}`
	rr := doProxy(t, svc, "POST", "chat/completions", "", body)

	if ct := rr.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("流式 Content-Type 应为 text/event-stream，实际 %q", ct)
	}
	if cc := rr.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("流式 Cache-Control 应透传，实际 %q", cc)
	}
	if xc := rr.Header().Get("X-Custom"); xc != "keep-me" {
		t.Fatalf("流式自定义头 X-Custom 应透传，实际 %q", xc)
	}
}

// TestHandleProxyResponseHeaders 非流式响应头全量透传（输出 hook 可改 header）。
func TestHandleProxyResponseHeaders(t *testing.T) {
	svc, _ := newTestService(t)
	echo, _ := newEchoServerWithHeaders(t, map[string]string{
		"Content-Type": "application/json",
		"X-RateLimit":  "100",
		"Retry-After":  "5",
		"Connection":   "keep-alive", // hop-by-hop 应被剔除
	})
	defer echo.Close()
	writeEchoChannel(t, svc, echo.URL)

	rr := doProxy(t, svc, "POST", "responses", "", `{"model":"gpt-5","input":[]}`)
	if rr.Header().Get("X-RateLimit") != "100" {
		t.Fatalf("X-RateLimit 应透传，实际 %q", rr.Header().Get("X-RateLimit"))
	}
	if rr.Header().Get("Retry-After") != "5" {
		t.Fatalf("Retry-After 应透传，实际 %q", rr.Header().Get("Retry-After"))
	}
	if rr.Header().Get("Connection") != "" {
		t.Fatalf("hop-by-hop 头 Connection 不应透传，实际 %q", rr.Header().Get("Connection"))
	}
}

// TestHandleProxyErrorHeaders 全部失败时透传上游错误头（Retry-After）。
func TestHandleProxyErrorHeaders(t *testing.T) {
	svc, _ := newTestService(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"rate-limited","type":"rate_limit_error"}}`)
	}))
	defer srv.Close()
	writeEchoChannel(t, svc, srv.URL)

	rr := doProxy(t, svc, "POST", "responses", "", `{"model":"gpt-5","input":[]}`)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("状态码应为 429，实际 %d", rr.Code)
	}
	if rr.Header().Get("Retry-After") != "30" {
		t.Fatalf("429 应透传 Retry-After，实际 %q", rr.Header().Get("Retry-After"))
	}
}

// newEchoServerWithHeaders 起一个带固定响应头的回显服务器。
func newEchoServerWithHeaders(t *testing.T, headers map[string]string) (*httptest.Server, func() []echoRecord) {
	t.Helper()
	var mu sync.Mutex
	var records []echoRecord
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		records = append(records, echoRecord{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: body, Headers: r.Header.Clone()})
		mu.Unlock()
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	return srv, func() []echoRecord {
		mu.Lock()
		defer mu.Unlock()
		return append([]echoRecord(nil), records...)
	}
}

// TestHandleProxyNoModel 请求体无 model：不做匹配，直接转发全部启用渠道。
func TestHandleProxyNoModel(t *testing.T) {
	svc, _ := newTestService(t)
	echo, getRecords := newEchoServer(t, `{"ok":true}`, 0, nil)
	defer echo.Close()
	writeEchoChannel(t, svc, echo.URL)

	body := `{"input":"anything","without":"model"}`
	rr := doProxy(t, svc, "POST", "embeddings", "", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("无 model 请求被拒绝: %d %s", rr.Code, rr.Body.String())
	}
	if recs := getRecords(); len(recs) != 1 || string(recs[0].Body) != body {
		t.Fatalf("无 model 请求未原样转发: %+v", recs)
	}
}

// TestHandleProxyBeforeHook 输入 hook 修改请求体与 model，上游收到改写后内容。
func TestHandleProxyBeforeHook(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := svc.ctx.(*mockCtx)
	echo, getRecords := newEchoServer(t, `{"ok":true}`, 0, nil)
	defer echo.Close()
	writeEchoChannel(t, svc, echo.URL)

	ctx.On(ProxyBeforeUpstream, func(payload any) (any, error) {
		pipe := payload.(*ProxyPipeline)
		pipe.Request.Body = []byte(`{"model":"rewritten","input":[{"role":"user","content":"改过"}]}`)
		pipe.Request.Model = "rewritten"
		return pipe, nil
	})

	doProxy(t, svc, "POST", "responses", "", `{"model":"orig","input":[]}`)
	recs := getRecords()
	if len(recs) != 1 {
		t.Fatalf("上游收到 %d 个请求", len(recs))
	}
	if got := string(recs[0].Body); !strings.Contains(got, "rewritten") || !strings.Contains(got, "改过") {
		t.Fatalf("上游未收到改写后的 body: %s", got)
	}
}

// TestHandleProxyAfterHook 输出 hook 修改响应体。
func TestHandleProxyAfterHook(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := svc.ctx.(*mockCtx)
	echo, _ := newEchoServer(t, `{"original":"yes"}`, 0, nil)
	defer echo.Close()
	writeEchoChannel(t, svc, echo.URL)

	ctx.On(ProxyAfterUpstream, func(payload any) (any, error) {
		ap := payload.(*AfterUpstreamPayload)
		ap.Response.Body = []byte(`{"patched":"by-plugin"}`)
		return ap, nil
	})

	rr := doProxy(t, svc, "POST", "chat/completions", "", `{"model":"m","messages":[]}`)
	if !strings.Contains(rr.Body.String(), "by-plugin") {
		t.Fatalf("客户端未收到 hook 修改后的响应: %s", rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "original") {
		t.Fatalf("客户端不应收到原始响应")
	}
}

// TestHandleProxyStreamChunkHook 流式 chunk hook 删除指定块。
func TestHandleProxyStreamChunkHook(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := svc.ctx.(*mockCtx)
	sse := []string{"data: {\"choices\":[{\"delta\":{\"content\":\"A\"}}]}\n\n", "data: {\"choices\":[{\"delta\":{\"content\":\"B\"}}]}\n\n"}
	echo, _ := newEchoServer(t, "", 0, sse)
	defer echo.Close()
	writeEchoChannel(t, svc, echo.URL)

	ctx.On(ProxyStreamChunk, func(payload any) (any, error) {
		chunk := payload.(*StreamChunkPayload)
		if strings.Contains(string(chunk.Data), "B") {
			chunk.Data = nil // 删除包含 B 的块
		}
		return chunk, nil
	})

	rr := doProxy(t, svc, "POST", "chat/completions", "", `{"model":"m","messages":[],"stream":true}`)
	got := rr.Body.String()
	if strings.Contains(got, "B") {
		t.Fatalf("流式块 B 未被删除: %s", got)
	}
	if !strings.Contains(got, "A") {
		t.Fatalf("流式块 A 应保留: %s", got)
	}
}

// TestHandleProxyFailover 首渠道失败自动切换第二渠道，最终响应来自第二渠道。
func TestHandleProxyFailover(t *testing.T) {
	svc, _ := newTestService(t)
	bad, _ := newEchoServer(t, `{"error":{"message":"boom"}}`, 500, nil)
	defer bad.Close()
	good, _ := newEchoServer(t, `{"ok":"from-good"}`, 0, nil)
	defer good.Close()
	if err := svc.st.Write(types.FileChannels, []types.Channel{
		{ID: "bad", Name: "坏渠道", BaseURL: bad.URL, Enabled: true},
		{ID: "good", Name: "好渠道", BaseURL: good.URL, Enabled: true},
	}); err != nil {
		t.Fatalf("写渠道表失败: %v", err)
	}

	rr := doProxy(t, svc, "POST", "responses", "", `{"model":"gpt-5","input":[]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("failover 后状态码 = %d, 期望 200: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "from-good") {
		t.Fatalf("failover 后应收到好渠道响应: %s", rr.Body.String())
	}
}

// TestHandleProxyAllFailed 全部渠道失败：原样透传最后一条上游错误。
func TestHandleProxyAllFailed(t *testing.T) {
	svc, _ := newTestService(t)
	bad, _ := newEchoServer(t, `{"error":{"message":"rate-limited","type":"rate_limit_error"}}`, 429, nil)
	defer bad.Close()
	writeEchoChannel(t, svc, bad.URL)

	rr := doProxy(t, svc, "POST", "responses", "", `{"model":"gpt-5","input":[]}`)
	if rr.Code != 429 {
		t.Fatalf("状态码 = %d, 期望透传 429", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "rate-limited") {
		t.Fatalf("错误体未原样透传: %s", rr.Body.String())
	}
}

// TestSplitV2Model 前缀拆分：最长 ChannelName 前缀匹配。
func TestSplitV2Model(t *testing.T) {
	isChannel := func(s string) bool {
		switch s {
		case "newapi", "team/workbuddy", "ab":
			return true
		}
		return false
	}
	cases := []struct {
		name       string
		model      string
		wantHint   string
		wantReal   string
		wantOK     bool
	}{
		{"普通前缀", "newapi/gpt-4o", "newapi", "gpt-4o", true},
		{"无前缀", "gpt-4o", "", "", false},
		{"模型名自带斜杠且前缀非渠道", "meta-llama/llama-3.1-8b", "", "", false},
		{"前缀包含优先最长", "ab/xxx", "ab", "xxx", true},
		{"渠道名前缀含斜杠", "team/workbuddy/deepseek-chat", "team/workbuddy", "deepseek-chat", true},
		{"空渠道名不参与", "/gpt-4o", "", "", false},
		{"前缀匹配但后半为空", "newapi/", "newapi", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hint, real, ok := splitV2Model(c.model, isChannel)
			if ok != c.wantOK || hint != c.wantHint || real != c.wantReal {
				t.Fatalf("splitV2Model(%q) = (%q, %q, %v), 期望 (%q, %q, %v)",
					c.model, hint, real, ok, c.wantHint, c.wantReal, c.wantOK)
			}
		})
	}
}

// TestRewriteModelField body model 字段改写：仅替换 model 值，其余字节不动。
func TestRewriteModelField(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"标准格式替换",
			`{"model":"newapi/gpt-4o","temperature":0.7}`,
			`{"model":"gpt-4o","temperature":0.7}`,
		},
		{
			"保留缩进与字段顺序",
			"{\n  \"model\": \"newapi/gpt-4o\",\n  \"temperature\": 0.7\n}",
			"{\n  \"model\": \"gpt-4o\",\n  \"temperature\": 0.7\n}",
		},
		{
			"大数精度不丢",
			`{"model":"newapi/gpt-4o","seed":12345678901234567890}`,
			`{"model":"gpt-4o","seed":12345678901234567890}`,
		},
		{
			"无 model 字段原样返回",
			`{"temperature":0.5}`,
			`{"temperature":0.5}`,
		},
		{
			"非 JSON 原样返回",
			`not json at all`,
			`not json at all`,
		},
		{
			"前导空白可处理",
			`  {"model":"newapi/gpt-4o","x":1}`,
			`  {"model":"gpt-4o","x":1}`,
		},
		{
			"BOM 前缀可处理",
			"\xEF\xBB\xBF{\"model\":\"newapi/gpt-4o\",\"x\":1}",
			"\xEF\xBB\xBF{\"model\":\"gpt-4o\",\"x\":1}",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := rewriteModelField([]byte(c.in), "newapi/gpt-4o", "gpt-4o")
			if string(got) != c.want {
				t.Fatalf("rewriteModelField 结果 = %q, 期望 %q", string(got), c.want)
			}
		})
	}
}

// TestEstimateTokens 估算口径：CJK ≈ 1 token/字，其他 ≈ 4 字符/token（向上取整）。
func TestEstimateTokens(t *testing.T) {
	cases := []struct{ in string; want int }{
		{"", 0},
		{"你好世界", 4},       // 4 个 CJK
		{"hello", 2},        // 5 字符 / 4 = 1.25 → 2
		{"hello world!", 3}, // 12 字符 / 4 = 3
		{"你好 hello", 4},    // 2 CJK + 6 字符 → 2 + 2 = 4
	}
	for _, c := range cases {
		if got := estimateTokens(c.in); got != c.want {
			t.Fatalf("estimateTokens(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestRewriteModelQuery query 参数 model 改写。
func TestRewriteModelQuery(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"标准", "model=newapi%2Fgpt-4o&stream=true", "model=gpt-4o&stream=true"},
		{"model 在中间", "stream=true&model=newapi%2Fgpt-4o&x=1", "model=gpt-4o&stream=true&x=1"},
		{"无 model 原样", "stream=true", "stream=true"},
		{"空 query 原样", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := rewriteModelQuery(c.in, "newapi/gpt-4o", "gpt-4o")
			if got != c.want {
				t.Fatalf("rewriteModelQuery(%q) = %q, 期望 %q", c.in, got, c.want)
			}
		})
	}
}

// TestProxyStreamAttemptFirstByteAndEstTokens：流式 attempt 有 first_byte_at（TTFB），
// completion_tokens 为节流写入的估算值（非 0）；流结束后 route log 里能看到。
func TestProxyStreamAttemptFirstByteAndEstTokens(t *testing.T) {
	svc, _ := newTestService(t)
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("db.OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	rl := routelog.NewService(database, slog.Default())
	svc.SetRoutingServices(database, &mockHealth{}, rl)

	// SSE 回显：两段中文内容 + [DONE]
	sse := []string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"你好世界\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"再次输出\"}}]}\n\n",
		"data: [DONE]\n\n",
	}
	echo, _ := newEchoServer(t, "", 0, sse)
	defer echo.Close()
	// SetRoutingServices 传了 database → routing 非 nil，候选渠道必须写进 SQLite channels 表。
	if _, err := database.Exec(`INSERT INTO channels(id, name, base_url, manual_enabled, sync_billing, created_at, updated_at) VALUES ('echo', '回显渠道', ?, 1, 1, 'now', 'now')`, echo.URL); err != nil {
		t.Fatalf("插入渠道失败: %v", err)
	}

	body := `{"model":"gpt-4o","messages":[],"stream":true}`
	rr := doProxy(t, svc, "POST", "chat/completions", "", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200: %s", rr.Code, rr.Body.String())
	}
	requestID := rr.Header().Get("X-Request-Id")
	if requestID == "" {
		t.Fatalf("响应头 X-Request-Id 应存在")
	}
	detail, err := svc.routeLog.Detail(context.Background(), requestID)
	if err != nil {
		t.Fatalf("route log Detail: %v", err)
	}
	if len(detail.Attempts) == 0 {
		t.Fatalf("应有流式 attempt，实际 0 条")
	}
	last := detail.Attempts[len(detail.Attempts)-1]
	if last.FirstByteAt == nil {
		t.Fatalf("attempt first_byte_at 应为非空（流式 TTFB），实际 nil")
	}
	if last.CompletionTokens <= 0 {
		t.Fatalf("attempt completion_tokens 应为节流估算值（非 0），实际 %d", last.CompletionTokens)
	}
	if !last.Stream {
		t.Fatalf("attempt stream 应为 true，实际 false")
	}
	if last.Action != "首次尝试" {
		t.Fatalf("attempt action 应为首次尝试（回归：done=true 收尾不得把 action 覆盖回默认值），实际 %q", last.Action)
	}
}

// TestHandleProxyV2Prefix v2 前缀路由：model="newapi/gpt-4o" 锁定 newapi 渠道组，
// 转发 body 的 model 改写回 gpt-4o；无前缀请求与 v1 行为一致。
func TestHandleProxyV2Prefix(t *testing.T) {
	svc, _ := newTestService(t)
	echo, getRecords := newEchoServer(t, `{"id":"resp_v2","object":"response","output":[]}`, 0, nil)
	defer echo.Close()
	// 两个渠道：newapi（启用）与 other（启用但不应被命中）。
	if err := svc.st.Write(types.FileChannels, []types.Channel{
		{ID: "c1", Name: "Key1", ChannelName: "newapi", BaseURL: echo.URL, Enabled: true},
		{ID: "c2", Name: "Key2", ChannelName: "other", BaseURL: echo.URL, Enabled: true},
	}); err != nil {
		t.Fatalf("写渠道表失败: %v", err)
	}

	// v2 前缀请求：model 带 newapi/ 前缀。
	body := `{"model":"newapi/gpt-4o","input":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v2/responses", strings.NewReader(body))
	rr := httptest.NewRecorder()
	svc.HandleProxyV2(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200: %s", rr.Code, rr.Body.String())
	}
	recs := getRecords()
	if len(recs) != 1 {
		t.Fatalf("上游收到 %d 个请求, 期望 1", len(recs))
	}
	// body model 必须改写回真实名。
	var sent struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(recs[0].Body, &sent); err != nil {
		t.Fatalf("解析上游 body 失败: %v", err)
	}
	if sent.Model != "gpt-4o" {
		t.Fatalf("上游 model = %q, 期望 gpt-4o（前缀应被改写）", sent.Model)
	}
	// 路径原样转发。
	if recs[0].Path != "/responses" {
		t.Fatalf("上游路径 = %s, 期望 /responses（base_url 不再自动补 /v1）", recs[0].Path)
	}
}

// TestHandleProxyV2NoPrefix 无前缀 model 走 v1 逻辑（字节级透传）。
func TestHandleProxyV2NoPrefix(t *testing.T) {
	svc, _ := newTestService(t)
	echo, getRecords := newEchoServer(t, `{"id":"resp_v1","object":"response","output":[]}`, 0, nil)
	defer echo.Close()
	writeEchoChannel(t, svc, echo.URL)

	body := `{"model":"gpt-4o","input":[]}`
	req := httptest.NewRequest("POST", "/v2/responses", strings.NewReader(body))
	rr := httptest.NewRecorder()
	svc.HandleProxyV2(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200: %s", rr.Code, rr.Body.String())
	}
	recs := getRecords()
	if len(recs) != 1 {
		t.Fatalf("上游收到 %d 个请求, 期望 1", len(recs))
	}
	if string(recs[0].Body) != body {
		t.Fatalf("body 未原样透传:\n上游: %s\n原值: %s", recs[0].Body, body)
	}
}

// TestHandleProxyV2UnknownChannel 前缀渠道组存在但无 Key 支持该模型 → 502 no_available_channel。
func TestHandleProxyV2UnknownChannel(t *testing.T) {
	svc, _ := newTestService(t)
	echo, _ := newEchoServer(t, `{}`, 0, nil)
	defer echo.Close()
	// newapi 渠道启用，但 Models 明确只含 deepseek-chat（不含 gpt-4o）→ 前缀路由后无候选。
	if err := svc.st.Write(types.FileChannels, []types.Channel{
		{ID: "c1", Name: "Key1", ChannelName: "newapi", BaseURL: echo.URL, Enabled: true, Models: []string{"deepseek-chat"}},
	}); err != nil {
		t.Fatalf("写渠道表失败: %v", err)
	}

	body := `{"model":"newapi/gpt-4o","input":[]}`
	req := httptest.NewRequest("POST", "/v2/responses", strings.NewReader(body))
	rr := httptest.NewRecorder()
	svc.HandleProxyV2(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("状态码 = %d, 期望 502 no_available_channel: %s", rr.Code, rr.Body.String())
	}
}

// TestHandleProxyV2ModelSlashFallback 前缀非渠道名（模型名自带斜杠）→ 不拆，走 v1 逻辑。
func TestHandleProxyV2ModelSlashFallback(t *testing.T) {
	svc, _ := newTestService(t)
	echo, getRecords := newEchoServer(t, `{"id":"resp_v1","object":"response","output":[]}`, 0, nil)
	defer echo.Close()
	writeEchoChannel(t, svc, echo.URL)

	// ghost 不是启用渠道名 → meta-llama/llama-3.1-8b 整体当真实模型名转发。
	body := `{"model":"meta-llama/llama-3.1-8b","input":[]}`
	req := httptest.NewRequest("POST", "/v2/responses", strings.NewReader(body))
	rr := httptest.NewRecorder()
	svc.HandleProxyV2(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200（模型名自带斜杠应走 v1）: %s", rr.Code, rr.Body.String())
	}
	recs := getRecords()
	if len(recs) != 1 || string(recs[0].Body) != body {
		t.Fatalf("body 未原样透传: %+v", recs)
	}
}

// TestHandleProxyAfterHookUsageKept 输出 hook 剔除 usage 字段后，token 计量仍基于
// 上游原始响应（否则 volc-free-quota 不扣减、route_log token 列恒 0）。
func TestHandleProxyAfterHookUsageKept(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := svc.ctx.(*mockCtx)
	echo, _ := newEchoServer(t, `{"choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`, 0, nil)
	defer echo.Close()
	writeEchoChannel(t, svc, echo.URL)

	var gotUsage contracts.TokenUsage
	ctx.On(ProxyAfterUpstream, func(payload any) (any, error) {
		ap := payload.(*AfterUpstreamPayload)
		// 模拟 field-filter 剔除 usage 字段
		ap.Response.Body = []byte(`{"choices":[{"message":{"content":"hi"}}]}`)
		return ap, nil
	})
	ctx.On(ProxyUpstreamSucceeded, func(payload any) (any, error) {
		if sp, ok := payload.(*ProxySuccessPayload); ok {
			gotUsage = sp.Usage
		}
		return payload, nil
	})

	rr := doProxy(t, svc, "POST", "chat/completions", "", `{"model":"m","messages":[]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("状态码 = %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "hi") {
		t.Fatalf("客户端响应异常: %s", rr.Body.String())
	}
	if gotUsage.PromptTokens != 5 || gotUsage.CompletionTokens != 3 {
		t.Fatalf("usage 计量应为原始响应值（输出 hook 剔除 usage 后仍应保留），实际 %+v", gotUsage)
	}
}

// TestHandleProxyV2WhitelistPrefixedName v2 白名单存带前缀名（newapi/gpt-4o）时
// 请求 model="newapi/gpt-4o" 应放行（双形态校验，与 /v2/models 输出语义一致）。
func TestHandleProxyV2WhitelistPrefixedName(t *testing.T) {
	svc, _ := newTestService(t)
	echo, _ := newEchoServer(t, `{"id":"resp_v2","object":"response","output":[]}`, 0, nil)
	defer echo.Close()
	writeEchoChannel(t, svc, echo.URL)

	body := `{"model":"newapi/gpt-4o","input":[]}`
	req := httptest.NewRequest("POST", "/v2/responses", strings.NewReader(body))
	// 白名单只存带前缀名。
	ctx := gatewaykeys.ContextWithAPIKey(req.Context(), types.APIKey{Models: []string{"newapi/gpt-4o"}})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	svc.HandleProxyV2(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200（带前缀白名单应放行）: %s", rr.Code, rr.Body.String())
	}
}

// TestHandleProxyV2WhitelistBareName v2 白名单存裸名（gpt-4o）时请求带前缀名也放行。
func TestHandleProxyV2WhitelistBareName(t *testing.T) {
	svc, _ := newTestService(t)
	echo, _ := newEchoServer(t, `{"id":"resp_v2","object":"response","output":[]}`, 0, nil)
	defer echo.Close()
	// 渠道必须带 ChannelName，前缀 newapi/ 才能被拆分命中。
	if err := svc.st.Write(types.FileChannels, []types.Channel{
		{ID: "c1", Name: "Key1", ChannelName: "newapi", BaseURL: echo.URL, Enabled: true},
	}); err != nil {
		t.Fatalf("写渠道表失败: %v", err)
	}

	body := `{"model":"newapi/gpt-4o","input":[]}`
	req := httptest.NewRequest("POST", "/v2/responses", strings.NewReader(body))
	ctx := gatewaykeys.ContextWithAPIKey(req.Context(), types.APIKey{Models: []string{"gpt-4o"}})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	svc.HandleProxyV2(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200（裸名白名单也应放行带前缀请求）: %s", rr.Code, rr.Body.String())
	}
}

// TestHandleProxyV2WhitelistDeny 白名单不含该模型 → 403。
func TestHandleProxyV2WhitelistDeny(t *testing.T) {
	svc, _ := newTestService(t)
	echo, _ := newEchoServer(t, `{}`, 0, nil)
	defer echo.Close()
	writeEchoChannel(t, svc, echo.URL)

	body := `{"model":"newapi/gpt-4o","input":[]}`
	req := httptest.NewRequest("POST", "/v2/responses", strings.NewReader(body))
	ctx := gatewaykeys.ContextWithAPIKey(req.Context(), types.APIKey{Models: []string{"deepseek-chat"}})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	svc.HandleProxyV2(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("状态码 = %d, 期望 403: %s", rr.Code, rr.Body.String())
	}
}

// TestHandleProxyV2HookRebuildKeepsHint hook 完整重建 pipe（非原地修改）后，
// v2 前缀锁定（__channel_hint）必须依然生效——hint 生命周期管理的回归测试。
func TestHandleProxyV2HookRebuildKeepsHint(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := svc.ctx.(*mockCtx)
	echo, getRecords := newEchoServer(t, `{"id":"resp_v2","object":"response","output":[]}`, 0, nil)
	defer echo.Close()
	// newapi 与 other 两个渠道组；hook 重建 pipe 后 hint 应仍锁定 newapi。
	if err := svc.st.Write(types.FileChannels, []types.Channel{
		{ID: "c1", Name: "Key1", ChannelName: "newapi", BaseURL: echo.URL, Enabled: true},
		{ID: "c2", Name: "Key2", ChannelName: "other", BaseURL: echo.URL, Enabled: true},
	}); err != nil {
		t.Fatalf("写渠道表失败: %v", err)
	}

	// hook 完整重建 pipe：新建 ProxyPipeline，仅保留 Request 与 Metadata 的 hint。
	ctx.On(ProxyBeforeUpstream, func(payload any) (any, error) {
		old := payload.(*ProxyPipeline)
		np := &ProxyPipeline{
			RequestID: old.RequestID,
			Request:   old.Request,
			Metadata:  map[string]any{"__route_step": 1},
		}
		return np, nil
	})

	body := `{"model":"newapi/gpt-4o","input":[]}`
	req := httptest.NewRequest("POST", "/v2/responses", strings.NewReader(body))
	rr := httptest.NewRecorder()
	svc.HandleProxyV2(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200（hint 应跨 hook 重建保留）: %s", rr.Code, rr.Body.String())
	}
	recs := getRecords()
	if len(recs) != 1 {
		t.Fatalf("上游收到 %d 个请求, 期望 1", len(recs))
	}
	// body model 必须仍被改写为真实名（证明 hint 锁定与改写链路在 hook 后生效）。
	var sent struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(recs[0].Body, &sent); err != nil {
		t.Fatalf("解析上游 body 失败: %v", err)
	}
	if sent.Model != "gpt-4o" {
		t.Fatalf("上游 model = %q, 期望 gpt-4o", sent.Model)
	}
}

// TestHandleProxyV2QueryRewrite query 参数路径的 model 改写（GET / 非 JSON body）。
func TestHandleProxyV2QueryRewrite(t *testing.T) {
	svc, _ := newTestService(t)
	echo, getRecords := newEchoServer(t, `{"object":"list","data":[]}`, 0, nil)
	defer echo.Close()
	writeEchoChannel(t, svc, echo.URL)

	// 渠道带 ChannelName，前缀 newapi/ 可拆分。
	if err := svc.st.Write(types.FileChannels, []types.Channel{
		{ID: "c1", Name: "Key1", ChannelName: "newapi", BaseURL: echo.URL, Enabled: true},
	}); err != nil {
		t.Fatalf("写渠道表失败: %v", err)
	}
	// GET 请求：body 为空，model 走 query 参数（URL 编码 newapi%2Fgpt-4o）。
	req := httptest.NewRequest("GET", "/v2/models?model=newapi%2Fgpt-4o&limit=5", nil)
	rr := httptest.NewRecorder()
	svc.HandleProxyV2(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200: %s", rr.Code, rr.Body.String())
	}
	recs := getRecords()
	if len(recs) != 1 {
		t.Fatalf("上游收到 %d 个请求, 期望 1", len(recs))
	}
	// url.Values.Encode 按 key 排序，顺序无语义；断言 model 已被改写且其余参数保留。
	if recs[0].Query != "limit=5&model=gpt-4o" {
		t.Fatalf("上游 query = %q, 期望 limit=5&model=gpt-4o（前缀应被改写）", recs[0].Query)
	}
}

// TestUpstreamErrorSummaryNoDoublePrefix 回归「上游返回错误(500): 上游返回错误(500)」
// 重复前缀 bug：upstreamErrorSummary 统一收口拼装状态码与 message，message 为空时只
// 输出「上游返回错误(N)」，绝不再二次拼前缀（旧实现里 upstreamErrorMsg 同时返回
// 「上游返回错误(500)」整串，外层 proxyHandle 又再包一次，导致响应里出现
// 「上游返回错误(500): 上游返回错误(500)」——见用户截图复现）。
func TestUpstreamErrorSummaryNoDoublePrefix(t *testing.T) {
	cases := []struct {
		name   string
		status int
		msg    string
		want   string
	}{
		{"JSON 有 message 时仅拼一次", 500, "boom", "上游返回错误(500): boom"},
		{"JSON 无 message 时只输出状态码", 500, "", "上游返回错误(500)"},
		{"message 含前后空白时去除", 500, "  boom  ", "上游返回错误(500): boom"},
		{"4xx 同样不再加前缀", 404, "", "上游返回错误(404)"},
		{"4xx 有 message 不重复", 429, "rate-limited", "上游返回错误(429): rate-limited"},
		{"上游已带同格式前缀时不重复", 400, "上游返回错误(400): json: unknown field \"client_metadata\"", "上游返回错误(400): json: unknown field \"client_metadata\""},
		{"上游已带前缀即使状态码不同也不重包", 502, "上游返回错误(404): not found", "上游返回错误(404): not found"},
		{"多级嵌套前缀不再叠加", 502, "上游返回错误(500): 上游返回错误(400): boom", "上游返回错误(500): 上游返回错误(400): boom"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := upstreamErrorSummary(tc.status, tc.msg)
			if got != tc.want {
				t.Fatalf("upstreamErrorSummary(%d, %q) = %q, 期望 %q", tc.status, tc.msg, got, tc.want)
			}
			// 强约束：禁止出现「上游返回错误(N): 上游返回错误(N)」连续重复前缀。
			if strings.Contains(got, "上游返回错误("+itoa(tc.status)+"): 上游返回错误(") {
				t.Fatalf("检测到重复前缀: %q", got)
			}
		})
	}
}

// TestUpstreamErrorMsgReturnsPureMessage upstreamErrorMsg 只返回 message 字段纯字符串，
// 前缀（"上游返回错误(N):"）由 upstreamErrorSummary 统一加；这是修复重复前缀 bug 的关键。
func TestUpstreamErrorMsgReturnsPureMessage(t *testing.T) {
	cases := []struct {
		name string
		body []byte
		want string
	}{
		{"合法 JSON 含 error.message", []byte(`{"error":{"message":"boom","type":"x"}}`), "boom"},
		{"error.message 为空串", []byte(`{"error":{"message":"","type":"x"}}`), ""},
		{"error 字段缺失", []byte(`{"foo":"bar"}`), ""},
		{"非 JSON 错误体", []byte(`<html>500 Internal</html>`), ""},
		{"空 body", []byte(``), ""},
		{"message 含前后空白", []byte(`{"error":{"message":"  hi  "}}`), "hi"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := upstreamErrorMsg(tc.body, 500)
			if got != tc.want {
				t.Fatalf("upstreamErrorMsg(%q) = %q, 期望 %q", tc.body, got, tc.want)
			}
			// 任何情况下返回值都不应包含「上游返回错误」字样（那是 summary 的事）。
			if strings.Contains(got, "上游返回错误") {
				t.Fatalf("upstreamErrorMsg 返回值不应包含上游返回错误前缀，实际 %q", got)
			}
		})
	}
}

// TestOpenAIBaseURLNoAutoV1 回归「自动补 /v1」逻辑错误：base_url 完全按用户配置
// 原样使用（只去末尾斜杠），不再自动补版本段——很多模型的基础 URL 不是 v1 结尾，
// 自动补全会导致 /v1/v1/xxx 类 404。需要 /v1 前缀时由用户自行写在 base_url 里。
func TestOpenAIBaseURLNoAutoV1(t *testing.T) {
	cases := []struct {
		name string
		base string
		want string
	}{
		{"无版本段原样返回", "https://api.example.com", "https://api.example.com"},
		{"用户自含 /v1 保留", "https://api.example.com/v1", "https://api.example.com/v1"},
		{"末尾斜杠去除", "https://api.example.com/api/v2/", "https://api.example.com/api/v2"},
		{"带端口", "http://127.0.0.1:3001", "http://127.0.0.1:3001"},
		{"空串", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := openAIBaseURL(tc.base); got != tc.want {
				t.Fatalf("openAIBaseURL(%q) = %q, 期望 %q", tc.base, got, tc.want)
			}
		})
	}
}

// TestProxyLogsUpstreamErrorDetails 上游返回 5xx/4xx 时 slog 必须记录完整诊断信息
// （request_id、channel_id/group、upstream URL、status、response_body 等）。
// 此前 proxyAttemptLog 只写 DB，slog 文件日志完全缺失——用户翻 loadout.log 看不到任何
// 上游 body，无法定位 500/502 的根因。本测试捕获 slog 输出，强约束所有关键字段出现。
func TestProxyLogsUpstreamErrorDetails(t *testing.T) {
	// 截获 slog：用一个带 bytes.Buffer 的 JSONHandler 替换 s.lg。
	var buf bytes.Buffer
	captured := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	prev := slog.Default()
	slog.SetDefault(captured)
	t.Cleanup(func() { slog.SetDefault(prev) })

	svc, st := newTestService(t)
	upstreamErrBody := `{"error":{"message":"rate-limited","type":"rate_limit_error"}}`
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(upstreamErrBody))
	}))
	defer bad.Close()
	if err := st.Write(types.FileChannels, []types.Channel{
		{ID: "ch-err", Name: "err-key", ChannelName: "workbuddy", BaseURL: bad.URL, Enabled: true},
	}); err != nil {
		t.Fatalf("写渠道表失败: %v", err)
	}
	svc.lg = captured

	body := `{"model":"deepseek-v4-flash","messages":[]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("X-Request-Id", "test-req-id-001")
	rr := httptest.NewRecorder()
	svc.HandleProxy(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("响应状态 = %d, 期望透传 500", rr.Code)
	}
	// 响应体不应再含「上游返回错误(500): 上游返回错误(500)」连续重复前缀。
	if strings.Contains(rr.Body.String(), "上游返回错误(500): 上游返回错误(500)") {
		t.Fatalf("响应体出现重复前缀 bug: %s", rr.Body.String())
	}

	logs := buf.String()
	// openAIBaseURL 不再自动补 /v1，base_url 原样使用，
	// 所以 upstream 字段形如 http://127.0.0.1:PORT/chat/completions。
	expectedUpstream := bad.URL + "/chat/completions"
	mustContain := []string{
		`"msg":"upstream returned error"`,
		`"request_id":"test-req-id-001"`,
		`"model":"deepseek-v4-flash"`,
		`"channel_id":"ch-err"`,
		`"channel_name":"err-key"`,
		`"channel_group":"workbuddy"`,
		`"upstream":"` + expectedUpstream + `"`,
		`"status":500`,
		`"error_message":"rate-limited"`,
		"rate-limited", // 出现在 response_body 中
		`"duration_ms":`,
	}
	for _, want := range mustContain {
		if !strings.Contains(logs, want) {
			t.Fatalf("slog 缺关键字段 %q\n完整 slog 输出:\n%s", want, logs)
		}
	}

	// 也应出现 all-channels-failed 汇总。
	if !strings.Contains(logs, `"msg":"all channels failed for model"`) {
		t.Fatalf("slog 缺汇总日志 msg=all channels failed for model\n%s", logs)
	}
	// summary 必须包含 last_channel_id / last_status / last_error 等关键诊断字段。
	mustContainSummary := []string{
		`"last_channel_id":"ch-err"`,
		`"last_channel_name":"err-key"`,
		`"last_channel_group":"workbuddy"`,
		`"last_status":500`,
		`"last_error":"上游返回错误(500): rate-limited"`, // 修 bug 后只拼一次前缀
	}
	for _, want := range mustContainSummary {
		if !strings.Contains(logs, want) {
			t.Fatalf("summary slog 缺关键字段 %q\n完整 slog 输出:\n%s", want, logs)
		}
	}
	// 强约束：slog/响应里都不应有「上游返回错误(500): 上游返回错误(500)」连续重复。
	if strings.Contains(logs, "上游返回错误(500): 上游返回错误(500)") {
		t.Fatalf("slog 出现重复前缀 bug:\n%s", logs)
	}
}

// itoa 私有实现，避免 strconv 依赖，方便测试中复用。
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
