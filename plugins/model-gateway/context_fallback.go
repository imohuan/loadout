package modelgateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// openrouterMetaCache 对应 openrouter 元数据缓存文件 ~/.unifyai/cache/openrouter-models.json 里
// 单个模型条目（与 unifyai metadata-fetcher / Loadout unifyai 插件一致）。该文件由
// unifyai CLI 的「更新元数据」写入，含每个 openrouter 模型的真实上下文窗口。
type openrouterMetaCache struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Context   int64  `json:"context"`
	Output    int64  `json:"output"`
	Vision    bool   `json:"vision"`
	Reasoning bool   `json:"reasoning"`
}

// openrouterModelMeta 一个模型的完整对外声明（OpenRouter 元数据 + 渠道探测兜底之外的能力来源）。
type openrouterModelMeta struct {
	ID        string // 命中的 OpenRouter 完整 id（诊断用）
	Context   int64  // 上下文窗口 token 数；0 = 缓存未提供
	Output    int64  // 最大输出 token 数；0 = 缓存未提供
	Vision    bool   // 支持视觉（图片输入）
	Reasoning bool   // 支持推理（思考）
}

// openrouterCachePath 返回 openrouter 元数据缓存文件路径。做成变量便于测试注入。
var openrouterCachePath = func() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".unifyai", "cache", "openrouter-models.json")
}

// loadOpenRouterContext 读取 openrouter 元数据缓存并返回模型名 → 完整元数据 的映射
// （键与旧行为一致：完整 id + 裸模型名各存一份，大小写不敏感）。
// 缓存不存在/解析失败/路径为空时返回 nil（调用方静默回落，不影响 /v1/models 正常返回）。
func loadOpenRouterContext() map[string]openrouterModelMeta {
	path := openrouterCachePath()
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var metas []openrouterMetaCache
	if err := json.Unmarshal(data, &metas); err != nil {
		return nil
	}
	if len(metas) == 0 {
		return nil
	}
	// 键存两份：完整 id + 裸模型名（去 provider 前缀），方便不同命名习惯命中。
	out := make(map[string]openrouterModelMeta, len(metas)*2)
	for _, m := range metas {
		// 上下文/输出任一非零，或能力为 true 的条目都保留——
		// 纯文本模型 context 可能为 0 但 vision=false 本身就是有价值的声明。
		if m.Context <= 0 && m.Output <= 0 && !m.Vision && !m.Reasoning {
			continue
		}
		meta := openrouterModelMeta{Context: m.Context, Output: m.Output, Vision: m.Vision, Reasoning: m.Reasoning}
		meta.ID = m.ID
		key := strings.ToLower(strings.TrimSpace(m.ID))
		if key == "" {
			continue
		}
		out[key] = meta
		if i := strings.LastIndexByte(key, '/'); i >= 0 {
			// 裸名键：只在尚未有同名裸名键时写入（首个裸名命中优先，不覆盖
			// 已写入的完整 id 键——同裸名跨 provider 时保留先到的声明）。
			bare := key[i+1:]
			if _, exists := out[bare]; !exists {
				out[bare] = meta
			}
		}
	}
	return out
}

// lookupOpenRouterContext 从 openrouter 元数据映射里按模型名查完整元数据。
// m 为空或映射为空时返回 0。键匹配容忍大小写与 provider 前缀：
// 先查完整 id，再查裸模型名（去最后一个 / 之后的部分）。
func lookupOpenRouterContext(meta map[string]openrouterModelMeta, model string) (openrouterModelMeta, bool) {
	if len(meta) == 0 || model == "" {
		return openrouterModelMeta{}, false
	}
	key := strings.ToLower(strings.TrimSpace(model))
	if v, ok := meta[key]; ok {
		return v, true
	}
	if i := strings.LastIndexByte(key, '/'); i >= 0 {
		key = key[i+1:]
		if v, ok := meta[key]; ok {
			return v, true
		}
	}
	return openrouterModelMeta{}, false
}

