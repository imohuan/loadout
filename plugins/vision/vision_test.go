package vision

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"loadout/core/config"
	"loadout/core/db"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
	fakellm "loadout/testkit/fake-llm"
)

// newTestService 用临时目录建 Store 与内存 SQLite，并把视觉缓存目录重定向到临时目录。
func newTestService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("db.OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	repo, err := db.NewRepository(database)
	if err != nil {
		t.Fatalf("db.NewRepository: %v", err)
	}
	old := config.VisionCacheDir
	config.VisionCacheDir = t.TempDir()
	t.Cleanup(func() { config.VisionCacheDir = old })
	return NewService(st, repo, slog.Default()), st
}

// seedChannels 把测试渠道写入 SQLite（渠道存储已从 channels.json 迁到 db）。
func seedChannels(t *testing.T, svc *Service, channels []types.Channel) {
	t.Helper()
	if svc.repo == nil {
		t.Fatal("svc.repo 未初始化")
	}
	dbc := make([]db.Channel, 0, len(channels))
	for _, c := range channels {
		var models []db.ChannelModel
		for _, m := range c.Models {
			models = append(models, db.ChannelModel{Model: m, Enabled: true, Source: "manual"})
		}
		dbc = append(dbc, db.Channel{
			ID: c.ID, Name: c.Name, BaseURL: c.BaseURL, APIKeyCipher: c.APIKeyCipher,
			ManualEnabled: c.ManualEnabled || c.Enabled, Models: models,
		})
	}
	if err := svc.repo.ReplaceChannels(context.Background(), dbc); err != nil {
		t.Fatalf("写入渠道失败: %v", err)
	}
}

// TestDetectImages 验证 content 数组里的 image_url（含 data URI）被检出，纯文本无图。
func TestDetectImages(t *testing.T) {
	svc, _ := newTestService(t)

	messages := []modelgateway.ChatMessage{
		{Role: "user", Content: modelgateway.MessageContent{Parts: []modelgateway.MessagePart{
			{Type: "text", Text: "看这张图"},
			{Type: "image_url", ImageURL: "http://img/a.png"},
			{Type: "image_url", ImageURL: "data:image/png;base64,iVBORw0KGgo="},
		}}},
		{Role: "user", Content: modelgateway.MessageContent{Text: "纯文本"}},
	}
	got := svc.DetectImages(messages)
	if len(got) != 2 {
		t.Fatalf("应检出 2 张图，实际 %d: %+v", len(got), got)
	}
	if got[0] != "http://img/a.png" || got[1] != "data:image/png;base64,iVBORw0KGgo=" {
		t.Fatalf("检出结果不符: %+v", got)
	}

	none := svc.DetectImages([]modelgateway.ChatMessage{
		{Role: "user", Content: modelgateway.MessageContent{Text: "你好"}},
	})
	if len(none) != 0 {
		t.Fatalf("纯文本不应检出图片，实际 %+v", none)
	}
}

// TestDecideRouteChannelLevel 渠道级约束：路由绑定 channel_base_urls，请求命中同 base_url 的
// 任一 Key（含新增 Key）都命中；不同 base_url / 未知渠道不命中；纯渠道级路由不误判为全渠道。
func TestDecideRouteChannelLevel(t *testing.T) {
	svc, _ := newTestService(t)
	// 同 base_url 两个 Key（k1 已存在，k2 模拟「后来新增」的 Key）。
	seedChannels(t, svc, []types.Channel{
		{ID: "k1", Name: "key1", BaseURL: "https://pixie.example/v1", Enabled: true},
		{ID: "k2", Name: "key2", BaseURL: "https://pixie.example/v1", Enabled: true},
		{ID: "other", Name: "other", BaseURL: "https://other.example/v1", Enabled: true},
	})
	routes := []types.CapabilityRoute{
		// 渠道级：绑定 pixie.example 整组。
		{Models: []string{"gpt-5"}, ChannelBaseURLs: []string{"https://pixie.example/v1"}, Capability: "vision", Route: types.RouteProxy, ViaOptions: []types.ViaOption{{ViaModel: "qwen-vl-max"}}},
	}
	if err := svc.repo.ReplaceCapabilityRoutes(context.Background(), routes); err != nil {
		t.Fatalf("写能力路由表失败: %v", err)
	}

	// 命中同 base_url 的已存在 Key k1。
	hitK1, err := svc.DecideRoute("gpt-5", "k1")
	if err != nil {
		t.Fatalf("DecideRoute 出错: %v", err)
	}
	if hitK1 == nil || hitK1.Route != types.RouteProxy {
		t.Fatalf("k1 应命中渠道级路由: %+v", hitK1)
	}
	// 命中同 base_url 的新增 Key k2（渠道级语义：新增 Key 仍命中）。
	hitK2, err := svc.DecideRoute("gpt-5", "k2")
	if err != nil {
		t.Fatalf("DecideRoute 出错: %v", err)
	}
	if hitK2 == nil {
		t.Fatalf("k2 应命中渠道级路由（新增 Key 仍命中）")
	}
	// 不同 base_url 不命中。
	missOther, err := svc.DecideRoute("gpt-5", "other")
	if err != nil {
		t.Fatalf("DecideRoute 出错: %v", err)
	}
	if missOther != nil {
		t.Fatalf("other 不应命中渠道级路由: %+v", missOther)
	}
	// 未知渠道不命中（纯渠道级路由不应被当全渠道）。
	missUnknown, err := svc.DecideRoute("gpt-5", "")
	if err != nil {
		t.Fatalf("DecideRoute 出错: %v", err)
	}
	if missUnknown != nil {
		t.Fatalf("未知渠道不应命中渠道级路由: %+v", missUnknown)
	}
}

