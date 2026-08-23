package sensitivefilter

import (
	"encoding/json"
	"log/slog"
	"testing"

	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// newTestService 用临时目录建 Store。
func newTestService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	return NewService(st, slog.Default()), st
}

// seedRoute 写入一条 sensitive_filter 能力路由。
func seedRoute(t *testing.T, st *store.Store, route types.CapabilityRoute) {
	t.Helper()
	routes := []types.CapabilityRoute{route}
	if err := st.Write(types.FileCapabilityRoutes, routes); err != nil {
		t.Fatalf("写能力路由表失败: %v", err)
	}
}

// proxyPipe 构造一个带 JSON body 的透明代理管线。
func proxyPipe(t *testing.T, body any) *modelgateway.ProxyPipeline {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return &modelgateway.ProxyPipeline{
		Request: &modelgateway.ProxyRequest{Body: raw, Model: "deepseek-chat"},
		Metadata: map[string]any{},
	}
}

// runHook 执行 Hook 并把返回值断言为 *ProxyPipeline（error 时返回 nil, err）。
func runHook(svc *Service, payload any) (*modelgateway.ProxyPipeline, error) {
	out, err := svc.HandleProxyBeforeUpstream(payload)
	if err != nil {
		return nil, err
	}
	pipe, _ := out.(*modelgateway.ProxyPipeline)
	return pipe, nil
}

// TestDecideRoute 验证路由命中/未命中/渠道约束/通配。
func TestDecideRoute(t *testing.T) {
	svc, st := newTestService(t)
	routes := []types.CapabilityRoute{
		{Models: []string{"deepseek-chat"}, Capability: capabilityName, Route: types.RouteProxy, Replacements: []types.SensitiveReplacement{{From: "脏话", To: "***"}}},
		// 渠道约束：仅 ch-b 命中。
		{Models: []string{"gpt-5"}, ChannelIDs: []string{"ch-b"}, Capability: capabilityName, Route: types.RouteProxy, Replacements: []types.SensitiveReplacement{{From: "x", To: "y"}}},
		// 通用全匹配：* 对任何渠道（含未知）命中。
		{Models: []string{"claude-x"}, ChannelIDs: []string{"*"}, Capability: capabilityName, Route: types.RouteNative},
		// 其他能力不命中 sensitive_filter。
		{Models: []string{"vision-model"}, Capability: "vision", Route: types.RouteProxy, ViaOptions: []types.ViaOption{{ViaModel: "qwen-vl-max"}}},
	}
	if err := st.Write(types.FileCapabilityRoutes, routes); err != nil {
		t.Fatalf("写能力路由表失败: %v", err)
	}

	hit, err := svc.DecideRoute("deepseek-chat", "")
	if err != nil {
		t.Fatalf("DecideRoute 出错: %v", err)
	}
	if hit == nil || hit.Route != types.RouteProxy || len(hit.Replacements) != 1 {
		t.Fatalf("应命中 proxy 路由: %+v", hit)
	}

	if hit, _ := svc.DecideRoute("gpt-5", "ch-a"); hit != nil {
		t.Fatalf("渠道 ch-a 不应命中: %+v", hit)
	}
	if hit, _ := svc.DecideRoute("gpt-5", "ch-b"); hit == nil {
		t.Fatal("渠道 ch-b 应命中")
	}
	if hit, _ := svc.DecideRoute("claude-x", "any"); hit == nil {
		t.Fatal("通配渠道应命中")
	}
	if hit, _ := svc.DecideRoute("vision-model", ""); hit != nil {
		t.Fatalf("vision 能力不应命中 sensitive_filter: %+v", hit)
	}
	if hit, _ := svc.DecideRoute("unknown-model", ""); hit != nil {
		t.Fatalf("未知模型不应命中: %+v", hit)
	}
}