// openrouterContextResolver 按需补上下文：仅当确有模型缺 context（首个 miss）时才读
// openrouter 元数据缓存文件，避免每个 /v1/models 请求都无条件做一次磁盘 I/O。
// 同一次请求里多次 miss 复用同一份已解析映射；映射解析一次后在本 resolver 上复用。
type openrouterContextResolver struct {
	meta map[string]openrouterModelMeta
}

// metaOf 返回 model 的 openrouter 兜底元数据；model 为空或缓存缺失时 ok=false。
func (r *openrouterContextResolver) metaOf(model string) (openrouterModelMeta, bool) {
	if r.meta == nil {
		r.meta = loadOpenRouterContext()
	}
	if v, ok := lookupOpenRouterContext(r.meta, model); ok {
		return v, true
	}
	// 精确键未命中：渠道命名与 OpenRouter 命名存在系统性差异（doubao-seed-* vs
	// bytedance-seed/seed-*、-ga-260731 日期版、hy 简称等），走模糊匹配兜底。
	return matchOpenRouterModel(r.meta, model)
}

// openrouterBareName 取模型名的裸名（去 provider 前缀，小写）。
func openrouterBareName(model string) string {
	key := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndexByte(key, '/'); i >= 0 {
		return key[i+1:]
	}
	return key
}

// openrouterNormalizeKey 标准化模型名用于比较：去掉 - . : _ 等分隔符。
// "glm-5-2" 与 "glm.5.2" 归一后都是 "glm52"。
func openrouterNormalizeKey(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '-', '.', ':', '_':
			return -1
		}
		return r
	}, s)
}

// matchOpenRouterModel 渠道命名 → OpenRouter 命名的模糊匹配（unifyai findInOpenRouter 的
// Go 移植 + 渠道特有差异的补充规则）。按置信度从高到低依次尝试：
//  1. ga 日期版映射：deepseek-v4-flash-ga-260731（渠道 GA 版，26=2026 年）→ -0731（OR 月日版）
//  2. doubao-seed → seed 前缀改名：渠道 doubao-seed-2-0-lite-260428 → OR bytedance-seed/seed-2.0-lite
//     （剥 -YYMMDD 日期尾、-preview 尾后，先精确再标准化相等）
//  3. unifyai 原生匹配（-latest 优先、逐段剥离尾部版本、名称包含）
func matchOpenRouterModel(meta map[string]openrouterModelMeta, model string) (openrouterModelMeta, bool) {
	if len(meta) == 0 {
		return openrouterModelMeta{}, false
	}
	miss := openrouterModelMeta{}
	bare := openrouterBareName(model)
	if bare == "" {
		return miss, false
	}

	// 1. ga 日期版：*-ga-YYMMDD（YY=年份）→ *-MMDD（OpenRouter 的发布日期后缀口径）。
	if ga := gaDateSuffix.FindStringSubmatch(bare); ga != nil {
		if v, ok := lookupOpenRouterContext(meta, ga[1]+"-"+ga[2]); ok {
			return v, true
		}
	}

	// 2. doubao-seed 前缀改名（渠道侧火山引擎命名 vs OpenRouter bytedance-seed 命名）：
	//    doubao-seed-2-0-lite-260428 → seed-2-0-lite（再剥日期尾）→ 精确/标准化匹配。
	// hy 简称同理：hy4 → hy4-preview（OR 只有 preview 变体），由第 3 步名称包含兜底。
	if renamed, ok := strings.CutPrefix(bare, "doubao-seed"); ok {
		base := "seed" + renamed
		candidates := []string{base}
		// 剥渠道日期尾：-260428（6 位以上数字）。
		if trimmed, n := cutNumericDateSuffix(base); n {
			candidates = append(candidates, trimmed)
			// 日期+preview 组合：seed-2-0-code-preview-260215 → seed-2-0-code。
			if t2, n2 := strings.CutSuffix(trimmed, "-preview"); n2 {
				candidates = append(candidates, t2)
			}
		}
		// 剥 -preview 尾。
		if trimmed, n := strings.CutSuffix(base, "-preview"); n {
			candidates = append(candidates, trimmed)
			if t2, n2 := cutNumericDateSuffix(trimmed); n2 {
				candidates = append(candidates, t2)
			}
		}
		for _, cand := range candidates {
			if v, ok := lookupOpenRouterContext(meta, cand); ok {
				return v, true
			}
			// 标准化相等：渠道 2-0 vs OR 2.0、日期尾 260428 还在键里时也按标准化比。
			norm := openrouterNormalizeKey(cand)
			for k, v := range meta {
				if openrouterNormalizeKey(openrouterBareName(k)) == norm {
					return v, true
				}
			}
		}
	}

	// 3. unifyai 原生匹配（-latest 优先、逐段剥离、名称包含）。
	// ga 日期版没精确命中 -MMDD 时，unifyai 的「-latest 优先」会抢在前面，导致
	// flash-ga-260731 命中 flash-latest 而不是 flash-0731（更精确的日期版）。所以
	// 这里先做一次「剥 ga 尾 → unifyai 匹配」，让日期语义仍然参与匹配。
	gaStrip := gaDateSuffix.ReplaceAllString(bare, "$1")
	for _, cand := range []string{bare, gaStrip} {
		if v, ok := lookupOpenRouterNative(meta, cand); ok {
			return v, true
		}
	}
	// hy 简称（hy3/hy4）：OR 只有 hy3 / hy4-preview。裸名是 OR 裸名的前缀时按
	// 前缀匹配（hy4 → tencent/hy4-preview），前提是长度差 ≥2（避免 gpt → gpt-5.6 误配）。
	for k, v := range meta {
		orBare := openrouterBareName(k)
		if strings.HasPrefix(orBare, bare) && len(orBare)-len(bare) >= 2 {
			return v, true
		}
	}
	return miss, false
}

