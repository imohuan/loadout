package modelgateway

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// syntheticSSEResponse 构造一个 fake 上游 SSE 响应（不真正发请求），供 readBufferedSSE 单测。
func syntheticSSEResponse(t *testing.T, lines []string) *http.Response {
	t.Helper()
	body := strings.Join(lines, "")
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// ssePipe 构造带未取消 ctx 的管线，供 readBufferedSSE 测试。
func ssePipe(t *testing.T, model string) *ProxyPipeline {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx, cancel := context.WithCancel(req.Context())
	t.Cleanup(cancel)
	return &ProxyPipeline{
		Request:     &ProxyRequest{Model: model, Path: "chat/completions"},
		HTTPRequest: req.WithContext(ctx),
		Metadata:    map[string]any{},
	}
}

// TestReadBufferedSSE_Basic 纯文本累积 → 完整 chat.completion JSON。
func TestReadBufferedSSE_Basic(t *testing.T) {
	svc := NewService(nil, slog.Default(), newMockCtx())
	pipe := ssePipe(t, "deepseek-chat")
	lines := []string{
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1710000000,\"model\":\"deepseek-chat\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n",
		"data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"你\"},\"finish_reason\":null}]}\n",
		"data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"好\"},\"finish_reason\":null}]}\n",
		"data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n",
		"data: {\"id\":\"chatcmpl-1\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n",
		"data: [DONE]\n",
	}
	resp := syntheticSSEResponse(t, lines)
	body, usage, err := svc.readBufferedSSE(resp, pipe)
	if err != nil {
		t.Fatalf("readBufferedSSE 出错: %v", err)
	}
	if usage.PromptTokens != 10 || usage.CompletionTokens != 2 || usage.TotalTokens != 12 {
		t.Fatalf("usage 提取错误: %+v", usage)
	}
	var out struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		Model   string `json:"model"`
		Choices []struct {
			Index   int `json:"index"`
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("拼装结果非法 JSON: %v\n%s", err, body)
	}
	if out.Object != "chat.completion" {
		t.Fatalf("object = %q, 期望 chat.completion", out.Object)
	}
	if out.Created != 1710000000 {
		t.Fatalf("created = %d, 期望 1710000000", out.Created)
	}
	if len(out.Choices) != 1 {
		t.Fatalf("choices 长度 = %d, 期望 1", len(out.Choices))
	}
	c := out.Choices[0]
	if c.Message.Content != "你好" {
		t.Fatalf("content = %q, 期望 你好", c.Message.Content)
	}
	if c.Message.Role != "assistant" {
		t.Fatalf("role = %q, 期望 assistant", c.Message.Role)
	}
	if c.FinishReason != "stop" {
		t.Fatalf("finish_reason = %q, 期望 stop", c.FinishReason)
	}
}

// TestReadBufferedSSE_ToolCalls tool_calls 跨块增量拼接（name 首块、arguments 分块 append）。
func TestReadBufferedSSE_ToolCalls(t *testing.T) {
	svc := NewService(nil, slog.Default(), newMockCtx())
	pipe := ssePipe(t, "deepseek-chat")
	lines := []string{
		"data: {\"id\":\"t1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"get_weather\",\"arguments\":\"\"}}]},\"finish_reason\":null}]}\n",
		"data: {\"id\":\"t1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"city\\\":\\\"北京\"}}]},\"finish_reason\":null}]}\n",
		"data: {\"id\":\"t1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"}\"}}]},\"finish_reason\":null}]}\n",
		"data: {\"id\":\"t1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n",
		"data: [DONE]\n",
	}
	body, _, err := svc.readBufferedSSE(syntheticSSEResponse(t, lines), pipe)
	if err != nil {
		t.Fatalf("readBufferedSSE 出错: %v", err)
	}
	var out struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("非法 JSON: %v\n%s", err, body)
	}
	if len(out.Choices) != 1 {
		t.Fatalf("choices 长度 = %d, 期望 1", len(out.Choices))
	}
	tcs := out.Choices[0].Message.ToolCalls
	if len(tcs) != 1 {
		t.Fatalf("tool_calls 长度 = %d, 期望 1", len(tcs))
	}
	tc := tcs[0]
	if tc.ID != "call_1" || tc.Type != "function" || tc.Function.Name != "get_weather" {
		t.Fatalf("tool_call 头信息错误: %+v", tc)
	}
	wantArgs := `{"city":"北京"}`
	if tc.Function.Arguments != wantArgs {
		t.Fatalf("arguments = %q, 期望 %q（跨块增量拼接错误）", tc.Function.Arguments, wantArgs)
	}
	if out.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("finish_reason = %q, 期望 tool_calls", out.Choices[0].FinishReason)
	}
}