// TestDecideRoute 验证写能力路由表（DB）后命中，未命中返回 nil；渠道约束按请求渠道过滤。
func TestDecideRoute(t *testing.T) {
	svc, _ := newTestService(t)
	routes := []types.CapabilityRoute{
		{Models: []string{"deepseek-chat"}, Capability: "vision", Route: types.RouteProxy, ViaOptions: []types.ViaOption{{ViaModel: "qwen-vl-max"}}},
		{Models: []string{"gpt-4o"}, Capability: "vision", Route: types.RouteNative},
		// 渠道约束：仅 ch-b 命中；ch-a / 未知渠道不命中。
		{Models: []string{"gpt-5"}, ChannelIDs: []string{"ch-b"}, Capability: "vision", Route: types.RouteProxy, ViaOptions: []types.ViaOption{{ViaModel: "qwen-vl-max"}}},
		// 通用全匹配：* 对任何渠道（含未知）命中。
		{Models: []string{"claude-x"}, ChannelIDs: []string{"*"}, Capability: "vision", Route: types.RouteProxy, ViaOptions: []types.ViaOption{{ViaModel: "qwen-vl-max"}}},
	}
	if err := svc.repo.ReplaceCapabilityRoutes(context.Background(), routes); err != nil {
		t.Fatalf("写能力路由表失败: %v", err)
	}

	hit, err := svc.DecideRoute("deepseek-chat", "")
	if err != nil {
		t.Fatalf("DecideRoute 出错: %v", err)
	}
	if hit == nil || hit.Route != types.RouteProxy || len(hit.ViaOptions) == 0 || hit.ViaOptions[0].ViaModel != "qwen-vl-max" {
		t.Fatalf("应命中 proxy 路由: %+v", hit)
	}

	native, err := svc.DecideRoute("gpt-4o", "")
	if err != nil {
		t.Fatalf("DecideRoute 出错: %v", err)
	}
	if native == nil || native.Route != types.RouteNative {
		t.Fatalf("应命中 native 路由: %+v", native)
	}

	miss, err := svc.DecideRoute("unknown-model", "")
	if err != nil {
		t.Fatalf("未命中不应报错: %v", err)
	}
	if miss != nil {
		t.Fatalf("未命中应返回 nil，实际 %+v", miss)
	}

	// 渠道约束：命中渠道命中，非命中渠道与未知渠道不命中。
	chHit, err := svc.DecideRoute("gpt-5", "ch-b")
	if err != nil {
		t.Fatalf("DecideRoute 出错: %v", err)
	}
	if chHit == nil || chHit.Route != types.RouteProxy {
		t.Fatalf("ch-b 上的 gpt-5 应命中约束路由: %+v", chHit)
	}
	chMiss, err := svc.DecideRoute("gpt-5", "ch-a")
	if err != nil {
		t.Fatalf("DecideRoute 出错: %v", err)
	}
	if chMiss != nil {
		t.Fatalf("ch-a 上的 gpt-5 不应命中 ch-b 约束路由，实际 %+v", chMiss)
	}
	unknownMiss, err := svc.DecideRoute("gpt-5", "")
	if err != nil {
		t.Fatalf("DecideRoute 出错: %v", err)
	}
	if unknownMiss != nil {
		t.Fatalf("渠道未知不应命中约束路由，实际 %+v", unknownMiss)
	}

	// 通用全匹配：* 对任何渠道（含未知）命中。
	starHit, err := svc.DecideRoute("claude-x", "")
	if err != nil {
		t.Fatalf("DecideRoute 出错: %v", err)
	}
	if starHit == nil || starHit.Route != types.RouteProxy {
		t.Fatalf("claude-x 应命中 * 全匹配渠道路由: %+v", starHit)
	}
	starHitCh, err := svc.DecideRoute("claude-x", "ch-z")
	if err != nil {
		t.Fatalf("DecideRoute 出错: %v", err)
	}
	if starHitCh == nil {
		t.Fatalf("claude-x 在任意渠道应命中 * 路由")
	}
}