// TestProxyReplace 验证 proxy 路由整体替换：body 敏感词被替换且仍是合法 JSON。
func TestProxyReplace(t *testing.T) {
	svc, st := newTestService(t)
	seedRoute(t, st, types.CapabilityRoute{
		Models: []string{"deepseek-chat"}, Capability: capabilityName, Route: types.RouteProxy,
		Replacements: []types.SensitiveReplacement{{From: "脏话", To: "***"}},
	})

	pipe := proxyPipe(t, map[string]any{"model": "deepseek-chat", "messages": []any{
		map[string]any{"role": "user", "content": "这是一句脏话测试"},
	}})
	out, err := runHook(svc, pipe)
	if err != nil {
		t.Fatalf("Hook 出错: %v", err)
	}
	if out == nil {
		t.Fatal("应返回改写后的管线")
	}
	body := string(out.Request.Body)
	if contains(body, "脏话") {
		t.Fatalf("敏感词未被替换: %s", body)
	}
	if !contains(body, "***") {
		t.Fatalf("替换内容未写入: %s", body)
	}
	if !json.Valid(out.Request.Body) {
		t.Fatalf("替换后 JSON 非法: %s", body)
	}
}

// TestProxyRegexReplace 验证正则替换：\d{11} → [手机号]，含捕获组引用。
func TestProxyRegexReplace(t *testing.T) {
	svc, st := newTestService(t)
	seedRoute(t, st, types.CapabilityRoute{
		Models: []string{"deepseek-chat"}, Capability: capabilityName, Route: types.RouteProxy,
		Replacements: []types.SensitiveReplacement{
			{From: `(\d{3})\d{8}`, To: `$1********`, Regex: true},
		},
	})

	pipe := proxyPipe(t, map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "联系 13812345678 电话"},
	}})
	out, err := runHook(svc, pipe)
	if err != nil {
		t.Fatalf("Hook 出错: %v", err)
	}
	body := string(out.Request.Body)
	if !contains(body, "138********") {
		t.Fatalf("正则替换未生效: %s", body)
	}
	if contains(body, "13812345678") {
		t.Fatalf("手机号未脱敏: %s", body)
	}
}

// TestProxyReplaceChain 验证替换顺序：先 脏话→***，再 ** → x。
func TestProxyReplaceChain(t *testing.T) {
	svc, st := newTestService(t)
	seedRoute(t, st, types.CapabilityRoute{
		Models: []string{"deepseek-chat"}, Capability: capabilityName, Route: types.RouteProxy,
		Replacements: []types.SensitiveReplacement{
			{From: "脏话", To: "***"},
			{From: "***", To: "x"},
		},
	})
	pipe := proxyPipe(t, map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "脏话来了"},
	}})
	out, err := runHook(svc, pipe)
	if err != nil {
		t.Fatalf("Hook 出错: %v", err)
	}
	body := string(out.Request.Body)
	if !contains(body, "x来了") {
		t.Fatalf("链式替换未按序生效: %s", body)
	}
}

// TestNativePassthrough 验证 native 路由原样透传。
func TestNativePassthrough(t *testing.T) {
	svc, st := newTestService(t)
	seedRoute(t, st, types.CapabilityRoute{
		Models: []string{"deepseek-chat"}, Capability: capabilityName, Route: types.RouteNative,
	})
	pipe := proxyPipe(t, map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "脏话"},  // 即使含敏感词，native 也不处理
	}})
	before := append([]byte(nil), pipe.Request.Body...)
	out, err := runHook(svc, pipe)
	if err != nil {
		t.Fatalf("Hook 出错: %v", err)
	}
	if out == nil {
		t.Fatal("native 应原样返回管线")
	}
	if string(out.Request.Body) != string(before) {
		t.Fatalf("native 不应改写 body")
	}
}

