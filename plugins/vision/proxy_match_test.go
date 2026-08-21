// 复现用户截图场景：火山渠道上指定 key 命中 deepseek-* 视觉路由。
// 模拟 aggregate.HandleProxyBeforeUpstream 已先写 __current_channel，验证 vision 命中并改写 body。
package vision

import (
	"context"
	"strings"
	"testing"

	"loadout/core/db"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
	fakellm "loadout/testkit/fake-llm"
)

// TestProxyRouteByAggregateCandidates 复现用户日志真实场景：
// 聚合模型 volcengine_auto 选中 Key 多选目标 → aggregate 写 __current_channel="" +
// __channel_candidates=[volcengine key]。视觉路由绑定 volcengine key（channel_ids）。
// 之前只读 __current_channel 导致匹配失败（图片直接发给 deepseek → 400 "Model do not support image input"）。
func TestProxyRouteByAggregateCandidates(t *testing.T) {
	fake, url := fakellm.New()
	defer fake.Close()
	fake.SetResponse(`{"choices":[{"message":{"role":"assistant","content":"图里是猫"}}]}`)

	svc, _ := newTestService(t)

	const volcKeyID = "ada333dbda7499c0" // 与用户日志 candidates 一致
	if err := svc.repo.ReplaceChannels(context.Background(), []db.Channel{
		{
			ID: volcKeyID, Name: "volcengine", ChannelName: "volcengine",
			BaseURL: url + "/v1", ManualEnabled: true,
			Models: []db.ChannelModel{
				{Model: "qwen3-vl-flash-2026-01-22", Enabled: true},
				{Model: "deepseek-v4-pro-ga-260813", Enabled: true},
			},
		},
	}); err != nil {
		t.Fatalf("写渠道失败: %v", err)
	}

	// 视觉路由绑定 volcengine 渠道级（channel_base_urls）——用户数据库里实际存的就是这个形态。
	if err := svc.repo.ReplaceCapabilityRoutes(context.Background(), []types.CapabilityRoute{
		{
			Models:          []string{"deepseek-*"},
			ChannelBaseURLs: []string{url + "/v1"},
			Capability:      "vision", Route: types.RouteProxy,
			ViaOptions: []types.ViaOption{{ViaModel: "qwen3-vl-flash-2026-01-22", ChannelIDs: []string{volcKeyID}}},
		},
	}); err != nil {
		t.Fatalf("写能力路由失败: %v", err)
	}

	body := `{"model":"deepseek-v4-pro-ga-260813","messages":[{"role":"user","content":[{"type":"text","text":"看"},{"type":"image_url","image_url":{"url":"http://img/a.png"}}]}]}`
	pipe := proxyPipe("chat/completions", "deepseek-v4-pro-ga-260813", body)
	// 与用户日志一致的 metadata：__current_channel 为空 + __channel_candidates 候选 key。
	pipe.Metadata = map[string]any{
		"__current_channel":          "",
		"__channel_candidates":       []string{volcKeyID},
		"__current_channel_base_url": "",
	}

	out, err := svc.HandleProxyBeforeUpstream(pipe)
	if err != nil {
		t.Fatalf("聚合候选场景视觉应命中却报错: %v", err)
	}
	rewritten, ok := out.(*modelgateway.ProxyPipeline)
	if !ok {
		t.Fatalf("payload 类型: %T", out)
	}
	if string(rewritten.Request.Body) == body {
		t.Fatalf("聚合候选场景视觉路由未命中（只读 __current_channel 拿到空串），body 原样透传 → 上游 400")
	}
	if strings.Contains(string(rewritten.Request.Body), "image_url") {
		t.Fatalf("vision 应删除 image_url，实际 body=%s", string(rewritten.Request.Body))
	}
	if !strings.Contains(string(rewritten.Request.Body), "图里是猫") {
		t.Fatalf("vision 描述未写入，body=%s", string(rewritten.Request.Body))
	}
}

