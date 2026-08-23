package types

import (
	"encoding/json"
	"testing"
)

func TestMatchChannelScope(t *testing.T) {
	cases := []struct {
		name      string
		ids       []string
		baseURLs  []string
		channelID string
		reqBase   string
		want      bool
	}{
		// 全渠道 / 通配
		{"both empty = all", nil, nil, "", "", true},
		{"both empty = all any channel", nil, nil, "ch-a", "https://a/v1", true},
		{"star = all", []string{"*"}, nil, "", "", true},
		{"star = all any channel", []string{"*"}, nil, "ch-z", "", true},

		// Key 级
		{"key exact hit", []string{"ch-b"}, nil, "ch-b", "https://b/v1", true},
		{"key miss", []string{"ch-b"}, nil, "ch-a", "https://a/v1", false},
		{"key unknown channel", []string{"ch-b"}, nil, "", "", false},

		// 渠道级
		{"base url hit", nil, []string{"https://b/v1"}, "ch-b", "https://b/v1", true},
		{"base url hit trailing slash", nil, []string{"https://b/v1/"}, "ch-b", "https://b/v1", true},
		{"base url miss", nil, []string{"https://b/v1"}, "ch-a", "https://a/v1", false},
		// 关键：纯渠道级（channel_ids 空）不能被 MatchChannel 的「空=全渠道」误判为全渠道。
		{"pure channel-level not all", nil, []string{"https://b/v1"}, "ch-a", "https://a/v1", false},
		{"pure channel-level unknown", nil, []string{"https://b/v1"}, "", "", false},

		// 并存（渠道级 + Key 级互斥，不同渠道）
		{"mixed key hit", []string{"ch-k"}, []string{"https://b/v1"}, "ch-k", "", true},
		{"mixed base hit", []string{"ch-k"}, []string{"https://b/v1"}, "ch-b", "https://b/v1", true},
		{"mixed miss", []string{"ch-k"}, []string{"https://b/v1"}, "ch-x", "https://x/v1", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := MatchChannelScope(c.ids, c.baseURLs, c.channelID, c.reqBase)
			if got != c.want {
				t.Fatalf("MatchChannelScope(%v, %v, %q, %q) = %v, want %v",
					c.ids, c.baseURLs, c.channelID, c.reqBase, got, c.want)
			}
		})
	}
}

func TestChannelBaseURLMatches(t *testing.T) {
	if ChannelBaseURLMatches([]string{"https://a/v1"}, "https://a/v1/") != true {
		t.Fatal("尾斜杠应归一化后命中")
	}
	if ChannelBaseURLMatches(nil, "https://a/v1") != false {
		t.Fatal("空渠道级列表不应命中")
	}
	if ChannelBaseURLMatches([]string{"https://a/v1"}, "") != false {
		t.Fatal("请求 base_url 为空不应命中")
	}
}

// resolveForTest 模拟插件侧 requestChannelBaseURLs 闭包：
// key id 精确匹配返回该渠道 base_url；渠道名 ChannelName 返回组内全部 base_urls。
func resolveForTest(term string) []string {
	switch term {
	case "df3f297543aebb94", "workbuddy": // workbuddy 组的 key id 与渠道名
		return []string{"https://copilot.tencent.com/v2"}
	case "ch-other":
		return []string{"https://other.example/v1"}
	}
	return nil
}

