package modelgateway

import (
	"os"
	"path/filepath"
	"testing"
)

// seedOpenRouterMatchCache 覆盖本地渠道命名到 OpenRouter 命名的全部已知差异形态。
func seedOpenRouterMatchCache(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "openrouter-models.json")
	content := `[
	  {"id":"bytedance-seed/seed-2.0-lite","name":"Seed 2.0 Lite","context":262144,"output":131072,"vision":true,"reasoning":true},
	  {"id":"bytedance-seed/seed-2.0-mini","name":"Seed 2.0 Mini","context":262144,"output":131072,"vision":true,"reasoning":true},
	  {"id":"bytedance-seed/seed-2.0-code","name":"Seed 2.0 Code","context":262144,"output":131072,"vision":true,"reasoning":true},
	  {"id":"bytedance-seed/seed-2-1-turbo","name":"Seed 2.1 Turbo","context":262144,"output":235929,"vision":true,"reasoning":true},
	  {"id":"tencent/hy3","name":"Hunyuan 3","context":256000,"output":32768,"vision":false,"reasoning":true},
	  {"id":"tencent/hy4-preview","name":"Hunyuan 4 Preview","context":1048576,"output":64000,"vision":false,"reasoning":true},
	  {"id":"z-ai/glm-5.2","name":"GLM 5.2","context":1048576,"output":128000,"vision":true,"reasoning":true},
	  {"id":"z-ai/glm-5.3-flash","name":"GLM 5.3 Flash","context":1310720,"output":131072,"vision":true,"reasoning":true},
	  {"id":"anthropic/claude-haiku-4.5","name":"Claude Haiku 4.5","context":200000,"output":64000,"vision":true,"reasoning":true},
	  {"id":"deepseek/deepseek-v4-flash-0731","name":"DS V4 Flash 0731","context":1310720,"output":943718,"vision":false,"reasoning":true},
	  {"id":"deepseek/deepseek-v4-pro-0813","name":"DS V4 Pro 0813","context":1048576,"output":384000,"vision":false,"reasoning":true},
	  {"id":"deepseek/deepseek-v4-flash-latest","name":"DS V4 Flash Latest","context":1048576,"output":384000,"vision":false,"reasoning":true},
	  {"id":"deepseek/deepseek-v4-pro","name":"DS V4 Pro","context":1048576,"output":384000,"vision":false,"reasoning":true}
	]`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	old := openrouterCachePath
	openrouterCachePath = func() string { return path }
	t.Cleanup(func() { openrouterCachePath = old })
}

// TestLookupOpenRouterModelNames 渠道侧模型名 → OpenRouter 元数据的命名差异匹配。
// 每个用例对应一个线上真实出现过的渠道命名形态。
func TestLookupOpenRouterModelNames(t *testing.T) {
	seedOpenRouterMatchCache(t)
	cases := []struct {
		model  string // 渠道 /v1/models 里的模型名
		wantID string // 应命中的 OpenRouter 完整 id；"" = 应 miss
	}{
		// 1. 前缀改名：doubao-seed-* 在 OpenRouter 叫 bytedance-seed/seed-*
		{"doubao-seed-2-0-lite-260428", "bytedance-seed/seed-2.0-lite"},         // 渠道 2-0 vs OR 2.0 + 日期后缀
		{"doubao-seed-2-0-lite-260215", "bytedance-seed/seed-2.0-lite"},         // 同上，不同日期
		{"doubao-seed-2-0-mini-260428", "bytedance-seed/seed-2.0-mini"},         // mini
		{"doubao-seed-2-0-code-preview-260215", "bytedance-seed/seed-2.0-code"}, // -preview 后缀
		{"doubao-seed-2-1-turbo-260628", "bytedance-seed/seed-2-1-turbo"},       // OR 本身就是 2-1 写法
		// 2. ga-YYMMDD GA 日期版：ga-260731 → 渠道口径 26 年 0731 → OR -0731
		{"deepseek-v4-flash-ga-260731", "deepseek/deepseek-v4-flash-0731"},
		{"deepseek-v4-pro-ga-260813", "deepseek/deepseek-v4-pro-0813"},
		// 3. hy 系列简称：hy3/hy4 在 OR 是 tencent/hy3、tencent/hy4-preview
		{"hy3", "tencent/hy3"},
		{"hy4-preview", "tencent/hy4-preview"},
		{"hy4", "tencent/hy4-preview"}, // 简称命中 preview 变体
		// 4. 标准化相等：glm-5-2-260617（剥日期）→ glm.5.2 == z-ai/glm-5.2
		{"glm-5-2-260617", "z-ai/glm-5.2"},
		{"claude-haiku-4-5-20251001", "anthropic/claude-haiku-4.5"}, // 点号差异
		// 5. 特价等业务后缀（unifyai 名称包含匹配兜底）
		{"glm-5.3-flash-特价", "z-ai/glm-5.3-flash"},
		{"deepseek-v4.1-flash-特价", ""}, // 缓存里没有 v4.1 → 该场景允许 miss
		// 6. 精确命中
		{"glm-5.2", "z-ai/glm-5.2"},
		{"deepseek-v4-pro", "deepseek/deepseek-v4-pro"},
		// 7. OR 里根本没有的：如实 miss，不猜
		{"doubao-seed-evolving", ""},
		{"git-commit", ""},
	}
	// 为「特价」用例补一条 v4.1 缓存，验证业务后缀剥离。
	path := openrouterCachePath()
	content := `[{"id":"deepseek/deepseek-v4.1-flash","name":"DS V4.1 Flash","context":1048576,"output":384000,"vision":true,"reasoning":true}]`
	extra := filepath.Join(t.TempDir(), "extra.json")
	if err := os.WriteFile(extra, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = path

	resolver := &openrouterContextResolver{}
	for _, c := range cases {
		meta, ok := resolver.metaOf(c.model)
		if c.wantID == "" {
			// 特价用例单独处理：额外缓存里有 v4.1 时应命中。
			if c.model == "deepseek-v4.1-flash-特价" {
				openrouterCachePath = func() string { return extra }
				resolver2 := &openrouterContextResolver{}
				meta2, ok2 := resolver2.metaOf(c.model)
				openrouterCachePath = func() string { return path }
				if !ok2 || meta2.ID != "deepseek/deepseek-v4.1-flash" {
					t.Errorf("%q: 业务后缀剥离失败, ok=%v meta=%+v", c.model, ok2, meta2)
				}
				continue
			}
			if ok {
				t.Errorf("%q: 应 miss 却命中 %s", c.model, meta.ID)
			}
			continue
		}
		if !ok {
			t.Errorf("%q: 应命中 %s 却 miss", c.model, c.wantID)
			continue
		}
		if meta.ID != c.wantID {
			t.Errorf("%q: 命中 %s, want %s", c.model, meta.ID, c.wantID)
		}
	}
}
