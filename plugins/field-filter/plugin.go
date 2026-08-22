// Package fieldfilter 实现 Loadout 的字段过滤能力适配器（plugins/field-filter）。
//
// 订阅 model-gateway 的 proxy:before-upstream（请求方向，改请求体）与
// proxy:after-upstream（非流式响应方向，改响应体/响应头）waterfall 事件，
// 按能力路由表（capability_routes，capability="field_filter"）的 field_rules
// 配置剔除/保留字段。
//
// 路由语义：native 原样透传；proxy 应用字段规则；error 路由 fail-open
// （不拒绝请求，按透传处理——与 sensitive-filter 的安全姿态一致）。
// 流式响应不做字段级处理（增量 delta 无法删字段）。
package fieldfilter

import (
	"database/sql"
	"fmt"
	"log/slog"

	"loadout/core/db"
	"loadout/core/plugin"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
)

type fieldFilterPlugin struct{}

func New() plugin.Plugin { return &fieldFilterPlugin{} }

func (p *fieldFilterPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:    "field-filter",
		Version: "0.1.0",
		Inject:  []string{"store", "logger", "db"},
		Provide: []string{"field-filter"},
	}
}

func (p *fieldFilterPlugin) Apply(ctx plugin.Context) error {
	st := ctx.Get("store").(*store.Store)
	lg, ok := ctx.Get("logger").(*slog.Logger)
	if !ok || lg == nil {
		return fmt.Errorf("field-filter: missing logger service")
	}
	if st == nil {
		return fmt.Errorf("field-filter: missing store service")
	}
	svc := NewService(st, lg)
	if database, ok := ctx.Get("db").(*sql.DB); ok && database != nil {
		if repo, err := db.NewRepository(database); err == nil {
			svc.SetRepository(repo)
		}
	}
	ctx.Set("field-filter", svc)
	// 请求方向安检挂在每次渠道尝试事件（proxy:before-attempt）：切换渠道/模型后
	// 字段规则按当前渠道上下文重新匹配；响应方向保持 proxy:after-upstream。
	ctx.On(modelgateway.ProxyBeforeAttempt, svc.HandleProxyBeforeUpstream)
	ctx.On(modelgateway.ProxyAfterUpstream, svc.HandleProxyAfterUpstream)
	return nil
}
