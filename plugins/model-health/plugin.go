// Package modelhealth owns channel and model availability state.
package modelhealth

import (
	"database/sql"
	"log/slog"

	"loadout/core/plugin"
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
	ctx.Effect(service.Start())
	return nil
}
