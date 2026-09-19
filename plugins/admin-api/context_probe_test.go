package adminapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestFetchChannelModelDetailsContext 上游 /v1/models 返回上下文时能正确解析。
func TestFetchChannelModelDetailsContext(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[
		  {"id":"ctx-model","context_length":1000000},
		  {"id":"window-model","context_window":128000},
		  {"id":"vllm-model","max_model_len":32768},
		  {"id":"meta-model","meta":{"n_ctx":8192}},
		  {"id":"nocontext"}
		]}`)
	}))
	t.Cleanup(upstream.Close)

	got, err := fetchChannelModelDetails(context.Background(), upstream.URL, "sk-x", 8*time.Second)
	if err != nil {
		t.Fatalf("fetchChannelModelDetails: %v", err)
	}
	want := map[string]int64{
		"ctx-model":    1000000,
		"window-model": 128000,
		"vllm-model":   32768,
		"meta-model":   8192,
		"nocontext":    0,
	}
	if len(got) != len(want) {
		t.Fatalf("模型数 = %d, want %d: %+v", len(got), len(want), got)
	}
	for _, m := range got {
		if m.Context != want[m.Model] {
			t.Errorf("模型 %s context = %d, want %d", m.Model, m.Context, want[m.Model])
		}
	}
}

// TestFetchChannelModelDetailsStringForm 兼容字符串数组形式（无上下文）。
func TestFetchChannelModelDetailsStringForm(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":["a","b"]}`)
	}))
	t.Cleanup(upstream.Close)

	got, err := fetchChannelModelDetails(context.Background(), upstream.URL, "", 8*time.Second)
	if err != nil {
		t.Fatalf("fetchChannelModelDetails: %v", err)
	}
	if len(got) != 2 || got[0].Model != "a" || got[0].Context != 0 || got[1].Model != "b" {
		t.Fatalf("字符串数组解析错误: %+v", got)
	}
}

// TestParseLeadingInt64 前导整数解析。
func TestParseLeadingInt64(t *testing.T) {
	cases := map[string]int64{
		"131072":      131072,
		"128000 (输入)": 128000,
		"":            0,
		"abc":         0,
		"-5":          0,
		"0":           0,
	}
	for in, want := range cases {
		if got := parseLeadingInt64(in); got != want {
			t.Errorf("parseLeadingInt64(%q) = %d, want %d", in, got, want)
		}
	}
}
