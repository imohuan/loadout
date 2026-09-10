package unifyai

import (
	"encoding/json"
	"os"
	"testing"
)

// TestEnrichAgainstRealCache 用本机真实的 OpenRouter 元数据缓存验证「同步时」的补全效果。
// 缓存不存在（CI / 新机器）时跳过，不作为失败。
func TestEnrichAgainstRealCache(t *testing.T) {
	if _, err := os.Stat(metadataCachePath()); err != nil {
		t.Skip("本机无 OpenRouter 元数据缓存，跳过")
	}
	byID, bySlug := openRouterContextIndex()
	if len(byID) == 0 {
		t.Fatalf("真实缓存解析为空：%s", metadataCachePath())
	}
	t.Logf("缓存条目 %d（裸名索引 %d）", len(byID), len(bySlug))

	// 模拟 opencodex 报回自家网关的模型 id（不含 provider 前缀），上下文为 0。
	res := OpenCodexModelsResult{Models: []OpenCodexModel{
		{Provider: "Loadout", ModelID: "deepseek-v4-flash"},
		{Provider: "Loadout", ModelID: "glm-5.2"},
	}}
	enrichFromOpenRouter(&res)
	filled := 0
	for _, m := range res.Models {
		if m.ContextWindow > 0 {
			filled++
			t.Logf("%s → context %d", m.ModelID, m.ContextWindow)
		}
	}
	if filled == 0 {
		t.Errorf("真实缓存下没有任何模型补到上下文：%+v", res.Models)
	}
	raw, _ := json.Marshal(res.Models[0])
	t.Logf("示例: %s", raw)
}
