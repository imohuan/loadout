package messageinject

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

// proxyPipe 构造一个带 JSON body 的透明代理管线。
func proxyPipe(t *testing.T, body any) *modelgateway.ProxyPipeline {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return &modelgateway.ProxyPipeline{
		Request:  &modelgateway.ProxyRequest{Body: raw, Model: "deepseek-chat"},
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

// decodeBody 把管线 body 解析为 map 供断言。
func decodeBody(t *testing.T, pipe *modelgateway.ProxyPipeline) map[string]any {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(pipe.Request.Body, &root); err != nil {
		t.Fatalf("解析 body 失败: %v", err)
	}
	return root
}

// messages 从 body 取 messages 数组。
func messages(t *testing.T, root map[string]any) []any {
	t.Helper()
	raw, ok := root["messages"]
	if !ok {
		t.Fatalf("body 无 messages")
	}
	arr, ok := raw.([]any)
	if !ok {
		t.Fatalf("messages 非数组")
	}
	return arr
}

// TestDecideRoute 验证路由命中/未命中/渠道约束/通配。
func TestDecideRoute(t *testing.T) {
	svc, st := newTestService(t)
	routes := []types.CapabilityRoute{
		{Models: []string{"deepseek-chat"}, Capability: capabilityName, Route: types.RouteProxy,
			Injections: []types.MessageInjection{{Role: "system", Content: "你好", Position: types.InjectPrepend}}},
		{Models: []string{"gpt-5"}, ChannelIDs: []string{"ch-b"}, Capability: capabilityName, Route: types.RouteProxy,
			Injections: []types.MessageInjection{{Role: "user", Content: "x", Position: types.InjectAppend}}},
		{Models: []string{"claude-x"}, ChannelIDs: []string{"*"}, Capability: capabilityName, Route: types.RouteNative},
	}
	if err := st.Write(types.FileCapabilityRoutes, routes); err != nil {
		t.Fatalf("写能力路由表失败: %v", err)
	}

	pipe := proxyPipe(t, map[string]any{
		"model":    "deepseek-chat",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	})
	// 通过 store 模式的路由判定验证（pipe.Request.Model = deepseek-chat）。
	r, err := svc.decideRoutes(pipe, "")
	if err != nil || len(r) != 1 {
		t.Fatalf("应命中 1 条 proxy 路由: %v %v", r, err)
	}

	// 未命中模型（需改 pipe.Request.Model，proxyPipe 固定为 deepseek-chat）
	pipe2 := proxyPipe(t, map[string]any{"model": "nope", "messages": []any{}})
	pipe2.Request.Model = "nope"
	if r, _ := svc.decideRoutes(pipe2, ""); len(r) != 0 {
		t.Fatalf("不应命中: %+v", r)
	}
}

// TestInjectPrependAppend 验证新增消息到首尾。
func TestInjectPrependAppend(t *testing.T) {
	svc, st := newTestService(t)
	routes := []types.CapabilityRoute{{
		Models: []string{"deepseek-chat"}, Capability: capabilityName, Route: types.RouteProxy,
		Injections: []types.MessageInjection{
			{Role: "system", Content: "开头约束", Position: types.InjectPrepend},
			{Role: "assistant", Content: "结尾提示", Position: types.InjectAppend},
		},
	}}
	if err := st.Write(types.FileCapabilityRoutes, routes); err != nil {
		t.Fatalf("写路由失败: %v", err)
	}

	pipe := proxyPipe(t, map[string]any{
		"model":    "deepseek-chat",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	})
	out, err := runHook(svc, pipe)
	if err != nil {
		t.Fatalf("hook 出错: %v", err)
	}
	msgs := messages(t, decodeBody(t, out))
	if len(msgs) != 3 {
		t.Fatalf("应有 3 条消息，实际 %d: %+v", len(msgs), msgs)
	}
	first := msgs[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "开头约束" {
		t.Fatalf("第一条应为注入的 system: %+v", first)
	}
	last := msgs[2].(map[string]any)
	if last["role"] != "assistant" || last["content"] != "结尾提示" {
		t.Fatalf("最后一条应为注入的 assistant: %+v", last)
	}
	mid := msgs[1].(map[string]any)
	if mid["content"] != "hi" {
		t.Fatalf("原始消息应保留: %+v", mid)
	}
}

// TestInjectFirstMerge 验证 prepend_first / append_first 拼接到原始第一条。
func TestInjectFirstMerge(t *testing.T) {
	svc, st := newTestService(t)
	routes := []types.CapabilityRoute{{
		Models: []string{"deepseek-chat"}, Capability: capabilityName, Route: types.RouteProxy,
		Injections: []types.MessageInjection{
			{Role: "user", Content: "【开头】", Position: types.InjectPrependFirst},
			{Role: "user", Content: "【结尾】", Position: types.InjectAppendFirst},
		},
	}}
	if err := st.Write(types.FileCapabilityRoutes, routes); err != nil {
		t.Fatalf("写路由失败: %v", err)
	}

	pipe := proxyPipe(t, map[string]any{
		"model":    "deepseek-chat",
		"messages": []any{map[string]any{"role": "user", "content": "原始内容"}},
	})
	out, err := runHook(svc, pipe)
	if err != nil {
		t.Fatalf("hook 出错: %v", err)
	}
	msgs := messages(t, decodeBody(t, out))
	if len(msgs) != 1 {
		t.Fatalf("应仍只有 1 条消息: %+v", msgs)
	}
	first := msgs[0].(map[string]any)
	if first["content"] != "【开头】原始内容【结尾】" {
		t.Fatalf("内容应拼接为 开头+原始+结尾: %q", first["content"])
	}
}

// TestInjectFirstMergeParts 验证分段 content（数组）的 prepend_first / append_first。
func TestInjectFirstMergeParts(t *testing.T) {
	svc, st := newTestService(t)
	routes := []types.CapabilityRoute{{
		Models: []string{"deepseek-chat"}, Capability: capabilityName, Route: types.RouteProxy,
		Injections: []types.MessageInjection{
			{Role: "user", Content: "前缀", Position: types.InjectPrependFirst},
			{Role: "user", Content: "后缀", Position: types.InjectAppendFirst},
		},
	}}
	if err := st.Write(types.FileCapabilityRoutes, routes); err != nil {
		t.Fatalf("写路由失败: %v", err)
	}

	pipe := proxyPipe(t, map[string]any{
		"model": "deepseek-chat",
		"messages": []any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "中"},
			map[string]any{"type": "image_url", "image_url": "http://img"},
		}}},
	})
	out, err := runHook(svc, pipe)
	if err != nil {
		t.Fatalf("hook 出错: %v", err)
	}
	msgs := messages(t, decodeBody(t, out))
	first := msgs[0].(map[string]any)
	parts := first["content"].([]any)
	if len(parts) != 4 {
		t.Fatalf("应有 4 个分段: %+v", parts)
	}
	if parts[0].(map[string]any)["text"] != "前缀" {
		t.Fatalf("第一段应为前缀: %+v", parts[0])
	}
	if parts[1].(map[string]any)["text"] != "中" {
		t.Fatalf("第二段应为中: %+v", parts[1])
	}
	if parts[2].(map[string]any)["type"] != "image_url" {
		t.Fatalf("图片块应保留在第三段: %+v", parts[2])
	}
	if parts[3].(map[string]any)["text"] != "后缀" {
		t.Fatalf("第四段应为后缀: %+v", parts[3])
	}
}