// TestErrorReject 验证 error 路由：命中敏感词直接拒绝。
// TestErrorDataDegradedPassthrough 历史 route="error" 数据按 native（透传）降级：
// 命中敏感词也不拒绝，body 原样透传（「不支持就不管他」语义）。
func TestErrorDataDegradedPassthrough(t *testing.T) {
	svc, st := newTestService(t)
	seedRoute(t, st, types.CapabilityRoute{
		Models: []string{"deepseek-chat"}, Capability: capabilityName, Route: "error",
		Replacements: []types.SensitiveReplacement{{From: "违禁词", To: ""}},
	})
	pipe := proxyPipe(t, map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "说了违禁词"},
	}})
	before := append([]byte(nil), pipe.Request.Body...)
	out, err := runHook(svc, pipe)
	if err != nil {
		t.Fatalf("历史 error 数据降级后不应拒绝: %v", err)
	}
	if string(out.Request.Body) != string(before) {
		t.Fatalf("历史 error 数据应按 native 原样透传，body 被改写")
	}
}

// TestErrorNotHitPassthrough 验证历史 error 数据未命中敏感词时原样透传（降级 native 语义）。
func TestErrorNotHitPassthrough(t *testing.T) {
	svc, st := newTestService(t)
	seedRoute(t, st, types.CapabilityRoute{
		Models: []string{"deepseek-chat"}, Capability: capabilityName, Route: "error",
		Replacements: []types.SensitiveReplacement{{From: "违禁词", To: ""}},
	})
	pipe := proxyPipe(t, map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "一切正常"},
	}})
	before := append([]byte(nil), pipe.Request.Body...)
	out, err := runHook(svc, pipe)
	if err != nil {
		t.Fatalf("未命中不应拒绝: %v", err)
	}
	if string(out.Request.Body) != string(before) {
		t.Fatalf("未命中不应改写 body")
	}
}

// TestJSONBreakFallback 验证整体替换破坏 JSON 时降级：不报错、只替换 messages 文本、JSON 仍合法。
func TestJSONBreakFallback(t *testing.T) {
	svc, st := newTestService(t)
	seedRoute(t, st, types.CapabilityRoute{
		Models: []string{"deepseek-chat"}, Capability: capabilityName, Route: types.RouteProxy,
		// to 含未转义引号：整体字符串替换会破坏 JSON 结构，触发降级。
		Replacements: []types.SensitiveReplacement{{From: "正常", To: `破坏"结构`}},
	})
	pipe := proxyPipe(t, map[string]any{"model": "deepseek-chat", "messages": []any{
		map[string]any{"role": "user", "content": "一段正常文本"},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "text", "text": "回复也正常"},
		}},
	}})
	out, err := runHook(svc, pipe)
	if err != nil {
		t.Fatalf("整体替换破坏 JSON 应降级而非报错: %v", err)
	}
	if out == nil {
		t.Fatal("应返回改写后的管线")
	}
	if !json.Valid(out.Request.Body) {
		t.Fatalf("降级后 JSON 应仍合法: %s", out.Request.Body)
	}
	// 校验替换发生在 messages 文本上（而不是拒绝请求）。
	var parsed struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out.Request.Body, &parsed); err != nil {
		t.Fatalf("解析降级结果失败: %v", err)
	}
	if len(parsed.Messages) != 2 {
		t.Fatalf("messages 数量不应变化: %d", len(parsed.Messages))
	}
	if parsed.Messages[0].Content != `一段破坏"结构文本` {
		t.Fatalf("纯字符串 content 应被替换: %#v", parsed.Messages[0].Content)
	}
	// 分段 content：检查 text 块被替换。
	parts, ok := parsed.Messages[1].Content.([]any)
	if !ok {
		t.Fatalf("分段 content 类型不符: %#v", parsed.Messages[1].Content)
	}
	block, ok := parts[0].(map[string]any)
	if !ok || block["text"] != `回复也破坏"结构` {
		t.Fatalf("分段 text 块应被替换: %#v", parts[0])
	}
}