func TestProxyRouteByChannelID_MimicUserScenario(t *testing.T) {
	fake, url := fakellm.New()
	defer fake.Close()
	fake.SetResponse(`{"choices":[{"message":{"role":"assistant","content":"图里是一只猫"}}]}`)

	svc, _ := newTestService(t)

	const volcKeyID = "volcengine-key-uuid"
	if err := svc.repo.ReplaceChannels(context.Background(), []db.Channel{
		{
			ID: volcKeyID, Name: "volcengine", ChannelName: "volcengine",
			BaseURL: url + "/v1", ManualEnabled: true,
			Models: []db.ChannelModel{
				{Model: "qwen3-vl-flash-2026-01-22", Enabled: true},
				{Model: "deepseek-v4-pro-ga-260813", Enabled: true},
			},
		},
	}); err != nil {
		t.Fatalf("写渠道失败: %v", err)
	}

	// 用户截图的视觉路由：deepseek-* + volcengine(volcengine) key + vision proxy。
	if err := svc.repo.ReplaceCapabilityRoutes(context.Background(), []types.CapabilityRoute{
		{
			Models:     []string{"deepseek-*"},
			ChannelIDs: []string{volcKeyID},
			Capability: "vision", Route: types.RouteProxy,
			ViaOptions: []types.ViaOption{{ViaModel: "qwen3-vl-flash-2026-01-22", ChannelID: volcKeyID}},
		},
	}); err != nil {
		t.Fatalf("写能力路由失败: %v", err)
	}

	body := `{"model":"deepseek-v4-pro-ga-260813","messages":[{"role":"user","content":[{"type":"text","text":"看"},{"type":"image_url","image_url":{"url":"http://img/a.png"}}]}]}`
	pipe := proxyPipe("chat/completions", "deepseek-v4-pro-ga-260813", body)
	// 模拟 aggregate.HandleProxyBeforeUpstream 已先写 __current_channel。
	pipe.Metadata = map[string]any{"__current_channel": volcKeyID}

	out, err := svc.HandleProxyBeforeUpstream(pipe)
	if err != nil {
		t.Fatalf("视觉处理应命中却报错: %v", err)
	}
	rewritten, ok := out.(*modelgateway.ProxyPipeline)
	if !ok {
		t.Fatalf("payload 类型：%T", out)
	}
	// vision 是 in-place 改 Body 然后返回同一 pipe 指针；判断命中要看 Body 是否被改写。
	if rewritten.Request.Body == nil || string(rewritten.Request.Body) == body {
		t.Fatalf("视觉路由未改写 body（route 未命中或被当 native），导致 deepseek-v4 上游 400")
	}
	if strings.Contains(string(rewritten.Request.Body), "image_url") {
		t.Fatalf("vision 应删除 image_url，实际 body=%s", string(rewritten.Request.Body))
	}
	if !strings.Contains(string(rewritten.Request.Body), "图里是一只猫") {
		t.Fatalf("vision 描述未写入，body=%s", string(rewritten.Request.Body))
	}
}

// TestProxyRouteByChannelBaseURL 渠道级：路由绑定 channel_base_urls，请求通过该 base_url 下任一 key 都应命中。
// 这覆盖「用户只选了渠道级（点组名），新增 key 仍命中」的场景。
func TestProxyRouteByChannelBaseURL(t *testing.T) {
	fake, url := fakellm.New()
	defer fake.Close()
	fake.SetResponse(`{"choices":[{"message":{"role":"assistant","content":"图描述"}}]}`)

	svc, _ := newTestService(t)

	volcBaseURL := url + "/v1"
	const volcKey1 = "volcengine-k1"
	const volcKey2 = "volcengine-k2-newer" // 模拟后来新增的 key
	if err := svc.repo.ReplaceChannels(context.Background(), []db.Channel{
		{
			ID: volcKey1, Name: "volcengine", ChannelName: "volcengine",
			BaseURL: volcBaseURL, ManualEnabled: true,
			Models: []db.ChannelModel{{Model: "qwen3-vl-flash", Enabled: true}, {Model: "deepseek-v4-pro-ga-260813", Enabled: true}},
		},
		{
			ID: volcKey2, Name: "volcengine", ChannelName: "volcengine",
			BaseURL: volcBaseURL, ManualEnabled: true,
			Models: []db.ChannelModel{{Model: "qwen3-vl-flash", Enabled: true}, {Model: "deepseek-v4-pro-ga-260813", Enabled: true}},
		},
	}); err != nil {
		t.Fatalf("写渠道失败: %v", err)
	}

	// 渠道级：之前没有 K2，所以不能保存 K2 的 id；后加的 K2 也必须命中。
	if err := svc.repo.ReplaceCapabilityRoutes(context.Background(), []types.CapabilityRoute{
		{
			Models:          []string{"deepseek-*"},
			ChannelBaseURLs: []string{volcBaseURL},
			Capability:      "vision", Route: types.RouteProxy,
			ViaOptions: []types.ViaOption{{ViaModel: "qwen3-vl-flash", ChannelID: volcKey1}},
		},
	}); err != nil {
		t.Fatalf("写能力路由失败: %v", err)
	}

	body := `{"model":"deepseek-v4-pro-ga-260813","messages":[{"role":"user","content":[{"type":"text","text":"看"},{"type":"image_url","image_url":{"url":"http://img/a.png"}}]}]}`
	// 模拟聚合先选中 K2（后来新增的 key）——这正是渠道级匹配必须命中的关键场景。
	pipe := proxyPipe("chat/completions", "deepseek-v4-pro-ga-260813", body)
	pipe.Metadata = map[string]any{"__current_channel": volcKey2}

	out, err := svc.HandleProxyBeforeUpstream(pipe)
	if err != nil {
		t.Fatalf("新增 key K2 上的请求应命中渠道级路由: %v", err)
	}
	rewritten, ok := out.(*modelgateway.ProxyPipeline)
	if !ok {
		t.Fatalf("payload 类型: %T", out)
	}
	if string(rewritten.Request.Body) == body {
		t.Fatalf("渠道级路由应改写 body，但未命中——意味着「渠道级新增 key 也命中」语义失效")
	}
	if strings.Contains(string(rewritten.Request.Body), "image_url") {
		t.Fatalf("vision 应删除 image_url，实际 body=%s", string(rewritten.Request.Body))
	}
}