// TestChannelScopeFromMetadataHintFallback 修复目标：入口阶段（ProxyBeforeUpstream）
// 只有 __channel_hint（v2 前缀渠道名），__current_channel / __current_channel_base_url
// 尚未写入。ChannelScopeFromMetadata 必须兜底用 hint 反查渠道组 base_urls，
// 否则渠道级约束（channel_base_urls）的路由永远匹配不到，被全渠道兜底路由抢走。
func TestChannelScopeFromMetadataHintFallback(t *testing.T) {
	scope := ChannelScopeFromMetadata(map[string]any{
		"__channel_hint": "workbuddy",
	}, resolveForTest)
	if len(scope.IDs) != 0 {
		t.Fatalf("hint 兜底不应产生 IDs（未指定具体 key），实际 %v", scope.IDs)
	}
	if len(scope.BaseURLs) != 1 || scope.BaseURLs[0] != "https://copilot.tencent.com/v2" {
		t.Fatalf("hint 兜底反查渠道组 base_urls 失败: %+v", scope.BaseURLs)
	}
	// 组合验证：channel_base_urls 非空的渠道级路由（如 workbuddy 原生透传）应命中
	if !MatchChannelScopeEx(nil, []string{"https://copilot.tencent.com/v2"}, scope) {
		t.Fatal("渠道级约束路由应命中 hint 解析出的 scope")
	}
	// 全渠道路由（无约束）仍命中
	if !MatchChannelScopeEx(nil, nil, scope) {
		t.Fatal("全渠道路由应始终命中")
	}
	// 不相关渠道的渠道级路由不应命中
	if MatchChannelScopeEx(nil, []string{"https://other.example/v1"}, scope) {
		t.Fatal("不相关渠道的渠道级路由不应命中")
	}
}

// TestChannelScopeFromMetadataHintNoOverride hint 兜底只在渠道字段全空时生效：
// __current_channel / __current_channel_base_url 已写入（ProxyBeforeAttempt 阶段）
// 时按原字段解析，hint 不追加不覆盖。
func TestChannelScopeFromMetadataHintNoOverride(t *testing.T) {
	scope := ChannelScopeFromMetadata(map[string]any{
		"__channel_hint":             "workbuddy",
		"__current_channel":          "ch-other",
		"__current_channel_base_url": "https://other.example/v1",
	}, resolveForTest)
	if len(scope.IDs) != 1 || scope.IDs[0] != "ch-other" {
		t.Fatalf("__current_channel 应优先于 hint: %+v", scope.IDs)
	}
	if len(scope.BaseURLs) != 1 || scope.BaseURLs[0] != "https://other.example/v1" {
		t.Fatalf("BaseURLs 应来自 __current_channel_base_url，hint 不追加: %+v", scope.BaseURLs)
	}
}

// TestChannelScopeFromMetadataCandidatesDedup BaseURLs 去重：candidates 反查的 base_url
// 与 __current_channel_base_url 同值（同一渠道组）时只保留一条。
// （注：__channel_hint 兜底只在 IDs/BaseURLs 全空时触发，此处有 candidates 不走 hint，
// 去重走的是 appendBaseURLs 的 containsString 检查。）
func TestChannelScopeFromMetadataCandidatesDedup(t *testing.T) {
	scope := ChannelScopeFromMetadata(map[string]any{
		"__channel_candidates":       []string{"df3f297543aebb94"},
		"__current_channel_base_url": "https://copilot.tencent.com/v2",
	}, resolveForTest)
	if len(scope.IDs) != 1 {
		t.Fatalf("候选 key 应进 IDs: %+v", scope.IDs)
	}
	if len(scope.BaseURLs) != 1 {
		t.Fatalf("candidates 反查与 __current_channel_base_url 同渠道组应去重为 1: %+v", scope.BaseURLs)
	}
	if scope.BaseURLs[0] != "https://copilot.tencent.com/v2" {
		t.Fatalf("BaseURLs = %v, want copilot.tencent.com/v2", scope.BaseURLs)
	}
}