// TestMessagesFallbackRegex 验证降级路径下正则规则也生效。
func TestMessagesFallbackRegex(t *testing.T) {
	svc, st := newTestService(t)
	seedRoute(t, st, types.CapabilityRoute{
		Models: []string{"deepseek-chat"}, Capability: capabilityName, Route: types.RouteProxy,
		// 正则替换后含引号破坏整体 JSON → 降级；降级路径里正则仍生效。
		Replacements: []types.SensitiveReplacement{{From: `(\d{3})\d{8}`, To: `$1"*"`, Regex: true}},
	})
	pipe := proxyPipe(t, map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "电话 13812345678"},
	}})
	out, err := runHook(svc, pipe)
	if err != nil {
		t.Fatalf("降级不应报错: %v", err)
	}
	if !json.Valid(out.Request.Body) {
		t.Fatalf("降级后 JSON 应合法: %s", out.Request.Body)
	}
	body := string(out.Request.Body)
	if !contains(body, `138\"*\"`) || contains(body, "13812345678") {
		t.Fatalf("降级路径正则替换未生效: %s", body)
	}
}

// TestNonJSONPassthrough 验证非 JSON body 原样透传（不误伤）。
func TestNonJSONPassthrough(t *testing.T) {
	svc, st := newTestService(t)
	seedRoute(t, st, types.CapabilityRoute{
		Models: []string{"deepseek-chat"}, Capability: capabilityName, Route: types.RouteProxy,
		Replacements: []types.SensitiveReplacement{{From: "abc", To: "xyz"}},
	})
	pipe := &modelgateway.ProxyPipeline{
		Request: &modelgateway.ProxyRequest{Body: []byte("this is not json abc"), Model: "deepseek-chat"},
		Metadata: map[string]any{},
	}
	before := append([]byte(nil), pipe.Request.Body...)
	out, err := runHook(svc, pipe)
	if err != nil {
		t.Fatalf("非 JSON 不应报错: %v", err)
	}
	if string(out.Request.Body) != string(before) {
		t.Fatalf("非 JSON body 应原样透传")
	}
}

// TestInvalidRegexReject 验证非法正则导致替换报错（配置错误拒绝请求）。
func TestInvalidRegexReject(t *testing.T) {
	svc, st := newTestService(t)
	seedRoute(t, st, types.CapabilityRoute{
		Models: []string{"deepseek-chat"}, Capability: capabilityName, Route: types.RouteProxy,
		Replacements: []types.SensitiveReplacement{{From: `([a-`, To: "x", Regex: true}},
	})
	pipe := proxyPipe(t, map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "文本"},
	}})
	_, err := runHook(svc, pipe)
	if err == nil {
		t.Fatal("非法正则应报错")
	}
}

// TestNoRoutePassthrough 验证未命中路由时原样透传。
func TestNoRoutePassthrough(t *testing.T) {
	svc, st := newTestService(t)
	seedRoute(t, st, types.CapabilityRoute{
		Models: []string{"other-model"}, Capability: capabilityName, Route: types.RouteProxy,
		Replacements: []types.SensitiveReplacement{{From: "脏话", To: "***"}},
	})
	pipe := proxyPipe(t, map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "脏话"},
	}})
	before := append([]byte(nil), pipe.Request.Body...)
	out, err := runHook(svc, pipe)
	if err != nil {
		t.Fatalf("未命中路由不应报错: %v", err)
	}
	if string(out.Request.Body) != string(before) {
		t.Fatalf("未命中路由不应改写 body")
	}
}

// TestErrorRegexHitDegradedPassthrough 历史 error 数据的正则规则命中也不拒绝（降级 native 透传）。
func TestErrorRegexHitDegradedPassthrough(t *testing.T) {
	svc, st := newTestService(t)
	seedRoute(t, st, types.CapabilityRoute{
		Models: []string{"deepseek-chat"}, Capability: capabilityName, Route: "error",
		Replacements: []types.SensitiveReplacement{{From: `\d{11}`, To: "", Regex: true}},
	})
	pipe := proxyPipe(t, map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "电话 13812345678"},
	}})
	before := append([]byte(nil), pipe.Request.Body...)
	out, err := runHook(svc, pipe)
	if err != nil {
		t.Fatalf("历史 error 数据正则命中不应拒绝: %v", err)
	}
	if string(out.Request.Body) != string(before) {
		t.Fatalf("历史 error 数据应按 native 原样透传，body 被改写")
	}
}