// TestNativePassthrough 验证 native 路由透传。
func TestNativePassthrough(t *testing.T) {
	svc, st := newTestService(t)
	routes := []types.CapabilityRoute{{
		Models: []string{"deepseek-chat"}, Capability: capabilityName, Route: types.RouteNative,
		Injections: []types.MessageInjection{{Role: "system", Content: "x", Position: types.InjectPrepend}},
	}}
	if err := st.Write(types.FileCapabilityRoutes, routes); err != nil {
		t.Fatalf("写路由失败: %v", err)
	}
	pipe := proxyPipe(t, map[string]any{
		"model":    "deepseek-chat",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	})
	orig := string(pipe.Request.Body)
	out, err := runHook(svc, pipe)
	if err != nil {
		t.Fatalf("hook 出错: %v", err)
	}
	if string(out.Request.Body) != orig {
		t.Fatalf("native 应原样透传")
	}
}

// TestNoMessages 验证无 messages 且无 input 时透传不报错。
func TestNoMessages(t *testing.T) {
	svc, st := newTestService(t)
	routes := []types.CapabilityRoute{{
		Models: []string{"deepseek-chat"}, Capability: capabilityName, Route: types.RouteProxy,
		Injections: []types.MessageInjection{{Role: "system", Content: "x", Position: types.InjectPrepend}},
	}}
	if err := st.Write(types.FileCapabilityRoutes, routes); err != nil {
		t.Fatalf("写路由失败: %v", err)
	}
	pipe := proxyPipe(t, map[string]any{"model": "deepseek-chat", "stream": true})
	orig := string(pipe.Request.Body)
	out, err := runHook(svc, pipe)
	if err != nil {
		t.Fatalf("hook 出错: %v", err)
	}
	if string(out.Request.Body) != orig {
		t.Fatalf("无 messages 应透传: %q", string(out.Request.Body))
	}
}

// TestInjectFirstNoFirst 验证原始第一条不存在时 prepend_first 退化为新增首项。
func TestInjectFirstNoFirst(t *testing.T) {
	svc, st := newTestService(t)
	routes := []types.CapabilityRoute{{
		Models: []string{"deepseek-chat"}, Capability: capabilityName, Route: types.RouteProxy,
		Injections: []types.MessageInjection{{Role: "system", Content: "约束", Position: types.InjectPrependFirst}},
	}}
	if err := st.Write(types.FileCapabilityRoutes, routes); err != nil {
		t.Fatalf("写路由失败: %v", err)
	}
	pipe := proxyPipe(t, map[string]any{"model": "deepseek-chat", "messages": []any{}})
	out, err := runHook(svc, pipe)
	if err != nil {
		t.Fatalf("hook 出错: %v", err)
	}
	msgs := messages(t, decodeBody(t, out))
	if len(msgs) != 1 {
		t.Fatalf("应新增 1 条消息: %+v", msgs)
	}
	if msgs[0].(map[string]any)["content"] != "约束" {
		t.Fatalf("内容不对: %+v", msgs[0])
	}
}

