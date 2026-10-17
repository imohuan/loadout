package types

import "testing"

// 「顺序即优先级」是用户确认的最终标准：多条同类 proxy 路由命中时，
// 表格里排在最上面的那条生效，不按匹配具体度自动排序。
//
// 这条测试复现了真实事故：同一个模型同时有通配 * 兜底规则和精确模型名规则，
// 谁在表格上面谁生效（用户拖到下面后图片被打码）。行为随顺序翻转是**预期**的，
// 所以这里锁定的是「按表格顺序」，而不是「精确优先」。
func TestSelectCapabilityRoutesFollowsTableOrder(t *testing.T) {
	specific := CapabilityRoute{
		Capability: "request_log", Route: RouteProxy,
		Models: []string{"step-5-preview"}, RequestLogKeepBase64: true,
	}
	wildcard := CapabilityRoute{
		Capability: "request_log", Route: RouteProxy,
		Models: []string{"*"}, RequestLogKeepBase64: false,
	}

	t.Run("精确在前", func(t *testing.T) {
		got := SelectCapabilityRoutes([]CapabilityRoute{specific, wildcard}, "request_log", "step-5-preview", ChannelRequestScope{})
		if len(got) != 2 {
			t.Fatalf("应命中 2 条，实际 %d", len(got))
		}
		if !got[0].RequestLogKeepBase64 {
			t.Fatal("表格第一条是精确规则时应由它生效")
		}
	})

	t.Run("通配在前", func(t *testing.T) {
		got := SelectCapabilityRoutes([]CapabilityRoute{wildcard, specific}, "request_log", "step-5-preview", ChannelRequestScope{})
		if len(got) != 2 {
			t.Fatalf("应命中 2 条，实际 %d", len(got))
		}
		if got[0].RequestLogKeepBase64 {
			t.Fatal("表格第一条是通配规则时应由它生效（顺序即优先级，不做具体度排序）")
		}
	})
}

// 顺序在收集阶段就保持表内原样，不做任何重排。
func TestSelectCapabilityRoutesPreservesInputOrder(t *testing.T) {
	routes := []CapabilityRoute{
		{Capability: "request_log", Route: RouteProxy, Models: []string{"git-*"}},
		{Capability: "request_log", Route: RouteProxy, Models: []string{"*"}},
		{Capability: "request_log", Route: RouteProxy, Models: []string{"git-commit"}},
	}
	got := SelectCapabilityRoutes(routes, "request_log", "git-commit", ChannelRequestScope{})
	if len(got) != 3 {
		t.Fatalf("应命中 3 条，实际 %d", len(got))
	}
	want := []string{"git-*", "*", "git-commit"}
	for i, w := range want {
		if got[i].Models[0] != w {
			t.Fatalf("第 %d 条应为 %q（保持表格顺序），实际 %q", i, w, got[i].Models[0])
		}
	}
}

// native 短路优先于所有 proxy 规则，不受顺序影响（这是独立于排序的语义）。
func TestSelectCapabilityRoutesNativeStillShortCircuits(t *testing.T) {
	routes := []CapabilityRoute{
		{Capability: "request_log", Route: RouteProxy, Models: []string{"step-5-preview"}},
		{Capability: "request_log", Route: RouteNative, Models: []string{"step-5-preview"}},
	}
	got := SelectCapabilityRoutes(routes, "request_log", "step-5-preview", ChannelRequestScope{})
	if len(got) != 1 || got[0].Route != RouteNative {
		t.Fatal("native 应短路返回，屏蔽同模型的 proxy 规则")
	}
}