// TestErrorInvalidRegexDegradedPassthrough 历史 error 数据的非法正则也不报错（native 短路，不解析规则）。
func TestErrorInvalidRegexDegradedPassthrough(t *testing.T) {
	svc, st := newTestService(t)
	seedRoute(t, st, types.CapabilityRoute{
		Models: []string{"deepseek-chat"}, Capability: capabilityName, Route: "error",
		Replacements: []types.SensitiveReplacement{{From: `([a-`, To: "", Regex: true}},
	})
	pipe := proxyPipe(t, map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "文本"},
	}})
	before := append([]byte(nil), pipe.Request.Body...)
	out, err := runHook(svc, pipe)
	if err != nil {
		t.Fatalf("历史 error 数据非法正则应透传不报错: %v", err)
	}
	if string(out.Request.Body) != string(before) {
		t.Fatalf("历史 error 数据应按 native 原样透传，body 被改写")
	}
}

// TestEmptyFromSkipped 验证空 from 规则被跳过：proxy 不破坏 body，error 不误拒。
func TestEmptyFromSkipped(t *testing.T) {
	svc, st := newTestService(t)
	seedRoute(t, st, types.CapabilityRoute{
		Models: []string{"deepseek-chat"}, Capability: capabilityName, Route: types.RouteProxy,
		Replacements: []types.SensitiveReplacement{
			{From: "", To: "x"},     // 空 from：应跳过，否则逐字符插入破坏 JSON
			{From: "脏话", To: "***"},
		},
	})
	pipe := proxyPipe(t, map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "脏话"},
	}})
	out, err := runHook(svc, pipe)
	if err != nil {
		t.Fatalf("空 from 不应报错: %v", err)
	}
	body := string(out.Request.Body)
	if !contains(body, "***") || contains(body, "脏话") {
		t.Fatalf("替换未生效: %s", body)
	}
	if !json.Valid(out.Request.Body) {
		t.Fatalf("替换后 JSON 非法: %s", body)
	}
}

// TestEmptyFromErrorNotHit 验证历史 error 数据空 from 规则被跳过，不误拒任何请求（降级 native）。
func TestEmptyFromErrorNotHit(t *testing.T) {
	svc, st := newTestService(t)
	seedRoute(t, st, types.CapabilityRoute{
		Models: []string{"deepseek-chat"}, Capability: capabilityName, Route: "error",
		Replacements: []types.SensitiveReplacement{{From: "", To: "x"}},
	})
	pipe := proxyPipe(t, map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "正常文本"},
	}})
	out, err := runHook(svc, pipe)
	if err != nil {
		t.Fatalf("空 from 不应误拒: %v", err)
	}
	if out == nil {
		t.Fatal("应返回管线")
	}
}

// TestDecideRouteReadErrorFailOpen 验证读表失败 fail-open（返回 nil 视为透传，不拒绝）。
func TestDecideRouteReadErrorFailOpen(t *testing.T) {
	svc, st := newTestService(t)
	// 写一个非 JSON 文件破坏 capability_routes.json。
	if err := st.Write(types.FileCapabilityRoutes, "not-json-array"); err != nil {
		t.Fatalf("写坏表失败: %v", err)
	}
	route, err := svc.DecideRoute("deepseek-chat", "")
	if err != nil {
		t.Fatalf("读表失败应 fail-open，实际报错: %v", err)
	}
	if route != nil {
		t.Fatalf("坏表应返回 nil，实际 %+v", route)
	}
	// 端到端：坏表下请求应原样透传。
	pipe := proxyPipe(t, map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "脏话"},
	}})
	before := append([]byte(nil), pipe.Request.Body...)
	out, err := runHook(svc, pipe)
	if err != nil {
		t.Fatalf("坏表不应拒绝请求: %v", err)
	}
	if string(out.Request.Body) != string(before) {
		t.Fatalf("坏表下应原样透传")
	}
}

