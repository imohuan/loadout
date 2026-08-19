// Package fakellm 提供一个可编程的 OpenAI 兼容假上游服务器，用于在测试中替代真实的
// OpenAI /chat/completions 接口。它基于 httptest.Server 实现，支持记录请求、
// 回放非流式 JSON、回放 SSE 流式脚本以及返回错误状态码，且所有方法均线程安全。
package fakellm

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
)

// ChatRequest 记录一次 /v1/chat/completions 请求（脱敏后的关键字段）。
type ChatRequest struct {
	Model    string           // 请求中的模型名
	Messages []map[string]any // 完整 messages 列表
	Stream   bool             // 是否请求流式输出
	Raw      []byte           // 原始请求体
}

// FakeLLM 可编程 OpenAI 兼容假上游。
type FakeLLM struct {
	mu       sync.Mutex       // 保护以下所有字段的并发访问
	server   *httptest.Server // 底层测试服务器
	requests []ChatRequest    // 按到达顺序记录的请求
	response *string          // 非流式响应体；nil 表示使用默认响应
	sse      []string         // SSE 回放脚本；nil 表示未设置
	errCode  int              // 错误状态码；0 表示未设置错误
	errBody  string           // 错误响应体
}

// defaultResponse 是未调用 SetResponse 时返回的默认非流式响应体。
const defaultResponse = `{"id":"chatcmpl-1","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"ok"}}]}`

// New 创建一个假上游并返回其 httptest server 与 URL。
func New() (*FakeLLM, string) {
	f := &FakeLLM{}
	f.server = httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	return f, f.server.URL
}

// URL 返回 server 基地址（如 http://127.0.0.1:PORT，不含 /v1）。
func (f *FakeLLM) URL() string {
	return f.server.URL
}

// Requests 返回所有收到的 chat 请求（按到达顺序）。返回的是副本，可安全只读。
func (f *FakeLLM) Requests() []ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ChatRequest, len(f.requests))
	copy(out, f.requests)
	return out
}

// SetResponse 设置对后续请求返回的非流式 JSON 响应体（原文透传）。
func (f *FakeLLM) SetResponse(body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b := body
	f.response = &b
}

// SetSSEScript 设置流式 SSE 回放脚本：每个元素是一段要写出的原始字节（如一个 data: 行）。
// 当请求 stream=true 时按顺序写出全部片段后结束。
func (f *FakeLLM) SetSSEScript(events []string) {
	cp := make([]string, len(events))
	copy(cp, events)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sse = cp
}

// SetError 让后续请求返回 HTTP 状态码 code 与 JSON 错误体。
func (f *FakeLLM) SetError(code int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errCode = code
	f.errBody = body
}

// Close 关闭 httptest server。
func (f *FakeLLM) Close() {
	f.server.Close()
}

// serveHTTP 处理所有到达的请求：仅对 POST /v1/chat/completions 记录并回放，
// 其余路径返回 404，非 POST 返回 405。
func (f *FakeLLM) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/chat/completions" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	raw, _ := io.ReadAll(r.Body)
	body := make([]byte, len(raw))
	copy(body, raw)

	var payload struct {
		Model    string           `json:"model"`
		Messages []map[string]any `json:"messages"`
		Stream   bool             `json:"stream"`
	}
	// 解析失败时仅记录原始字节，解析出的字段保留零值。
	_ = json.Unmarshal(raw, &payload)

	req := ChatRequest{
		Model:    payload.Model,
		Messages: payload.Messages,
		Stream:   payload.Stream,
		Raw:      body,
	}

	// 在锁内记录请求并读取一份配置快照，避免持锁进行响应写入。
	f.mu.Lock()
	f.requests = append(f.requests, req)
	errCode, errBody := f.errCode, f.errBody
	sse := f.sse
	resp := f.response
	f.mu.Unlock()

	if errCode != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(errCode)
		_, _ = io.WriteString(w, errBody)
		return
	}

	if payload.Stream && sse != nil {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, ev := range sse {
			_, _ = io.WriteString(w, ev)
			if flusher != nil {
				flusher.Flush()
			}
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if resp != nil {
		_, _ = io.WriteString(w, *resp)
		return
	}
	_, _ = io.WriteString(w, defaultResponse)
}
