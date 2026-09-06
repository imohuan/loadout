package modelgateway

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLookupOpenRouterContext(t *testing.T) {
	// 与 loadOpenRouterContext 输出一致：完整 id 与其裸模型名都作键。
	meta := map[string]int64{
		"deepseek/deepseek-v4-flash": 1000000,
		"deepseek-v4-flash":          1000000,
		"openai/gpt-4o":              128000,
		"gpt-4o":                     128000,
		"gpt-4o-mini":                128000,
	}
	cases := []struct {
		model string
		want  int64
	}{
		{"deepseek-v4-flash", 1000000},   // 无前缀 → 裸名命中
		{"DeepSeek-v4-flash", 1000000},   // 大小写不敏感
		{"openai/gpt-4o", 128000},        // 完整 id 命中
		{"gpt-4o", 128000},               // 裸名命中
		{"gpt-4o-mini", 128000},          // 裸名键命中
		{"unknown-model", 0},             // 查不到 → 0
		{"", 0},                          // 空名 → 0
	}
	for _, c := range cases {
		if got := lookupOpenRouterContext(meta, c.model); got != c.want {
			t.Errorf("lookupOpenRouterContext(%q) = %d, want %d", c.model, got, c.want)
		}
	}
	if got := lookupOpenRouterContext(nil, "gpt-4o"); got != 0 {
		t.Errorf("nil 映射应返回 0, got %d", got)
	}
}

func TestLoadOpenRouterContext(t *testing.T) {
	dir := t.TempDir()
	// 指向不存在的缓存 → nil，不报错。
	old := openrouterCachePath
	openrouterCachePath = func() string { return filepath.Join(dir, "missing.json") }
	defer func() { openrouterCachePath = old }()
	if got := loadOpenRouterContext(); got != nil {
		t.Fatalf("缺失缓存应返回 nil, got %v", got)
	}

	// 写一份合法缓存。
	content := `[
	  {"id":"deepseek/deepseek-v4-flash","name":"DeepSeek V4 Flash","context":1000000},
	  {"id":"openai/gpt-4o","name":"GPT-4o","context":128000}
	]`
	path := filepath.Join(dir, "openrouter-models.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	openrouterCachePath = func() string { return path }
	got := loadOpenRouterContext()
	if got == nil {
		t.Fatal("合法缓存应返回映射")
	}
	if got["deepseek-v4-flash"] != 1000000 || got["deepseek/deepseek-v4-flash"] != 1000000 {
		t.Fatalf("deepseek 上下文解析错误: %v", got)
	}
	if got["gpt-4o"] != 128000 {
		t.Fatalf("gpt-4o 上下文解析错误: %v", got)
	}
}