// TestDescribeAndCache 验证 Describe 返回描述文本、二次调用命中缓存、关闭缓存后重新请求。
func TestDescribeAndCache(t *testing.T) {
	fake, url := fakellm.New()
	defer fake.Close()

	svc, _ := newTestService(t)

	// 写一个未知渠道（Models 空 → 兜底匹配视觉模型）。
	seedChannels(t, svc, []types.Channel{
		{ID: "vis", Name: "视觉渠道", BaseURL: url + "/v1", Enabled: true},
	})

	oldEnabled := config.VisionCacheEnabled
	config.VisionCacheEnabled = true
	defer func() { config.VisionCacheEnabled = oldEnabled }()

	fake.SetResponse(`{"id":"c1","choices":[{"message":{"role":"assistant","content":"图中是一只猫"}}]}`)

	text, _, err := svc.Describe(context.Background(), []string{"http://img/a.png"}, "qwen-vl-max", "", nil)
	if err != nil {
		t.Fatalf("Describe 出错: %v", err)
	}
	if text != "图中是一只猫" {
		t.Fatalf("描述文本不符: %q", text)
	}
	if got := len(fake.Requests()); got != 1 {
		t.Fatalf("首次调用应发 1 个请求，实际 %d", got)
	}

	// 二次调用命中缓存，请求数不增加。
	text2, _, err := svc.Describe(context.Background(), []string{"http://img/a.png"}, "qwen-vl-max", "", nil)
	if err != nil {
		t.Fatalf("二次 Describe 出错: %v", err)
	}
	if text2 != "图中是一只猫" {
		t.Fatalf("缓存命中文本不符: %q", text2)
	}
	if got := len(fake.Requests()); got != 1 {
		t.Fatalf("命中缓存不应新增请求，实际 %d", got)
	}

	// 关闭缓存后再次调用应新增请求。
	config.VisionCacheEnabled = false
	text3, _, err := svc.Describe(context.Background(), []string{"http://img/a.png"}, "qwen-vl-max", "", nil)
	if err != nil {
		t.Fatalf("关闭缓存后 Describe 出错: %v", err)
	}
	if text3 != "图中是一只猫" {
		t.Fatalf("关闭缓存后文本不符: %q", text3)
	}
	if got := len(fake.Requests()); got != 2 {
		t.Fatalf("关闭缓存后应新增请求，实际 %d", got)
	}
}

// TestRewriteMessages 验证图片分段被替换为 text，文本分段保留，纯文本消息不变。
func TestRewriteMessages(t *testing.T) {
	svc, _ := newTestService(t)

	messages := []modelgateway.ChatMessage{
		{Role: "user", Content: modelgateway.MessageContent{Parts: []modelgateway.MessagePart{
			{Type: "text", Text: "看这张图"},
			{Type: "image_url", ImageURL: "http://img/a.png"},
		}}},
		{Role: "user", Content: modelgateway.MessageContent{Text: "纯文本"}},
	}
	got := svc.RewriteMessages(messages, "图中是一只猫")

	parts := got[0].Content.Parts
	if len(parts) != 2 {
		t.Fatalf("第一条消息应保留 2 段，实际 %d: %+v", len(parts), parts)
	}
	if parts[0].Type != "text" || parts[0].Text != "看这张图" {
		t.Fatalf("原文本分段应保留，实际 %+v", parts[0])
	}
	if parts[1].Type != "text" || parts[1].Text != "图中是一只猫" {
		t.Fatalf("图片分段应替换为 text 描述，实际 %+v", parts[1])
	}
	if got[1].Content.Text != "纯文本" {
		t.Fatalf("纯文本消息应保持原样，实际 %+v", got[1].Content)
	}
}

// TestHandleBeforeUpstreamNoImage 验证无图时原样返回 payload。
func TestHandleBeforeUpstreamNoImage(t *testing.T) {
	svc, _ := newTestService(t)
	pipe := &modelgateway.Pipeline{
		Request:  &modelgateway.ChatRequest{Model: "deepseek-chat"},
		Messages: []modelgateway.ChatMessage{{Role: "user", Content: modelgateway.MessageContent{Text: "你好"}}},
	}
	out, err := svc.handleBeforeUpstream(pipe)
	if err != nil {
		t.Fatalf("无图不应报错: %v", err)
	}
	if out != pipe {
		t.Fatalf("无图应原样返回同一 payload")
	}
}

