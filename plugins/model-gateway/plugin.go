// Package modelgateway 实现 Loadout 的 /v1 模型转发核心：请求管线 + 渠道解析 +
// 能力路由 + 字段清洗 + 转发 + 流式 reasoning 注入。
//
// 请求到达 /v1/chat/completions 后，先归一化为内部结构，再通过 waterfall 事件
// chat:before-upstream 交给能力插件（如 vision）改写，最后清洗字段并转发到上游渠道。
package modelgateway

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"

	"loadout/core/plugin"
	"loadout/core/store"
	"loadout/plugins/contracts"
)

// modelGateway 是 model-gateway 插件的实现，编译期由 core 装配。
type modelGateway struct{}

// New 创建 model-gateway 插件实例。
func New() plugin.Plugin {
	return &modelGateway{}
}

// Manifest 返回插件清单：依赖 store/logger，提供 model-gateway 服务。
func (p *modelGateway) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:    "model-gateway",
		Version: "0.1.0",
		Inject:  []string{"store", "db", "logger", "model-health", "route-log"},
		Provide: []string{"model-gateway"},
	}
}

// Apply 装配插件：从容器取 store 与 logger，构造转发服务并注册 /v1 路由。
func (p *modelGateway) Apply(ctx plugin.Context) error {
	st := ctx.Get("store").(*store.Store)
	lg := ctx.Get("logger").(*slog.Logger)

	svc := NewService(st, lg, ctx)
	database, ok := ctx.Get("db").(*sql.DB)
	if !ok || database == nil {
		return fmt.Errorf("model-gateway: missing db service")
	}
	health, ok := ctx.Get("model-health").(contracts.ModelHealth)
	if !ok {
		return fmt.Errorf("model-gateway: missing model-health service")
	}
	routeLog, ok := ctx.Get("route-log").(contracts.RouteLog)
	if !ok {
		return fmt.Errorf("model-gateway: missing route-log service")
	}
	svc.SetRoutingServices(database, health, routeLog)
	ctx.Set("model-gateway", svc)
	ctx.RegisterRoute(plugin.RouteSpec{
		Method:  "GET",
		Pattern: "/v1/models",
		Auth:    plugin.AuthSkKey,
		Handler: http.HandlerFunc(svc.HandleModels),
	})
	// 透明代理：/v1 下其余任意路径、任意方法原样转发到匹配渠道
	// （/v1/models 因更具体优先命中，不受影响）。
	ctx.RegisterRoute(plugin.RouteSpec{
		Method:  "",
		Pattern: "/v1/{path...}",
		Auth:    plugin.AuthSkKey,
		Handler: http.HandlerFunc(svc.HandleProxy),
	})
	return nil
}
