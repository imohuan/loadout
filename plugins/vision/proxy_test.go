package vision

import (
	"encoding/json"
	"strings"
	"testing"

	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
	fakellm "loadout/testkit/fake-llm"
)

// proxyPipe 构造一个 ProxyPipeline 请求。
func proxyPipe(path, model, body string) *modelgateway.ProxyPipeline {
	return &modelgateway.ProxyPipeline{
		Request: &modelgateway.ProxyRequest{Path: path, Model: model, Body: []byte(body)},
	}
}

// TestDetectProxyImagesChat chat/completions 格式图片检测（image_url 对象与字符串）。
func TestDetectProxyImagesChat(t *testing.T) {
	svc, _ := newTestService(t)
	body := `{"model":"gpt-4o","messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"http://img/a.png"}}]},{"role":"user","content":[{"type":"image_url","image_url":"data:image/png;base64,iVBORw0KGgo="}]}]}`

	out, err := svc.HandleProxyBeforeUpstream(proxyPipe("chat/completions", "gpt-4o", body))
	if err != nil {
		t.Fatalf("未配置路由时应原样返回, 实际出错: %v", err)
	}
	pipe := out.(*modelgateway.ProxyPipeline)
	if string(pipe.Request.Body) != body {
		t.Fatalf("native 模型不应改写 body")
	}

	// 直接测检测函数（不依赖路由）。
	var m map[string]any
	_ = json.Unmarshal([]byte(body), &m)
	msgs := proxyMessageArray(m, formatChat)
	images, _ := detectProxyImages(msgs, formatChat)
	if len(images) != 2 {
		t.Fatalf("应检出 2 张图, 实际 %d: %+v", len(images), images)
	}
	if images[0] != "http://img/a.png" {
		t.Fatalf("第一张图 URL 不符: %s", images[0])
	}
	if !strings.HasPrefix(images[1], "data:image/png;base64,") {
		t.Fatalf("第二张图应为 data URI: %s", images[1])
	}
}

// TestDetectProxyImagesClaude claude messages 格式图片检测（base64 转 data URI）。
func TestDetectProxyImagesClaude(t *testing.T) {
	svc, _ := newTestService(t)
	body := `{"model":"claude-sonnet","messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"AAAA"}}]}]}`

	out, err := svc.HandleProxyBeforeUpstream(proxyPipe("messages", "claude-sonnet", body))
	if err != nil {
		t.Fatalf("未配置路由时应原样返回, 实际出错: %v", err)
	}
	pipe := out.(*modelgateway.ProxyPipeline)
	if string(pipe.Request.Body) != body {
		t.Fatalf("native 模型不应改写 body")
	}

	var m map[string]any
	_ = json.Unmarshal([]byte(body), &m)
	msgs := proxyMessageArray(m, formatClaude)
	images, _ := detectProxyImages(msgs, formatClaude)
	if len(images) != 1 {
		t.Fatalf("应检出 1 张图, 实际 %d", len(images))
	}
	if images[0] != "data:image/jpeg;base64,AAAA" {
		t.Fatalf("base64 应转 data URI: %s", images[0])
	}
}

// TestDetectProxyImagesResponses responses 格式图片检测（input 数组 + input_image）。
func TestDetectProxyImagesResponses(t *testing.T) {
	svc, _ := newTestService(t)
	body := `{"model":"gpt-5","input":[{"role":"user","content":[{"type":"input_text","text":"hi"},{"type":"input_image","image_url":"http://img/b.png"}]}]}`

	out, err := svc.HandleProxyBeforeUpstream(proxyPipe("responses", "gpt-5", body))
	if err != nil {
		t.Fatalf("未配置路由时应原样返回, 实际出错: %v", err)
	}
	pipe := out.(*modelgateway.ProxyPipeline)
	if string(pipe.Request.Body) != body {
		t.Fatalf("native 模型不应改写 body")
	}

	var m map[string]any
	_ = json.Unmarshal([]byte(body), &m)
	msgs := proxyMessageArray(m, formatResponses)
	images, _ := detectProxyImages(msgs, formatResponses)
	if len(images) != 1 || images[0] != "http://img/b.png" {
		t.Fatalf("responses 图片检测失败: %+v", images)
	}
}

