// Package modelhealth owns channel and model availability state.
package modelhealth

import (
	"database/sql"
	"log/slog"

	"loadout/core/plugin"
	"loadout/core/store"
	gatewaykeys "loadout/plugins/gateway-keys"
)

type healthPlugin struct{}

func New() plugin.Plugin { return &healthPlugin{} }

func (p *healthPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{Name: "model-health", Version: "0.1.0", Inject: []string{"db", "logger", "gateway-keys"}, Provide: []string{"model-health"}}
}

func (p *healthPlugin) Apply(ctx plugin.Context) error {
	database, ok := ctx.Get("db").(*sql.DB)
	if !ok || database == nil {
		return pluginError("db")
	}
	logger, ok := ctx.Get("logger").(*slog.Logger)
	if !ok || logger == nil {
		return pluginError("logger")
	}
	service := NewService(database, logger)

	// SK key 解析（AI 兜底请求走网关自身 /v1 鉴权；gateway-keys 未装配时保持空 = 关闭）。
	if keysRaw := ctx.Get("gateway-keys"); keysRaw != nil {
		if keys, ok := keysRaw.(*gatewaykeys.Manager); ok && keys != nil {
			service.SetKeyResolver(func() string {
				list, err := keys.ListAPIKeys()
				if err != nil {
					return ""
				}
				for _, k := range list {
					plain, _, e := keys.ResolveAPIKey(k.Hash)
					if e == nil && plain != "" {
						return plain
					}
				}
				return ""
			})
		}
	}
	ctx.Set("model-health", service)

	// AI 兜底模型默认取「内置模型」：用户在多模态插件里配的那批模型
	// （图片/音频/视频/文档各一个）。用户没在设置页显式配置时，用其中第一个
	// 非空的当兜底判定模型——否则规则未命中就只能落默认冷却，AI 兜底形同虚设。
	if svc, ok := ctx.Get("model-health").(*Service); ok && svc != nil {
		if m := builtinFallbackModel(ctx); m != "" {
			svc.SetBuiltinFallbackModel(m)
		}
	}
	ctx.Effect(service.Start())
	return nil
}

// builtinFallbackModel 从多模态配置里挑一个内置模型作为 AI 兜底默认值。
// 优先级：image → audio → video → document（图片理解模型通常最通用、最便宜）。
func builtinFallbackModel(ctx plugin.Context) string {
	st, ok := ctx.Get("store").(*store.Store)
	if !ok || st == nil {
		return ""
	}
	type toolCfg struct {
		Kind    string `json:"kind"`
		Enabled bool   `json:"enabled"`
		Model   string `json:"model"`
	}
	var cfg struct {
		Tools []toolCfg `json:"tools"`
	}
	// 文件不存在/解析失败都当「没配内置模型」，不影响启动。
	if err := st.Read(fileMultimodalConfig(), &cfg); err != nil {
		return ""
	}
	byKind := map[string]string{}
	for _, tl := range cfg.Tools {
		if tl.Enabled && tl.Model != "" {
			byKind[tl.Kind] = tl.Model
		}
	}
	for _, k := range []string{"image", "audio", "video", "document"} {
		if m := byKind[k]; m != "" {
			return m
		}
	}
	return ""
}

// fileMultimodalConfig 多模态配置文件名（与 multimodal-mcp 包保持一致）。
func fileMultimodalConfig() string { return "multimodal_config.json" }
