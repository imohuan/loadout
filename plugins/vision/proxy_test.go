package vision

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"loadout/core/config"
	"loadout/plugins/contracts"
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
	svc, _ := newTestService(t)
	fake, url := fakellm.New()
	defer fake.Close()
	fake.SetResponse(`{"choices":[{"message":{"role":"assistant","content":"这是一张猫的图片"}}]}`)
	seedChannels(t, svc, []types.Channel{
		{ID: "v", Name: "视觉", BaseURL: url + "/v1", Enabled: true, Models: []string{"qwen-vl-max"}},
	})
	if err := svc.repo.ReplaceCapabilityRoutes(context.Background(), []types.CapabilityRoute{
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
	if n := len(fake.Requests()); n != 1 {
		t.Fatalf("单新图请求应识别 1 次，实际 %d 次", n)
	}
}

// TestHandleProxyBeforeUpstreamChat 多图：只保留一份描述，其余图片块删除。
func TestHandleProxyBeforeUpstreamChatMultipleImages(t *testing.T) {
	svc, _ := newTestService(t)
	fake, url := fakellm.New()
	defer fake.Close()
	fake.SetResponse(`{"choices":[{"message":{"role":"assistant","content":"图一图二综合描述"}}]}`)
	seedChannels(t, svc, []types.Channel{
		{ID: "v", Name: "视觉", BaseURL: url + "/v1", Enabled: true, Models: []string{"qwen-vl-max"}},
	})
	if err := svc.repo.ReplaceCapabilityRoutes(context.Background(), []types.CapabilityRoute{
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
	svc, _ := newTestService(t)
	fake, url := fakellm.New()
	defer fake.Close()
	fake.SetResponse(`{"choices":[{"message":{"role":"assistant","content":"图里是山川"}}]}`)
	seedChannels(t, svc, []types.Channel{
		{ID: "v", Name: "视觉", BaseURL: url + "/v1", Enabled: true, Models: []string{"qwen-vl-max"}},
	})
	if err := svc.repo.ReplaceCapabilityRoutes(context.Background(), []types.CapabilityRoute{
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

// TestHandleProxyBeforeUpstreamChannelScoped 渠道约束：路由绑定 ch-b，
// 请求 __current_channel=ch-a（或未知）时不命中（body 原样透传），=ch-b 时命中并改写。
func TestHandleProxyBeforeUpstreamChannelScoped(t *testing.T) {
	fake, url := fakellm.New()
	defer fake.Close()
	fake.SetResponse(`{"choices":[{"message":{"role":"assistant","content":"视觉描述"}}]}`)

	svc, _ := newTestService(t)
	seedChannels(t, svc, []types.Channel{
		{ID: "v", Name: "视觉", BaseURL: url + "/v1", Enabled: true, Models: []string{"qwen-vl-max"}},
	})
	if err := svc.repo.ReplaceCapabilityRoutes(context.Background(), []types.CapabilityRoute{
		{Models: []string{"deepseek-chat"}, ChannelIDs: []string{"ch-b"}, Capability: "vision", Route: types.RouteProxy, ViaOptions: []types.ViaOption{{ViaModel: "qwen-vl-max"}}},
	}); err != nil {
		t.Fatalf("写能力路由失败: %v", err)
	}

	body := `{"model":"deepseek-chat","messages":[{"role":"user","content":[{"type":"text","text":"看"},{"type":"image_url","image_url":{"url":"http://img/a.png"}}]}]}`

	// 渠道未知（普通请求，无 __current_channel）：约束路由不命中，原样透传。
	unknown, err := svc.HandleProxyBeforeUpstream(proxyPipe("chat/completions", "deepseek-chat", body))
	if err != nil {
		t.Fatalf("渠道未知不应报错: %v", err)
	}
	if got := string(unknown.(*modelgateway.ProxyPipeline).Request.Body); got != body {
		t.Fatalf("渠道未知应原样透传: %s", got)
	}

	// 非命中渠道 ch-a：不命中，原样透传。
	pipeA := proxyPipe("chat/completions", "deepseek-chat", body)
	pipeA.Metadata = map[string]any{"__current_channel": "ch-a"}
	other, err := svc.HandleProxyBeforeUpstream(pipeA)
	if err != nil {
		t.Fatalf("非命中渠道不应报错: %v", err)
	}
	if got := string(other.(*modelgateway.ProxyPipeline).Request.Body); got != body {
		t.Fatalf("ch-a 应原样透传: %s", got)
	}

	// 命中渠道 ch-b：图片替换为视觉描述。
	pipeB := proxyPipe("chat/completions", "deepseek-chat", body)
	pipeB.Metadata = map[string]any{"__current_channel": "ch-b"}
	hit, err := svc.HandleProxyBeforeUpstream(pipeB)
	if err != nil {
		t.Fatalf("命中渠道处理出错: %v", err)
	}
	got := string(hit.(*modelgateway.ProxyPipeline).Request.Body)
	if strings.Contains(got, "image_url") || !strings.Contains(got, "视觉描述") {
		t.Fatalf("ch-b 应命中并改写 body: %s", got)
	}
}

// historyPipe 构造带两条消息的 proxy 请求：msg0 历史 user 带图，最后一条 user 纯文本（无新图）。
const historyOnlyBody = `{"model":"deepseek-chat","messages":[` +
	`{"role":"user","content":[{"type":"text","text":"看这张图"},{"type":"image_url","image_url":{"url":"http://img/a.png"}}]},` +
	`{"role":"assistant","content":[{"type":"text","text":"这是一只猫"}]},` +
	`{"role":"user","content":[{"type":"text","text":"继续说"}]}]}`

// seedProxyRoute 写入 deepseek-chat → qwen-vl-max 的 proxy 能力路由与视觉渠道。
func seedProxyRoute(t *testing.T, svc *Service, fakeURL string) {
	t.Helper()
	seedChannels(t, svc, []types.Channel{
		{ID: "v", Name: "视觉", BaseURL: fakeURL + "/v1", Enabled: true, Models: []string{"qwen-vl-max"}},
	})
	if err := svc.repo.ReplaceCapabilityRoutes(context.Background(), []types.CapabilityRoute{
		{Models: []string{"deepseek-chat"}, Capability: "vision", Route: types.RouteProxy, ViaOptions: []types.ViaOption{{ViaModel: "qwen-vl-max"}}},
	}); err != nil {
		t.Fatalf("写能力路由失败: %v", err)
	}
}

// TestHandleProxyHistoryOnly 纯历史图请求：不调视觉模型，旧图替换为占位符（缓存关闭时）。
func TestHandleProxyHistoryOnly(t *testing.T) {
	fake, url := fakellm.New()
	defer fake.Close()
	fake.SetResponse(`{"choices":[{"message":{"role":"assistant","content":"不应被调用"}}]}`)

	svc, _ := newTestService(t)
	seedProxyRoute(t, svc, url)
	oldEnabled := config.VisionCacheEnabled
	config.VisionCacheEnabled = false
	defer func() { config.VisionCacheEnabled = oldEnabled }()

	out, err := svc.HandleProxyBeforeUpstream(proxyPipe("chat/completions", "deepseek-chat", historyOnlyBody))
	if err != nil {
		t.Fatalf("纯历史图请求不应报错: %v", err)
	}
	got := string(out.(*modelgateway.ProxyPipeline).Request.Body)
	if n := len(fake.Requests()); n != 0 {
		t.Fatalf("纯历史图不应调用视觉模型，实际 %d 次", n)
	}
	if strings.Contains(got, "image_url") {
		t.Fatalf("历史图片块应被替换: %s", got)
	}
	if !strings.Contains(got, historyPlaceholder) {
		t.Fatalf("缓存 miss 的历史旧图应替换为占位符 %q: %s", historyPlaceholder, got)
	}
	if !strings.Contains(got, "继续说") {
		t.Fatalf("最新 user 文本应保留: %s", got)
	}
}

// TestHandleProxyCacheHitForHistory 历史旧图缓存命中：用缓存描述替换，不调视觉模型。
func TestHandleProxyCacheHitForHistory(t *testing.T) {
	fake, url := fakellm.New()
	defer fake.Close()
	fake.SetResponse(`{"choices":[{"message":{"role":"assistant","content":"不应被调用"}}]}`)

	svc, _ := newTestService(t)
	seedProxyRoute(t, svc, url)
	oldEnabled := config.VisionCacheEnabled
	config.VisionCacheEnabled = true
	defer func() { config.VisionCacheEnabled = oldEnabled }()

	// 预写缓存（key 与 Describe 一致）。
	key := md5Hex("http://img/a.png|qwen-vl-max|" + visionCacheVersion)
	if err := svc.writeCache(key, "这是一只猫的描述"); err != nil {
		t.Fatalf("预写缓存失败: %v", err)
	}

	out, err := svc.HandleProxyBeforeUpstream(proxyPipe("chat/completions", "deepseek-chat", historyOnlyBody))
	if err != nil {
		t.Fatalf("历史旧图缓存命中不应报错: %v", err)
	}
	got := string(out.(*modelgateway.ProxyPipeline).Request.Body)
	if n := len(fake.Requests()); n != 0 {
		t.Fatalf("缓存命中不应调用视觉模型，实际 %d 次", n)
	}
	if !strings.Contains(got, "这是一只猫的描述") {
		t.Fatalf("历史旧图应替换为缓存描述: %s", got)
	}
	if strings.Contains(got, historyPlaceholder) {
		t.Fatalf("缓存命中不应出现占位符: %s", got)
	}
	if strings.Contains(got, "image_url") {
		t.Fatalf("历史图片块应被替换: %s", got)
	}
}

// TestHandleProxyMixed 历史图 + 新图：新图识别 1 次，历史旧图用占位符（缓存关闭时）。
func TestHandleProxyMixed(t *testing.T) {
	fake, url := fakellm.New()
	defer fake.Close()
	fake.SetResponse(`{"choices":[{"message":{"role":"assistant","content":"第二张图的描述"}}]}`)

	svc, _ := newTestService(t)
	seedProxyRoute(t, svc, url)
	oldEnabled := config.VisionCacheEnabled
	config.VisionCacheEnabled = false
	defer func() { config.VisionCacheEnabled = oldEnabled }()

	body := `{"model":"deepseek-chat","messages":[` +
		`{"role":"user","content":[{"type":"text","text":"第一张"},{"type":"image_url","image_url":{"url":"http://img/a.png"}}]},` +
		`{"role":"user","content":[{"type":"text","text":"第二张"},{"type":"image_url","image_url":{"url":"http://img/b.png"}}]}]}`

	out, err := svc.HandleProxyBeforeUpstream(proxyPipe("chat/completions", "deepseek-chat", body))
	if err != nil {
		t.Fatalf("混合请求不应报错: %v", err)
	}
	got := string(out.(*modelgateway.ProxyPipeline).Request.Body)
	if n := len(fake.Requests()); n != 1 {
		t.Fatalf("混合请求应只识别新图 1 次，实际 %d 次", n)
	}
	if strings.Count(got, "第二张图的描述") != 1 {
		t.Fatalf("新图描述应出现 1 份: %s", got)
	}
	if strings.Count(got, historyPlaceholder) != 1 {
		t.Fatalf("历史旧图应替换为 1 个占位符: %s", got)
	}
	if strings.Contains(got, "image_url") {
		t.Fatalf("图片块应全部替换: %s", got)
	}
}

// TestHandleProxyStreamPrefixOnlyForNew 流式「图片理解」前缀仅新图输出：
// 纯历史图请求不输出；新图请求输出一次。
func TestHandleProxyStreamPrefixOnlyForNew(t *testing.T) {
	fake, url := fakellm.New()
	defer fake.Close()
	fake.SetResponse(`{"choices":[{"message":{"role":"assistant","content":"描述文本"}}]}`)

	svc, _ := newTestService(t)
	seedProxyRoute(t, svc, url)
	oldEnabled := config.VisionCacheEnabled
	config.VisionCacheEnabled = false
	defer func() { config.VisionCacheEnabled = oldEnabled }()

	// 纯历史图 + 流式：不输出前缀（也不调用视觉模型）。
	var oldDeltas []string
	oldPipe := proxyPipe("chat/completions", "deepseek-chat", historyOnlyBody)
	oldPipe.StreamWriter = func(d string) error { oldDeltas = append(oldDeltas, d); return nil }
	if _, err := svc.HandleProxyBeforeUpstream(oldPipe); err != nil {
		t.Fatalf("纯历史图流式不应报错: %v", err)
	}
	if joined := strings.Join(oldDeltas, ""); strings.Contains(joined, visionStreamPrefix) {
		t.Fatalf("纯历史图请求不应输出图片理解前缀: %q", joined)
	}
	if n := len(fake.Requests()); n != 0 {
		t.Fatalf("纯历史图流式不应调用视觉模型，实际 %d 次", n)
	}

	// 新图 + 流式：输出前缀一次（视觉走流式，用 SSE 脚本回放）。
	fake.SetSSEScript([]string{`data: {"choices":[{"delta":{"content":"描述文本"}}]}` + "\n\n", "data: [DONE]\n\n"})
	var newDeltas []string
	newPipe := proxyPipe("chat/completions", "deepseek-chat", `{"model":"deepseek-chat","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"http://img/b.png"}}]}]}`)
	newPipe.StreamWriter = func(d string) error { newDeltas = append(newDeltas, d); return nil }
	if _, err := svc.HandleProxyBeforeUpstream(newPipe); err != nil {
		t.Fatalf("新图流式不应报错: %v", err)
	}
	joined := strings.Join(newDeltas, "")
	if !strings.HasPrefix(joined, visionStreamPrefix) {
		t.Fatalf("新图请求应输出图片理解前缀: %q", joined)
	}
	if strings.Count(joined, visionStreamPrefix) != 1 {
		t.Fatalf("图片理解前缀应只输出一次: %q", joined)
	}
}

// TestHandleProxyKeepModeUnchanged keep 模式：旧图也完整识别（现状行为）。
func TestHandleProxyKeepModeUnchanged(t *testing.T) {
	fake, url := fakellm.New()
	defer fake.Close()
	fake.SetResponse(`{"choices":[{"message":{"role":"assistant","content":"历史图也识别"}}]}`)

	svc, _ := newTestService(t)
	seedProxyRoute(t, svc, url)
	oldEnabled := config.VisionCacheEnabled
	config.VisionCacheEnabled = false
	defer func() { config.VisionCacheEnabled = oldEnabled }()
	oldMode := config.VisionHistoryMode
	config.VisionHistoryMode = "keep"
	defer func() { config.VisionHistoryMode = oldMode }()

	out, err := svc.HandleProxyBeforeUpstream(proxyPipe("chat/completions", "deepseek-chat", historyOnlyBody))
	if err != nil {
		t.Fatalf("keep 模式不应报错: %v", err)
	}
	got := string(out.(*modelgateway.ProxyPipeline).Request.Body)
	if n := len(fake.Requests()); n != 1 {
		t.Fatalf("keep 模式旧图应识别 1 次，实际 %d 次", n)
	}
	if !strings.Contains(got, "历史图也识别") {
		t.Fatalf("keep 模式应注入描述: %s", got)
	}
	if strings.Contains(got, "image_url") {
		t.Fatalf("keep 模式图片块也应替换: %s", got)
	}
}

// mockVisionRouteLog 记录 route-log 调用，供视觉两阶段日志断言。
type mockVisionRouteLog struct {
	starts   []contracts.RouteRequest
	attempts []contracts.RouteAttempt
	finishs  []contracts.RouteFinish
}

func (m *mockVisionRouteLog) Start(ctx context.Context, r contracts.RouteRequest) error {
	m.starts = append(m.starts, r)
	return nil
}
func (m *mockVisionRouteLog) Attempt(ctx context.Context, a contracts.RouteAttempt) (int64, error) {
	m.attempts = append(m.attempts, a)
	return int64(len(m.attempts)), nil
}
func (m *mockVisionRouteLog) Finish(ctx context.Context, f contracts.RouteFinish) error {
	m.finishs = append(m.finishs, f)
	return nil
}
func (m *mockVisionRouteLog) List(ctx context.Context, f contracts.RouteLogFilter) ([]contracts.RouteRequestView, error) {
	return nil, nil
}
func (m *mockVisionRouteLog) Detail(ctx context.Context, id string) (contracts.RouteRequestView, error) {
	return contracts.RouteRequestView{}, nil
}
func (m *mockVisionRouteLog) Clear(ctx context.Context, t time.Time) error { return nil }
func (m *mockVisionRouteLog) SelfHeal(ctx context.Context, id string, threshold time.Duration) error {
	return nil
}

// TestHandleProxyBeforeUpstreamVisionLogTwoPhase 验证两阶段写日志：识别开始写 running
// 占位，识别结束更新为 success（同一 step_no，UPSERT 合并）。
func TestHandleProxyBeforeUpstreamVisionLogTwoPhase(t *testing.T) {
	svc, _ := newTestService(t)
	log := &mockVisionRouteLog{}
	svc.SetRouteLog(log)
	fake, url := fakellm.New()
	defer fake.Close()
	fake.SetResponse(`{"choices":[{"message":{"role":"assistant","content":"这是一只猫"}}]}`)
	seedChannels(t, svc, []types.Channel{
		{ID: "v", Name: "视觉", BaseURL: url + "/v1", Enabled: true, Models: []string{"qwen-vl-max"}},
	})
	if err := svc.repo.ReplaceCapabilityRoutes(context.Background(), []types.CapabilityRoute{
		{Models: []string{"deepseek-chat"}, Capability: "vision", Route: types.RouteProxy, ViaOptions: []types.ViaOption{{ViaModel: "qwen-vl-max"}}},
	}); err != nil {
		t.Fatalf("写能力路由失败: %v", err)
	}

	body := `{"model":"deepseek-chat","messages":[{"role":"user","content":[{"type":"text","text":"看"},{"type":"image_url","image_url":{"url":"http://img/a.png"}}]}]}`
	pipe := proxyPipe("chat/completions", "deepseek-chat", body)
	pipe.RequestID = "req-vision-1"
	if _, err := svc.HandleProxyBeforeUpstream(pipe); err != nil {
		t.Fatalf("视觉处理出错: %v", err)
	}
	if len(log.attempts) != 2 {
		t.Fatalf("应写 2 条 attempt（running + success），实际 %d: %+v", len(log.attempts), log.attempts)
	}
	first, second := log.attempts[0], log.attempts[1]
	if first.RequestID != "req-vision-1" || first.StepNo != 1 || first.Action != "视觉识别" || first.Result != "running" {
		t.Fatalf("占位 attempt 不符: %+v", first)
	}
	if second.RequestID != "req-vision-1" || second.StepNo != 1 || second.Result != "success" || second.Model != "qwen-vl-max" || second.ChannelID != "v" {
		t.Fatalf("成功 attempt 不符: %+v", second)
	}
	if second.Duration.Duration() <= 0 {
		t.Fatalf("成功 attempt 耗时应为正: %v", second.Duration)
	}
}

// TestHandleProxyBeforeUpstreamVisionLogMultiCandidate 多 via_option：doubao 失败（404）
// + qwen3 成功，验证 4 条 attempt——doubao: running→failed(step=-2)、qwen3: running→success(step=-1)。
func TestHandleProxyBeforeUpstreamVisionLogMultiCandidate(t *testing.T) {
	svc, _ := newTestService(t)
	log := &mockVisionRouteLog{}
	svc.SetRouteLog(log)

	failLLM, failURL := fakellm.New()
	defer failLLM.Close()
	failLLM.SetError(http.StatusBadGateway, `{"error":{"message":"down","type":"upstream_error"}}`)
	okLLM, okURL := fakellm.New()
	defer okLLM.Close()
	okLLM.SetResponse(`{"choices":[{"message":{"role":"assistant","content":"识别成功"}}]}`)

	seedChannels(t, svc, []types.Channel{
		{ID: "ch-doubao", Name: "豆包", BaseURL: failURL + "/v1", Enabled: true, Models: []string{"doubao-vision"}},
		{ID: "ch-qwen", Name: "通义", BaseURL: okURL + "/v1", Enabled: true, Models: []string{"qwen3-vl"}},
	})
	if err := svc.repo.ReplaceCapabilityRoutes(context.Background(), []types.CapabilityRoute{
		{Models: []string{"deepseek-chat"}, Capability: "vision", Route: types.RouteProxy, ViaOptions: []types.ViaOption{
			{ViaModel: "doubao-vision", ChannelID: "ch-doubao"},
			{ViaModel: "qwen3-vl", ChannelID: "ch-qwen"},
		}},
	}); err != nil {
		t.Fatalf("写能力路由失败: %v", err)
	}

	body := `{"model":"deepseek-chat","messages":[{"role":"user","content":[{"type":"text","text":"看"},{"type":"image_url","image_url":{"url":"http://img/a.png"}}]}]}`
	pipe := proxyPipe("chat/completions", "deepseek-chat", body)
	pipe.RequestID = "req-vision-2"
	out, err := svc.HandleProxyBeforeUpstream(pipe)
	if err != nil {
		t.Fatalf("qwen3 兜底成功不应报错: %v", err)
	}
	if !strings.Contains(string(out.(*modelgateway.ProxyPipeline).Request.Body), "识别成功") {
		t.Fatalf("body 应包含 qwen3 描述: %s", string(out.(*modelgateway.ProxyPipeline).Request.Body))
	}
	if len(log.attempts) != 4 {
		t.Fatalf("应写 4 条 attempt（2 候选 × running+终态），实际 %d: %+v", len(log.attempts), log.attempts)
	}
	// 顺序：doubao running(1) → doubao failed(1) → qwen running(2) → qwen success(2)
	doubaoRunning, doubaoFailed := log.attempts[0], log.attempts[1]
	qwenRunning, qwenSuccess := log.attempts[2], log.attempts[3]
	if doubaoRunning.StepNo != 1 || doubaoRunning.Model != "doubao-vision" || doubaoRunning.Result != "running" {
		t.Fatalf("doubao 占位不符: %+v", doubaoRunning)
	}
	if doubaoFailed.StepNo != 1 || doubaoFailed.Result != "failed" || doubaoFailed.ErrorMessage == "" {
		t.Fatalf("doubao 失败更新不符: %+v", doubaoFailed)
	}
	if qwenRunning.StepNo != 2 || qwenRunning.Model != "qwen3-vl" || qwenRunning.Result != "running" {
		t.Fatalf("qwen 占位不符: %+v", qwenRunning)
	}
	if qwenSuccess.StepNo != 2 || qwenSuccess.Result != "success" || qwenSuccess.ChannelID != "ch-qwen" {
		t.Fatalf("qwen 成功更新不符: %+v", qwenSuccess)
	}
}

// TestHandleProxyBeforeUpstreamPureHistoryNoStepPollution 纯历史旧图场景
// （图片都在历史消息、最后一条 user 消息无图）：视觉不写 attempt（visionLogging=false），
// pipe.Metadata["__route_step"] 不应被视觉循环占用，主链路保持从 step 1 开始。
func TestHandleProxyBeforeUpstreamPureHistoryNoStepPollution(t *testing.T) {
	svc, _ := newTestService(t)
	log := &mockVisionRouteLog{}
	svc.SetRouteLog(log)
	fake, url := fakellm.New()
	defer fake.Close()
	fake.SetResponse(`{"choices":[{"message":{"role":"assistant","content":"历史图描述"}}]}`)
	seedChannels(t, svc, []types.Channel{
		{ID: "v", Name: "视觉", BaseURL: url + "/v1", Enabled: true, Models: []string{"qwen-vl-max"}},
	})
	if err := svc.repo.ReplaceCapabilityRoutes(context.Background(), []types.CapabilityRoute{
		{Models: []string{"deepseek-chat"}, Capability: "vision", Route: types.RouteProxy, ViaOptions: []types.ViaOption{{ViaModel: "qwen-vl-max"}}},
	}); err != nil {
		t.Fatalf("写能力路由失败: %v", err)
	}

	// 最后一条 user 消息无图 → newGroup 空、图片全归历史组（oldGroups）。
	body := `{"model":"deepseek-chat","messages":[
		{"role":"user","content":[{"type":"text","text":"看图"},{"type":"image_url","image_url":{"url":"http://img/a.png"}}]},
		{"role":"assistant","content":"描述"},
		{"role":"user","content":[{"type":"text","text":"继续"}]}
	]}`
	pipe := proxyPipe("chat/completions", "deepseek-chat", body)
	pipe.RequestID = "req-history-only"
	out, err := svc.HandleProxyBeforeUpstream(pipe)
	if err != nil {
		t.Fatalf("纯历史旧图不应报错: %v", err)
	}
	if len(log.attempts) != 0 {
		t.Fatalf("纯历史旧图不应写视觉 attempt，实际 %d: %+v", len(log.attempts), log.attempts)
	}
	// 视觉 step 空间未被占用：__route_step 不应存在（或保持 0），主链路从 1 开始。
	if step, ok := out.(*modelgateway.ProxyPipeline).Metadata["__route_step"]; ok && step != 0 {
		t.Fatalf("纯历史旧图不应占用 __route_step，实际 %v", step)
	}
}
