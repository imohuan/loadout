// Package visionv2 视觉能力 v2：占位符 + 工具调用式图片识别（chat/claude/responses 三格式）。
package visionv2

import (
	"database/sql"
	"fmt"
	"log/slog"

	"loadout/core/db"
	"loadout/core/plugin"
	"loadout/core/store"
	"loadout/plugins/contracts"
	modelgateway "loadout/plugins/model-gateway"
)

type visionPlugin struct{}

func New() plugin.Plugin { return &visionPlugin{} }

func (p *visionPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{Name: "vision_v2", Version: "0.1.0",
		Inject: []string{"store", "db", "logger", "route-log"}, Provide: []string{"vision_v2"}}
}

func (p *visionPlugin) Apply(ctx plugin.Context) error {
	st := ctx.Get("store").(*store.Store)
	lg := ctx.Get("logger").(*slog.Logger)
	database, ok := ctx.Get("db").(*sql.DB)
	if !ok || database == nil {
		return fmt.Errorf("vision_v2: missing db service")
	}
	routeLog, ok := ctx.Get("route-log").(contracts.RouteLog)
	if !ok {
		return fmt.Errorf("vision_v2: missing route-log service")
	}
	repo, err := db.NewRepository(database)
	if err != nil {
		return fmt.Errorf("vision_v2: 初始化渠道仓储失败: %w", err)
	}
	svc := NewService(st, repo, lg)
	svc.SetRouteLog(routeLog)
	ctx.Set("vision_v2", svc)
	// 注册 ProxyBeforeAttempt 而非 ProxyBeforeUpstream：渠道匹配需要真实的
	// __current_channel / __current_channel_base_url（ProxyBeforeAttempt 每次渠道尝试前已写入）。
	// 挂在 ProxyBeforeUpstream 时渠道上下文为空，channel_base_urls 约束的路由（如
	// workbuddy 原生透传）永远匹配不上，会被全渠道附加代理兜底抢走。
	ctx.On(modelgateway.ProxyBeforeAttempt, svc.HandleProxyBeforeUpstream)
	ctx.On(modelgateway.ProxyStreamChunk, svc.HandleProxyStreamChunk)
	ctx.On(modelgateway.ProxyAfterUpstream, svc.HandleProxyAfterUpstream)
	return nil
}
