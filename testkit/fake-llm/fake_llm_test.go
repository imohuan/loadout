package fakellm

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// postJSON 向 url 发起一个携带指定 JSON 请求体的 POST 请求，返回响应与读取到的响应体。
func postJSON(t *testing.T, url, body string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return resp, string(data)
}

// TestRecordsChatRequest 验证普通请求会被正确解析并记录 model/messages/stream。
func TestRecordsChatRequest(t *testing.T) {
	f, base := New()
	defer f.Close()

	reqBody := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"你好"},{"role":"assistant","content":"hi"}],"stream":false}`
	resp, _ := postJSON(t, base+"/v1/chat/completions", reqBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	reqs := f.Requests()
	if len(reqs) != 1 {
		t.Fatalf("len(Requests()) = %d, want 1", len(reqs))
	}
	got := reqs[0]
	if got.Model != "gpt-4o-mini" {
		t.Errorf("Model = %q, want gpt-4o-mini", got.Model)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("len(Messages) = %d, want 2", len(got.Messages))
	}
	if got.Messages[0]["role"] != "user" || got.Messages[0]["content"] != "你好" {
		t.Errorf("Messages[0] = %v, want user/你好", got.Messages[0])
	}
	if got.Stream {
		t.Errorf("Stream = true, want false")
	}
	if string(got.Raw) != reqBody {
		t.Errorf("Raw = %q, want %q", got.Raw, reqBody)
	}
}

// TestSSEScript 验证 stream 请求会按脚本回放 SSE 片段。
func TestSSEScript(t *testing.T) {
	f, base := New()
	defer f.Close()

	f.SetSSEScript([]string{
		"data: {\"delta\":{\"content\":\"你\"}}\n\n",
		"data: {\"delta\":{\"content\":\"好\"}}\n\n",
	})

	reqBody := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}],"stream":true}`
	resp, body := postJSON(t, base+"/v1/chat/completions", reqBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	if !strings.Contains(body, "data: {\"delta\":{\"content\":\"你\"}}") {
		t.Errorf("body %q missing first script fragment", body)
	}
	if !strings.Contains(body, "data: {\"delta\":{\"content\":\"好\"}}") {
		t.Errorf("body %q missing second script fragment", body)
	}

	reqs := f.Requests()
	if len(reqs) != 1 || !reqs[0].Stream {
		t.Fatalf("stream request not recorded: %+v", reqs)
	}
}

// TestSetError 验证设置错误后返回指定状态码与响应体。
func TestSetError(t *testing.T) {
	f, base := New()
	defer f.Close()

	f.SetError(http.StatusBadGateway, `{"error":{"message":"upstream down"}}`)

	reqBody := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`
	resp, body := postJSON(t, base+"/v1/chat/completions", reqBody)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadGateway)
	}
	if body != `{"error":{"message":"upstream down"}}` {
		t.Errorf("body = %q, want error body", body)
	}
}

// TestDefaultResponseIsJSON 验证未配置时的默认响应是可解析的 JSON。
func TestDefaultResponseIsJSON(t *testing.T) {
	f, base := New()
	defer f.Close()

	reqBody := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`
	resp, body := postJSON(t, base+"/v1/chat/completions", reqBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var parsed struct {
		ID      string `json:"id"`
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("default response is not valid JSON: %v", err)
	}
	if parsed.ID == "" || len(parsed.Choices) == 0 {
		t.Errorf("default response missing id/choices: %s", body)
	}
}

// TestSetResponsePassthrough 验证 SetResponse 的正文被原文透传。
func TestSetResponsePassthrough(t *testing.T) {
	f, base := New()
	defer f.Close()

	custom := `{"id":"custom-1","choices":[{"message":{"role":"assistant","content":"hello"}}]}`
	f.SetResponse(custom)

	reqBody := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`
	resp, body := postJSON(t, base+"/v1/chat/completions", reqBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if body != custom {
		t.Errorf("body = %q, want %q", body, custom)
	}
}