// cutNumericDateSuffix 剥掉尾部的日期片段（-260428 这类 6 位以上纯数字，渠道
// 侧常用来标记 GA 日期；OpenRouter 同模型条目不带该尾）。没有该尾时原样返回。
func cutNumericDateSuffix(s string) (string, bool) {
	i := strings.LastIndexByte(s, '-')
	if i <= 0 || i+7 > len(s) {
		return s, false
	}
	for _, r := range s[i+1:] {
		if r < '0' || r > '9' {
			return s, false
		}
	}
	return s[:i], true
}

// gaDateSuffix 匹配渠道 GA 日期版模型名：*-ga-YYMMDD。
// ga[1] = 去掉 -ga-YYMMDD 的基础名；ga[2] = MMDD（OpenRouter 用月日后缀，如 -0731）。
var gaDateSuffix = regexp.MustCompile(`^(.+)-ga-\d{2}(\d{4})$`)

// lookupOpenRouterNative unifyai findInOpenRouter 的 Go 移植（仅保留对 /v1/models
// 补全有意义的顺序）：裸名后缀匹配 → -latest 优先 → 逐段剥离尾部 → 标准化包含。
func lookupOpenRouterNative(meta map[string]openrouterModelMeta, bare string) (openrouterModelMeta, bool) {
	// OR 条目裸名以「/ + bare」结尾：hy3 → tencent/hy3。
	if v, ok := meta[bare]; ok {
		return v, true
	}
	// 逐段剥离尾部片段（至少保留 2 段），优先 <candidate>-latest。
	parts := strings.Split(bare, "-")
	for i := len(parts) - 1; i >= 2; i-- {
		candidate := strings.Join(parts[:i], "-")
		if v, ok := meta[candidate+"-latest"]; ok {
			return v, true
		}
		if v, ok := meta[candidate]; ok {
			return v, true
		}
		norm := openrouterNormalizeKey(candidate)
		if len(norm) < 4 {
			continue
		}
		for k, v := range meta {
			if strings.Contains(openrouterNormalizeKey(openrouterBareName(k)), norm) {
				return v, true
			}
		}
	}
	return openrouterModelMeta{}, false
}
