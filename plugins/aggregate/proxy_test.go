package aggregate

import (
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// newProxyTestService 构造聚合服务 + 含聚合模型配置的 store。
func newProxyTestService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("创建 store 失败: %v", err)
	}
	if err := st.Write(types.FileAggregates, []types.AggregateModel{
		{Name: "auto", Targets: []types.AggregateTarget{
			{Model: "gpt-4", ChannelID: "ch-openai"},
			{Model: "claude-3", ChannelID: "ch-anthropic"},
		}},
	}); err != nil {
		t.Fatalf("写聚合配置失败: %v", err)
	}
	return NewService(st, slog.Default(), nil), st
}

// TestHandleProxyBeforeUpstream 聚合模型被改写为第一个目标模型，body 同步更新。
func TestHandleProxyBeforeUpstream(t *testing.T) {
	svc, _ := newProxyTestService(t)
	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}],"extra":"keep"}`
	pipe := &modelgateway.ProxyPipeline{
		Request: &modelgateway.ProxyRequest{Path: "chat/completions", Model: "auto", Body: []byte(body)},
	}

	out, err := svc.HandleProxyBeforeUpstream(pipe)
	if err != nil {
		t.Fatalf("处理出错: %v", err)
	}
	got := out.(*modelgateway.ProxyPipeline)
	if got.Request.Model != "gpt-4" {
		t.Fatalf("model 应为 gpt-4, 实际 %s", got.Request.Model)
	}
	if got.Metadata["__current_channel"] != "ch-openai" {
		t.Fatalf("渠道应为 ch-openai, 实际 %v", got.Metadata["__current_channel"])
	}
	if got.Metadata["__virtual_model"] != "auto" {
		t.Fatalf("应记录虚拟模型 auto")
	}
	var parsed map[string]any
	if err := json.Unmarshal(got.Request.Body, &parsed); err != nil {
		t.Fatalf("改写后 body 不是合法 JSON: %v", err)
	}
	if parsed["model"] != "gpt-4" {
		t.Fatalf("body 内 model 应为 gpt-4, 实际 %v", parsed["model"])
	}
	if parsed["extra"] != "keep" {
		t.Fatalf("body 其他字段应保留: %v", parsed["extra"])
	}
}

// TestHandleProxyBeforeUpstreamNonAggregate 非聚合模型原样返回。
func TestHandleProxyBeforeUpstreamNonAggregate(t *testing.T) {
	svc, _ := newProxyTestService(t)
	body := `{"model":"gpt-4o","messages":[]}`
	pipe := &modelgateway.ProxyPipeline{
		Request: &modelgateway.ProxyRequest{Model: "gpt-4o", Body: []byte(body)},
	}
	out, err := svc.HandleProxyBeforeUpstream(pipe)
	if err != nil {
		t.Fatalf("非聚合模型不应报错: %v", err)
	}
	if out.(*modelgateway.ProxyPipeline).Request.Model != "gpt-4o" {
		t.Fatalf("非聚合模型不应被改写")
	}
}

// TestHandleProxyUpstreamFailed 首目标失败后切换到第二个目标。
func TestHandleProxyUpstreamFailed(t *testing.T) {
	svc, _ := newProxyTestService(t)
	pipe := &modelgateway.ProxyPipeline{
		Request: &modelgateway.ProxyRequest{Model: "gpt-4", Body: []byte(`{"model":"gpt-4","messages":[]}`)},
		Metadata: map[string]any{
			"__virtual_model": "auto",
			"__aggregate_targets": []types.AggregateTarget{
				{Model: "gpt-4", ChannelID: "ch-openai"},
				{Model: "claude-3", ChannelID: "ch-anthropic"},
			},
			"__failed_targets": []string{},
			"__retry_count":    0,
		},
	}
	failure := &modelgateway.ProxyFailurePayload{
		Pipe: pipe, Model: "gpt-4", ChannelID: "ch-openai",
		StatusCode: 429, ErrorBody: `{"error":{"message":"rate limited"}}`,
	}

	out, err := svc.HandleProxyUpstreamFailed(failure)
	if err != nil {
		t.Fatalf("切换出错: %v", err)
	}
	retry, ok := out.(*modelgateway.ProxyRetry)
	if !ok || retry.Pipe == nil {
		t.Fatalf("应返回 ProxyRetry, 实际 %T", out)
	}
	if retry.Pipe.Request.Model != "claude-3" {
		t.Fatalf("应切换到 claude-3, 实际 %s", retry.Pipe.Request.Model)
	}
	if !strings.Contains(string(retry.Pipe.Request.Body), `"model":"claude-3"`) {
		t.Fatalf("body 内 model 应同步切换: %s", retry.Pipe.Request.Body)
	}
	failed, _ := retry.Pipe.Metadata["__failed_targets"].([]string)
	if len(failed) != 1 || failed[0] != "gpt-4@ch-openai" {
		t.Fatalf("应记录失败目标: %v", failed)
	}
}

// TestHandleProxyUpstreamFailedAll 所有目标失败时返回错误。
func TestHandleProxyUpstreamFailedAll(t *testing.T) {
	svc, _ := newProxyTestService(t)
	pipe := &modelgateway.ProxyPipeline{
		Request: &modelgateway.ProxyRequest{Model: "gpt-4", Body: []byte(`{"model":"gpt-4"}`)},
		Metadata: map[string]any{
			"__virtual_model":     "auto",
			"__aggregate_targets": []types.AggregateTarget{{Model: "gpt-4", ChannelID: "ch-openai"}},
			"__failed_targets":    []string{},
			"__retry_count":       0,
		},
	}
	failure := &modelgateway.ProxyFailurePayload{
		Pipe: pipe, Model: "gpt-4", ChannelID: "ch-openai",
		StatusCode: 401, ErrorBody: `{"error":{"message":"invalid key"}}`,
	}
	_, err := svc.HandleProxyUpstreamFailed(failure)
	if err == nil {
		t.Fatalf("所有目标失败应返回错误")
	}
}