// TestNonJSONPassthrough 验证非 JSON body 透传。
func TestNonJSONPassthrough(t *testing.T) {
	svc, st := newTestService(t)
	routes := []types.CapabilityRoute{{
		Models: []string{"deepseek-chat"}, Capability: capabilityName, Route: types.RouteProxy,
		Injections: []types.MessageInjection{{Role: "system", Content: "x", Position: types.InjectPrepend}},
	}}
	if err := st.Write(types.FileCapabilityRoutes, routes); err != nil {
		t.Fatalf("写路由失败: %v", err)
	}
	pipe := proxyPipe(t, "not json at all")
	orig := string(pipe.Request.Body)
	out, err := runHook(svc, pipe)
	if err != nil {
		t.Fatalf("hook 出错: %v", err)
	}
	if string(out.Request.Body) != orig {
		t.Fatalf("非 JSON 应透传")
	}
}

// TestVirtualModelMatch 验证虚拟模型（聚合）请求：路由配虚拟前缀 `git-*`，真实模型不匹配、
// 但 __virtual_model 命中时仍命中。
func TestVirtualModelMatch(t *testing.T) {
	svc, st := newTestService(t)
	routes := []types.CapabilityRoute{{
		Models: []string{"git-*"}, Capability: capabilityName, Route: types.RouteProxy,
		Injections: []types.MessageInjection{{Role: "system", Content: "Git 约束", Position: types.InjectPrepend}},
	}}
	if err := st.Write(types.FileCapabilityRoutes, routes); err != nil {
		t.Fatalf("写路由失败: %v", err)
	}

	pipe := proxyPipe(t, map[string]any{
		"model":    "deepseek-v4-flash",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	})
	pipe.Request.Model = "deepseek-v4-flash"
	// 无虚拟模型：真实模型 deepseek-v4-flash 不匹配 git-*，应不命中。
	if r, _ := svc.decideRoutes(pipe, ""); len(r) != 0 {
		t.Fatalf("无虚拟模型不应命中: %+v", r)
	}
	// 有虚拟模型 git-xxx：命中。
	pipe.Metadata["__virtual_model"] = "git-xxx"
	r, err := svc.decideRoutes(pipe, types.VirtualModelFromMetadata(pipe.Metadata))
	if err != nil || len(r) != 1 {
		t.Fatalf("虚拟模型应命中 1 条: %v %v", r, err)
	}
	// 端到端 hook：注入生效。
	out, err := runHook(svc, pipe)
	if err != nil {
		t.Fatalf("hook 出错: %v", err)
	}
	msgs := messages(t, decodeBody(t, out))
	if len(msgs) != 2 {
		t.Fatalf("应注入 1 条，共 2 条: %+v", msgs)
	}
	if msgs[0].(map[string]any)["content"] != "Git 约束" {
		t.Fatalf("首条应为注入内容: %+v", msgs[0])
	}
}


// TestFailoverNoDuplicateInjection 验证聚合 failover 多次渠道尝试不叠加注入：
// 每次 attempt 都应基于【原始请求体】注入，而非基于上一次 attempt 注入后的 body
// （"接力棒"效应）。模拟同一 pipe 连续 4 次 attempt，注入内容应只有 1 份。
func TestFailoverNoDuplicateInjection(t *testing.T) {
	svc, st := newTestService(t)
	routes := []types.CapabilityRoute{{
		Models: []string{"*"}, Capability: capabilityName, Route: types.RouteProxy,
		Injections: []types.MessageInjection{{Role: "system", Content: "使用中文交流", Position: types.InjectPrepend}},
	}}
	if err := st.Write(types.FileCapabilityRoutes, routes); err != nil {
		t.Fatalf("写路由失败: %v", err)
	}
	pipe := proxyPipe(t, map[string]any{
		"model":    "deepseek-chat",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	})
	// 模拟聚合 failover：同一 pipe 多次渠道尝试，每次把改写后的 body 传回下一次。
	for i := 0; i < 4; i++ {
		out, err := runHook(svc, pipe)
		if err != nil {
			t.Fatalf("attempt %d 出错: %v", i, err)
		}
		pipe = out
	}
	msgs := messages(t, decodeBody(t, pipe))
	count := 0
	for _, m := range msgs {
		if mm, ok := m.(map[string]any); ok {
			if mm["content"] == "使用中文交流" {
				count++
			}
		}
	}
	if count != 1 {
		t.Fatalf("failover 后注入内容应只有 1 份，实际 %d 份: %+v", count, msgs)
	}
}