// TestHandleProxyBeforeUpstreamChat chat/completions 集成：图片被替换为视觉描述。
func TestHandleProxyBeforeUpstreamChat(t *testing.T) {
	svc, st := newTestService(t)
	fake, url := fakellm.New()
	defer fake.Close()
	fake.SetResponse(`{"choices":[{"message":{"role":"assistant","content":"这是一张猫的图片"}}]}`)
	seedChannels(t, svc, []types.Channel{
		{ID: "v", Name: "视觉", BaseURL: url + "/v1", Enabled: true, Models: []string{"qwen-vl-max"}},
	})
	if err := st.Write(types.FileCapabilityRoutes, []types.CapabilityRoute{
		{Models: []string{"deepseek-chat"}, Capability: "vision", Route: types.RouteProxy, ViaOptions: []types.ViaOption{{ViaModel: "qwen-vl-max"}}},
	}); err != nil {
		t.Fatalf("写能力路由失败: %v", err)
	}

	body := `{"model":"deepseek-chat","messages":[{"role":"user","content":[{"type":"text","text":"看"},{"type":"image_url","image_url":{"url":"http://img/a.png"}}]}]}`
	out, err := svc.HandleProxyBeforeUpstream(proxyPipe("chat/completions", "deepseek-chat", body))
	if err != nil {
		t.Fatalf("视觉处理出错: %v", err)
	}
	got := string(out.(*modelgateway.ProxyPipeline).Request.Body)
	if strings.Contains(got, "image_url") {
		t.Fatalf("图片块未被替换: %s", got)
	}
	if !strings.Contains(got, "这是一张猫的图片") {
		t.Fatalf("描述文本未写入: %s", got)
	}
}

// TestHandleProxyBeforeUpstreamChat 多图：只保留一份描述，其余图片块删除。
func TestHandleProxyBeforeUpstreamChatMultipleImages(t *testing.T) {
	svc, st := newTestService(t)
	fake, url := fakellm.New()
	defer fake.Close()
	fake.SetResponse(`{"choices":[{"message":{"role":"assistant","content":"图一图二综合描述"}}]}`)
	seedChannels(t, svc, []types.Channel{
		{ID: "v", Name: "视觉", BaseURL: url + "/v1", Enabled: true, Models: []string{"qwen-vl-max"}},
	})
	if err := st.Write(types.FileCapabilityRoutes, []types.CapabilityRoute{
		{Models: []string{"deepseek-chat"}, Capability: "vision", Route: types.RouteProxy, ViaOptions: []types.ViaOption{{ViaModel: "qwen-vl-max"}}},
	}); err != nil {
		t.Fatalf("写能力路由失败: %v", err)
	}

	body := `{"model":"deepseek-chat","messages":[{"role":"user","content":[{"type":"text","text":"看"},{"type":"image_url","image_url":{"url":"http://img/a.png"}},{"type":"image_url","image_url":{"url":"http://img/b.png"}}]}]}`
	out, err := svc.HandleProxyBeforeUpstream(proxyPipe("chat/completions", "deepseek-chat", body))
	if err != nil {
		t.Fatalf("视觉处理出错: %v", err)
	}
	got := string(out.(*modelgateway.ProxyPipeline).Request.Body)
	// 描述只出现一次（不是两份）。
	if strings.Count(got, "图一图二综合描述") != 1 {
		t.Fatalf("描述应只出现 1 份，实际 %d 份: %s", strings.Count(got, "图一图二综合描述"), got)
	}
	if strings.Contains(got, "image_url") {
		t.Fatalf("图片块应全部处理（1 替换 1 删除）: %s", got)
	}
}
func TestHandleProxyBeforeUpstreamResponses(t *testing.T) {
	svc, st := newTestService(t)
	fake, url := fakellm.New()
	defer fake.Close()
	fake.SetResponse(`{"choices":[{"message":{"role":"assistant","content":"图里是山川"}}]}`)
	seedChannels(t, svc, []types.Channel{
		{ID: "v", Name: "视觉", BaseURL: url + "/v1", Enabled: true, Models: []string{"qwen-vl-max"}},
	})
	if err := st.Write(types.FileCapabilityRoutes, []types.CapabilityRoute{
		{Models: []string{"gpt-5"}, Capability: "vision", Route: types.RouteProxy, ViaOptions: []types.ViaOption{{ViaModel: "qwen-vl-max"}}},
	}); err != nil {
		t.Fatalf("写能力路由失败: %v", err)
	}

	body := `{"model":"gpt-5","input":[{"role":"user","content":[{"type":"input_text","text":"看"},{"type":"input_image","image_url":"http://img/b.png"}]}]}`
	out, err := svc.HandleProxyBeforeUpstream(proxyPipe("responses", "gpt-5", body))
	if err != nil {
		t.Fatalf("视觉处理出错: %v", err)
	}
	got := string(out.(*modelgateway.ProxyPipeline).Request.Body)
	if strings.Contains(got, "input_image") {
		t.Fatalf("图片块未被替换: %s", got)
	}
	if !strings.Contains(got, `"input_text"`) {
		t.Fatalf("responses 格式应替换为 input_text 块: %s", got)
	}
	if !strings.Contains(got, "图里是山川") {
		t.Fatalf("描述文本未写入: %s", got)
	}
}
