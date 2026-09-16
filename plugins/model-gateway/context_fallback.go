package modelgateway

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	Context   int64 // 上下文窗口 token 数；0 = 缓存未提供
	Output    int64 // 最大输出 token 数；0 = 缓存未提供
	Vision    bool  // 支持视觉（图片输入）
	Reasoning bool  // 支持推理（思考）
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
	return lookupOpenRouterContext(r.meta, model)
}
