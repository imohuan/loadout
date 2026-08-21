package modelgateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"loadout/plugins/types"
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
	if rec.Path != "/v1/responses" {
		t.Fatalf("上游路径 = %s, 期望 /v1/responses", rec.Path)
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
	if len(recs) != 1 || recs[0].Method != "GET" || recs[0].Path != "/v1/files" {
		t.Fatalf("GET /v1/files 未原样转发: %+v", recs)
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