// TestNilPayload 验证非法载荷原样返回。
func TestNilPayload(t *testing.T) {
	svc, _ := newTestService(t)
	out, err := svc.HandleProxyBeforeUpstream("not-a-pipe")
	if err != nil {
		t.Fatalf("非法载荷不应报错: %v", err)
	}
	if out != "not-a-pipe" {
		t.Fatal("非法载荷应原样返回")
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestProxyUnicodeEscape 验证客户端把中文转义成 \uXXXX 时（python json.dumps 默认行为），
// 整体字节替换搜不到，必须降级到结构化替换命中。
func TestProxyUnicodeEscape(t *testing.T) {
	svc, st := newTestService(t)
	seedRoute(t, st, types.CapabilityRoute{
		Models: []string{"deepseek-chat"}, Capability: capabilityName, Route: types.RouteProxy,
		Replacements: []types.SensitiveReplacement{{From: "你好", To: "给我讲一个笑话"}},
	})
	// 模拟 python json.dumps ensure_ascii=True：中文转成 \uXXXX 字面量。
	pipe := &modelgateway.ProxyPipeline{
		Request: &modelgateway.ProxyRequest{
			Body:  []byte(`{"model":"deepseek-chat","messages":[{"role":"user","content":"\u4f60\u597d"}]}`),
			Model: "deepseek-chat",
		},
		Metadata: map[string]any{},
	}
	out, err := runHook(svc, pipe)
	if err != nil {
		t.Fatalf("Hook 出错: %v", err)
	}
	if out == nil {
		t.Fatal("应返回改写后的管线")
	}
	if !json.Valid(out.Request.Body) {
		t.Fatalf("替换后 JSON 非法: %s", out.Request.Body)
	}
	body := string(out.Request.Body)
	if contains(body, "给我讲一个笑话") {
		t.Logf("替换成功: %s", body)
	} else {
		t.Fatalf("结构化替换未命中（body 里应有「给我讲一个笑话」）: %s", body)
	}
	// 原「你好」不应再以明文存在（\u4f60\u597d 或 你好 都不行）。
	if contains(body, "你好") || contains(body, "\\u4f60\\u597d") {
		t.Fatalf("敏感词仍存在: %s", body)
	}
}

// TestProxyNestedText 验证嵌套字符串值（system prompt、tool 参数等任意位置）也会被替换。
func TestProxyNestedText(t *testing.T) {
	svc, st := newTestService(t)
	seedRoute(t, st, types.CapabilityRoute{
		Models: []string{"deepseek-chat"}, Capability: capabilityName, Route: types.RouteProxy,
		Replacements: []types.SensitiveReplacement{{From: "违禁", To: "***"}},
	})
	pipe := proxyPipe(t, map[string]any{
		"model": "deepseek-chat",
		"messages": []any{
			map[string]any{"role": "system", "content": "禁止说违禁内容"},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "这里也有违禁词"},
				map[string]any{"type": "image_url", "image_url": map[string]string{"url": "http://a.com/x.png"}},
			}},
		},
	})
	out, err := runHook(svc, pipe)
	if err != nil {
		t.Fatalf("Hook 出错: %v", err)
	}
	body := string(out.Request.Body)
	if contains(body, "违禁") {
		t.Fatalf("嵌套文本未被替换: %s", body)
	}
	if !contains(body, "***") {
		t.Fatalf("替换内容未写入: %s", body)
	}
	if !contains(body, "image_url") {
		t.Fatalf("图片块不应丢失: %s", body)
	}
	if !json.Valid(out.Request.Body) {
		t.Fatalf("替换后 JSON 非法: %s", body)
	}
}
