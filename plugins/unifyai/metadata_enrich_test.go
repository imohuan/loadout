package unifyai

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestEnrichFromOpenRouterFillsContext 验证代理没给上下文时，用本地 OpenRouter 缓存
// 按模型名补上上下文窗口（修复「同步后 OpenCodex 模型显示 0，刷新元数据才有效」）。
func TestEnrichFromOpenRouterFillsContext(t *testing.T) {
	withMetadataCache(t, `[
		{"id":"anthropic/claude-sonnet-5-20260101","name":"Claude Sonnet 5","context":200000,"output":64000,"vision":true,"reasoning":true},
		{"id":"deepseek/deepseek-v4-flash","name":"DeepSeek V4 Flash","context":131072,"output":32000,"vision":false,"reasoning":true}
	]`)

	res := OpenCodexModelsResult{
		Degraded: true,
		Models: []OpenCodexModel{
			{Provider: "Loadout", ModelID: "claude-sonnet-5"},
			{Provider: "Loadout", ModelID: "deepseek-v4-flash", ContextWindow: 4096},
			{Provider: "Loadout", ModelID: "unknown-model"},
		},
	}
	enrichFromOpenRouter(&res)

	if got := res.Models[0].ContextWindow; got != 200000 {
		t.Errorf("claude-sonnet-5 contextWindow = %d, want 200000（裸名匹配到日期快照条目）", got)
	}
	if got := res.Models[1].ContextWindow; got != 4096 {
		t.Errorf("已有 contextWindow 不应被覆盖: got %d, want 4096", got)
	}
	if got := res.Models[2].ContextWindow; got != 0 {
		t.Errorf("未知模型 contextWindow = %d, want 0（无匹配保持原样）", got)
	}
}

// TestEnrichFromOpenRouterSkipsWhenComplete 验证模型已全部带上下文时不读缓存、不改动。
func TestEnrichFromOpenRouterSkipsWhenComplete(t *testing.T) {
	withMetadataCache(t, `[{"id":"x/y","context":1,"output":0,"vision":false,"reasoning":false}]`)
	res := OpenCodexModelsResult{Models: []OpenCodexModel{{Provider: "p", ModelID: "x/y", ContextWindow: 123}}}
	enrichFromOpenRouter(&res)
	if res.Models[0].ContextWindow != 123 {
		t.Errorf("contextWindow = %d, want 123（无需补全）", res.Models[0].ContextWindow)
	}
}

// TestEnrichFromOpenRouterMissingCache 验证缓存缺失/损坏时不 panic、不改动模型。
func TestEnrichFromOpenRouterMissingCache(t *testing.T) {
	old := metadataCachePath
	defer func() { metadataCachePath = old }()
	metadataCachePath = func() string { return filepath.Join(t.TempDir(), "missing.json") }

	res := OpenCodexModelsResult{Models: []OpenCodexModel{{Provider: "p", ModelID: "a/b"}}}
	enrichFromOpenRouter(&res)
	if res.Models[0].ContextWindow != 0 {
		t.Errorf("无缓存时 contextWindow = %d, want 0", res.Models[0].ContextWindow)
	}
}

// TestOpenRouterSlug 验证名字归一化（去 provider 前缀、变体后缀、日期后缀）。
func TestOpenRouterSlug(t *testing.T) {
	cases := map[string]string{
		"anthropic/claude-sonnet-5-20260101": "claude-sonnet-5",
		"deepseek/deepseek-v4-flash":         "deepseek-v4-flash",
		"z-ai/glm-5:free":                    "glm-5",
		"gpt-5.6":                            "gpt-5.6",
	}
	for in, want := range cases {
		if got := openRouterSlug(in); got != want {
			t.Errorf("openRouterSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

// withMetadataCache 把 OpenRouter 元数据缓存指向临时文件，写入给定 JSON。
func withMetadataCache(t *testing.T, body string) {
	t.Helper()
	old := metadataCachePath
	t.Cleanup(func() { metadataCachePath = old })
	p := filepath.Join(t.TempDir(), "openrouter-models.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("写入缓存: %v", err)
	}
	metadataCachePath = func() string { return p }
	// 顺带确认测试缓存可被 ModelSource 解析（字段名与真实缓存一致）。
	var metas []OpenRouterMeta
	if err := json.Unmarshal([]byte(body), &metas); err != nil {
		t.Fatalf("测试缓存不是合法 OpenRouterMeta 数组: %v", err)
	}
}