// TestReadBufferedSSE_Reasoning reasoning_content 累积。
func TestReadBufferedSSE_Reasoning(t *testing.T) {
	svc := NewService(nil, slog.Default(), newMockCtx())
	pipe := ssePipe(t, "deepseek-chat")
	lines := []string{
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"想\"},\"finish_reason\":null}]}\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"一想\"},\"finish_reason\":null}]}\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"答案\"},\"finish_reason\":\"stop\"}]}\n",
		"data: [DONE]\n",
	}
	body, _, err := svc.readBufferedSSE(syntheticSSEResponse(t, lines), pipe)
	if err != nil {
		t.Fatalf("readBufferedSSE 出错: %v", err)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Reasoning string `json:"reasoning_content"`
				Content   string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("非法 JSON: %v", err)
	}
	if len(out.Choices) != 1 || out.Choices[0].Message.Reasoning != "想一想" {
		t.Fatalf("reasoning 拼接错误: %+v", out.Choices)
	}
	if out.Choices[0].Message.Content != "答案" {
		t.Fatalf("content = %q, 期望 答案", out.Choices[0].Message.Content)
	}
}

// TestReadBufferedSSE_MultiChoice 多 choice 各自独立累积。
func TestReadBufferedSSE_MultiChoice(t *testing.T) {
	svc := NewService(nil, slog.Default(), newMockCtx())
	pipe := ssePipe(t, "deepseek-chat")
	lines := []string{
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"A\"},\"finish_reason\":null},{\"index\":1,\"delta\":{\"content\":\"B\"},\"finish_reason\":null}]}\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"A2\"},\"finish_reason\":\"stop\"},{\"index\":1,\"delta\":{\"content\":\"B2\"},\"finish_reason\":\"stop\"}]}\n",
		"data: [DONE]\n",
	}
	body, _, err := svc.readBufferedSSE(syntheticSSEResponse(t, lines), pipe)
	if err != nil {
		t.Fatalf("readBufferedSSE 出错: %v", err)
	}
	var out struct {
		Choices []struct {
			Index   int `json:"index"`
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("非法 JSON: %v", err)
	}
	if len(out.Choices) != 2 {
		t.Fatalf("choices 长度 = %d, 期望 2", len(out.Choices))
	}
	if out.Choices[0].Index != 0 || out.Choices[0].Message.Content != "AA2" {
		t.Fatalf("choice0 拼接错误: %+v", out.Choices[0])
	}
	if out.Choices[1].Index != 1 || out.Choices[1].Message.Content != "BB2" {
		t.Fatalf("choice1 拼接错误: %+v", out.Choices[1])
	}
}

// TestReadBufferedSSE_InterruptedBeforeDone 上游在 [DONE] 前 EOF → 返回 error（不吐半包）。
func TestReadBufferedSSE_InterruptedBeforeDone(t *testing.T) {
	svc := NewService(nil, slog.Default(), newMockCtx())
	pipe := ssePipe(t, "deepseek-chat")
	lines := []string{
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"一半\"},\"finish_reason\":null}]}\n",
		// 没有 [DONE]，body 直接结束
	}
	_, _, err := svc.readBufferedSSE(syntheticSSEResponse(t, lines), pipe)
	if err == nil {
		t.Fatalf("应返回错误（上游在 [DONE] 前中断），实际无错误")
	}
	if !strings.Contains(err.Error(), "[DONE]") {
		t.Fatalf("错误信息应说明在 [DONE] 前中断: %v", err)
	}
}

// TestReadBufferedSSE_Empty 上游空流 → 返回错误。
func TestReadBufferedSSE_Empty(t *testing.T) {
	svc := NewService(nil, slog.Default(), newMockCtx())
	pipe := ssePipe(t, "deepseek-chat")
	_, _, err := svc.readBufferedSSE(syntheticSSEResponse(t, nil), pipe)
	if err == nil {
		t.Fatalf("空流应返回错误，实际无错误")
	}
}

// TestReadBufferedSSE_NoUsage 无 usage 块 → usage 零值且响应无 usage 字段。
func TestReadBufferedSSE_NoUsage(t *testing.T) {
	svc := NewService(nil, slog.Default(), newMockCtx())
	pipe := ssePipe(t, "m")
	lines := []string{
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n",
		"data: [DONE]\n",
	}
	body, usage, err := svc.readBufferedSSE(syntheticSSEResponse(t, lines), pipe)
	if err != nil {
		t.Fatalf("readBufferedSSE 出错: %v", err)
	}
	if usage.PromptTokens != 0 || usage.CompletionTokens != 0 {
		t.Fatalf("无 usage 时应为零值: %+v", usage)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("非法 JSON: %v", err)
	}
	if _, ok := out["usage"]; ok {
		t.Fatalf("无 usage 时响应不应含 usage 字段")
	}
	if out["model"] != "m" {
		t.Fatalf("model 应为 pipe.Request.Model: %v", out["model"])
	}
}
