package modelgateway

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLookupOpenRouterContext(t *testing.T) {
	// 与 loadOpenRouterContext 输出一致：完整 id 与其裸模型名都作键。
	meta := map[string]openrouterModelMeta{
		"deepseek/deepseek-v4-flash": {Context: 1000000, Output: 943718, Reasoning: true},
		"deepseek-v4-flash":          {Context: 1000000, Output: 943718, Reasoning: true},
		"openai/gpt-4o":              {Context: 128000, Vision: true},
		"gpt-4o":                     {Context: 128000, Vision: true},
		"gpt-4o-mini":                {Context: 128000, Vision: true},
	}
	cases := []struct {
		model    string
		wantCtx  int64
		wantOK   bool
		wantVis  bool
		wantReas bool
	}{
		{"deepseek-v4-flash", 1000000, true, false, true}, // 无前缀 → 裸名命中
		{"DeepSeek-v4-flash", 1000000, true, false, true}, // 大小写不敏感
		{"openai/gpt-4o", 128000, true, true, false},      // 完整 id 命中
		{"gpt-4o", 128000, true, true, false},             // 裸名命中
		{"gpt-4o-mini", 128000, true, true, false},        // 裸名键命中
		{"unknown-model", 0, false, false, false},         // 查不到 → miss
		{"", 0, false, false, false},                      // 空名 → miss
	}
	for _, c := range cases {
		got, ok := lookupOpenRouterContext(meta, c.model)
		if !ok {
			if c.wantOK {
				t.Errorf("lookupOpenRouterContext(%q) 未命中，应为命中", c.model)
			}
			continue
		}
		if !c.wantOK {
			t.Errorf("lookupOpenRouterContext(%q) 命中了 miss 用例", c.model)
			continue
		}
		if got.Context != c.wantCtx {
			t.Errorf("lookupOpenRouterContext(%q).Context = %d, want %d", c.model, got.Context, c.wantCtx)
		}
		if got.Vision != c.wantVis {
			t.Errorf("lookupOpenRouterContext(%q).Vision = %v, want %v", c.model, got.Vision, c.wantVis)
		}
		if got.Reasoning != c.wantReas {
			t.Errorf("lookupOpenRouterContext(%q).Reasoning = %v, want %v", c.model, got.Reasoning, c.wantReas)
		}
	}
	if _, ok := lookupOpenRouterContext(nil, "gpt-4o"); ok {
		t.Error("nil 映射应返回 miss")
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

	// 写一份合法缓存：含上下文/输出/能力全字段；context 为 0 但有能力的条目也保留。
	content := `[
	  {"id":"deepseek/deepseek-v4-flash","name":"DeepSeek V4 Flash","context":1000000,"output":943718,"vision":false,"reasoning":true},
	  {"id":"openai/gpt-4o","name":"GPT-4o","context":128000,"output":16384,"vision":true,"reasoning":false}
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
	flash, ok := got["deepseek-v4-flash"]
	if !ok || flash.Context != 1000000 || flash.Output != 943718 || flash.Reasoning != true || flash.Vision != false {
		t.Fatalf("deepseek 裸名键解析错误: %+v ok=%v", flash, ok)
	}
	if full, ok := got["deepseek/deepseek-v4-flash"]; !ok || full.Context != 1000000 {
		t.Fatalf("deepseek 完整 id 键解析错误: %+v ok=%v", full, ok)
	}
	if gpt, ok := got["gpt-4o"]; !ok || gpt.Context != 128000 || gpt.Vision != true {
		t.Fatalf("gpt-4o 解析错误: %+v ok=%v", gpt, ok)
	}
}