// TestHandleBeforeUpstreamNative 验证 native 路由不处理，原样返回。
func TestHandleBeforeUpstreamNative(t *testing.T) {
	svc, _ := newTestService(t)
	if err := svc.repo.ReplaceCapabilityRoutes(context.Background(), []types.CapabilityRoute{
		{Models: []string{"gpt-4o"}, Capability: "vision", Route: types.RouteNative},
	}); err != nil {
		t.Fatalf("写能力路由表失败: %v", err)
	}

	pipe := &modelgateway.Pipeline{
		Request: &modelgateway.ChatRequest{Model: "gpt-4o"},
		Messages: []modelgateway.ChatMessage{{Role: "user", Content: modelgateway.MessageContent{Parts: []modelgateway.MessagePart{
			{Type: "image_url", ImageURL: "http://img/a.png"},
		}}}},
	}
	out, err := svc.handleBeforeUpstream(pipe)
	if err != nil {
		t.Fatalf("native 路由不应报错: %v", err)
	}
	if out != pipe {
		t.Fatalf("native 路由应原样返回同一 payload")
	}
	if len(pipe.Messages[0].Content.Parts) != 1 || pipe.Messages[0].Content.Parts[0].Type != "image_url" {
		t.Fatalf("native 路由不应改写消息: %+v", pipe.Messages)
	}
}

// TestHandleBeforeUpstreamProxy 验证 proxy 路由改写 Messages 且设置 VisionText。
func TestHandleBeforeUpstreamProxy(t *testing.T) {
	fake, url := fakellm.New()
	defer fake.Close()

	svc, _ := newTestService(t)
	oldEnabled := config.VisionCacheEnabled
	config.VisionCacheEnabled = false
	defer func() { config.VisionCacheEnabled = oldEnabled }()

	if err := svc.repo.ReplaceCapabilityRoutes(context.Background(), []types.CapabilityRoute{
		{Models: []string{"deepseek-chat"}, Capability: "vision", Route: types.RouteProxy, ViaOptions: []types.ViaOption{{ViaModel: "qwen-vl-max", ChannelID: "vis"}}},
	}); err != nil {
		t.Fatalf("写能力路由表失败: %v", err)
	}
	seedChannels(t, svc, []types.Channel{
		{ID: "vis", Name: "视觉渠道", BaseURL: url + "/v1", Enabled: true},
	})
	fake.SetResponse(`{"id":"c1","choices":[{"message":{"role":"assistant","content":"图中是一只猫"}}]}`)

	pipe := &modelgateway.Pipeline{
		Request: &modelgateway.ChatRequest{Model: "deepseek-chat"},
		Messages: []modelgateway.ChatMessage{{Role: "user", Content: modelgateway.MessageContent{Parts: []modelgateway.MessagePart{
			{Type: "text", Text: "看这张图"},
			{Type: "image_url", ImageURL: "http://img/a.png"},
		}}}},
	}
	out, err := svc.handleBeforeUpstream(pipe)
	if err != nil {
		t.Fatalf("proxy 路由不应报错: %v", err)
	}
	rewritten, ok := out.(*modelgateway.Pipeline)
	if !ok {
		t.Fatalf("应返回 *Pipeline，实际 %T", out)
	}
	parts := rewritten.Messages[0].Content.Parts
	if len(parts) != 2 {
		t.Fatalf("改写后应保留 2 段，实际 %d: %+v", len(parts), parts)
	}
	if parts[1].Type != "text" || parts[1].Text != "图中是一只猫" {
		t.Fatalf("图片分段应替换为 text 描述，实际 %+v", parts[1])
	}
	if got := len(fake.Requests()); got != 1 {
		t.Fatalf("视觉渠道应收到 1 个请求，实际 %d", got)
	}
}

// TestHandleBeforeUpstreamError 验证 error 路由返回 vision_capability_error。
func TestHandleBeforeUpstreamError(t *testing.T) {
	svc, _ := newTestService(t)
	if err := svc.repo.ReplaceCapabilityRoutes(context.Background(), []types.CapabilityRoute{
		{Models: []string{"deepseek-chat"}, Capability: "vision", Route: types.RouteError},
	}); err != nil {
		t.Fatalf("写能力路由表失败: %v", err)
	}

	pipe := &modelgateway.Pipeline{
		Request: &modelgateway.ChatRequest{Model: "deepseek-chat"},
		Messages: []modelgateway.ChatMessage{{Role: "user", Content: modelgateway.MessageContent{Parts: []modelgateway.MessagePart{
			{Type: "image_url", ImageURL: "http://img/a.png"},
		}}}},
	}
	_, err := svc.handleBeforeUpstream(pipe)
	if err == nil {
		t.Fatal("error 路由应报错")
	}
	var gw *modelgateway.GatewayError
	if !errors.As(err, &gw) {
		t.Fatalf("应返回 *GatewayError，实际 %T", err)
	}
	if gw.Type != "vision_capability_error" {
		t.Fatalf("错误类型应为 vision_capability_error，实际 %q", gw.Type)
	}
}
