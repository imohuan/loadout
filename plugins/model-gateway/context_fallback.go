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
	ID      string `json:"id"`
	Name    string `json:"name"`
	Context int64  `json:"context"`
}

// openrouterCachePath 返回 openrouter 元数据缓存文件路径。做成变量便于测试注入。
var openrouterCachePath = func() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".unifyai", "cache", "openrouter-models.json")
}

// loadOpenRouterContext 读取 openrouter 元数据缓存并返回 id → 上下文窗口 的映射。
// 缓存不存在/解析失败/路径为空时返回 nil（调用方静默回落，不影响 /v1/models 正常返回）。
func loadOpenRouterContext() map[string]int64 {
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
	out := make(map[string]int64, len(metas)*2)
	for _, m := range metas {
		if m.Context <= 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(m.ID))
		if key == "" {
			continue
		}
		out[key] = m.Context
		if i := strings.LastIndexByte(key, '/'); i >= 0 {
			key = key[i+1:]
		}
		out[key] = m.Context
	}
	return out
}

// lookupOpenRouterContext 从 openrouter 元数据映射里按模型名查上下文。
// m 为空或映射为空时返回 0。键匹配容忍大小写与 provider 前缀：
// 先查完整 id，再查裸模型名（去最后一个 / 之后的部分）。
func lookupOpenRouterContext(meta map[string]int64, model string) int64 {
	if len(meta) == 0 || model == "" {
		return 0
	}
	key := strings.ToLower(strings.TrimSpace(model))
	if v, ok := meta[key]; ok {
		return v
	}
	if i := strings.LastIndexByte(key, '/'); i >= 0 {
		key = key[i+1:]
	}
	if v, ok := meta[key]; ok {
		return v
	}
	return 0
}