func TestCapabilityRouteFieldRulesJSON(t *testing.T) {
	in := CapabilityRoute{
		Models:     []string{"*"},
		Capability: "field_filter",
		Route:      "proxy",
		FieldRules: &FieldRules{
			RequestStrip:  []string{"client_metadata", "a.b.c"},
			RequestKeep:   []string{"model", "messages"},
			ResponseStrip: []string{"choices.0.usage"},
			ResponseHeaderStrip:   []string{"X-Internal"},
		},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out CapabilityRoute
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.FieldRules == nil || len(out.FieldRules.RequestStrip) != 2 || out.FieldRules.RequestStrip[0] != "client_metadata" {
		t.Fatalf("FieldRules roundtrip 失败: %+v", out.FieldRules)
	}
	// 未配置时 FieldRules 应为 nil（omitempty 不输出，反序列化为 nil）
	var out2 CapabilityRoute
	if err := json.Unmarshal([]byte(`{"capability":"vision"}`), &out2); err != nil {
		t.Fatal(err)
	}
	if out2.FieldRules != nil {
		t.Fatalf("未配置 FieldRules 应为 nil，实际 %+v", out2.FieldRules)
	}
}

// TestSelectCapabilityRoutes 验证统一路由选择函数：
//   - 模型/渠道不匹配返回空；
//   - proxy 收集全部匹配项（叠加）；
//   - 非 proxy（native/历史 error）短路优先，不依赖 position 排序；
//   - 历史 route="error" 数据按短路返回（调用方按 native 透传降级）。
func TestSelectCapabilityRoutes(t *testing.T) {
	routes := []CapabilityRoute{
		// pos0: 全渠道 proxy（应被后面的 native 短路覆盖）
		{Models: []string{"hy*"}, Capability: "vision", Route: RouteProxy, ViaOptions: []ViaOption{{ViaModel: "m1"}}},
		// pos1: workbuddy 渠道 native（豁免优先，不依赖 position——放在 proxy 后面仍应赢）
		{Models: []string{"*"}, ChannelBaseURLs: []string{"https://copilot.tencent.com/v2"}, Capability: "vision", Route: RouteNative},
		// pos2: 历史 error 数据（等价 native，短路返回原数据）
		{Models: []string{"ds-*"}, Capability: "vision", Route: "error"},
		// pos3: field_filter proxy（不同 capability 不参与）
		{Models: []string{"hy*"}, Capability: "field_filter", Route: RouteProxy, FieldRules: &FieldRules{RequestStrip: []string{"x"}}},
	}
	scopeWorkbuddy := ChannelRequestScope{BaseURLs: []string{"https://copilot.tencent.com/v2"}}

	// 1. workbuddy 渠道 + hy3：native 短路优先（即使排在 proxy 后面）
	sel := SelectCapabilityRoutes(routes, "vision", "hy3", scopeWorkbuddy)
	if len(sel) != 1 || sel[0].Route != RouteNative {
		t.Fatalf("workbuddy hy3 应短路命中 native: %+v", sel)
	}

	// 2. 无渠道 + hy3：proxy 全收集（native 渠道约束不命中）
	sel = SelectCapabilityRoutes(routes, "vision", "hy3", ChannelRequestScope{})
	if len(sel) != 1 || sel[0].Route != RouteProxy {
		t.Fatalf("无渠道 hy3 应命中 proxy: %+v", sel)
	}

	// 3. 历史 error 数据：短路返回原数据（route="error"，调用方按 native 透传）
	sel = SelectCapabilityRoutes(routes, "vision", "ds-3", ChannelRequestScope{})
	if len(sel) != 1 || sel[0].Route != "error" {
		t.Fatalf("ds-* 历史 error 应短路返回: %+v", sel)
	}

	// 4. 不匹配能力/模型：空
	if sel = SelectCapabilityRoutes(routes, "vision", "unknown-model", ChannelRequestScope{}); len(sel) != 0 {
		t.Fatalf("未知模型应返回空: %+v", sel)
	}
	if sel = SelectCapabilityRoutes(routes, "sensitive_filter", "hy3", ChannelRequestScope{}); len(sel) != 0 {
		t.Fatalf("其他能力应返回空: %+v", sel)
	}

	// 5. 多 proxy 叠加（field_filter 场景：全部收集）
	routes2 := []CapabilityRoute{
		{Models: []string{"*"}, Capability: "field_filter", Route: RouteProxy, FieldRules: &FieldRules{RequestStrip: []string{"a"}}},
		{Models: []string{"hy*"}, Capability: "field_filter", Route: RouteProxy, FieldRules: &FieldRules{RequestKeep: []string{"b"}}},
	}
	sel = SelectCapabilityRoutes(routes2, "field_filter", "hy3", ChannelRequestScope{})
	if len(sel) != 2 {
		t.Fatalf("field_filter 多 proxy 应全部收集: %+v", sel)
	}
}
