// Package multimodalmcp 多模态 MCP 插件：内置一个独立 MCP 端点 /mcp/multimodal，
// 导出 3 个工具（understand_image / understand_video / understand_audio），
// 通过 model-gateway 子请求通道识别图片、视频、音频。
package multimodalmcp

import (
	"database/sql"
	"fmt"
	"log/slog"

	"loadout/core/db"
	"loadout/core/plugin"
	"loadout/core/store"
	"loadout/plugins/contracts"
	mcphub "loadout/plugins/mcp-hub"
	modelgateway "loadout/plugins/model-gateway"
)

// multimodalPlugin 多模态 MCP 插件实现：在 Apply 中装配 Service 并注册路由。
type multimodalPlugin struct{}

// New 创建多模态 MCP 插件（符合插件约定：导出 func New() plugin.Plugin）。
func New() plugin.Plugin { return &multimodalPlugin{} }

// Manifest 声明插件元数据：依赖 store/db/logger/model-gateway/route-log/mcp-hub，提供 "multimodal-mcp" 服务。
func (p *multimodalPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:    "multimodal-mcp",
		Version: "0.1.0",
		Inject:  []string{"store", "db", "logger", "model-gateway", "route-log", "mcp-hub"},
		Provide: []string{"multimodal-mcp"},
	}
}

// Apply 装配多模态插件：从容器取各服务，构建 Service 并注入网关与路由日志，
// 注册 MCP 端点路由（POST /mcp/multimodal）与配置路由（GET/PUT /api/multimodal/config）。
func (p *multimodalPlugin) Apply(ctx plugin.Context) error {
	st := ctx.Get("store").(*store.Store)
	lg := ctx.Get("logger").(*slog.Logger)
	database, ok := ctx.Get("db").(*sql.DB)
	if !ok || database == nil {
		return fmt.Errorf("multimodal-mcp: missing db service")
	}
	routeLog, ok := ctx.Get("route-log").(contracts.RouteLog)
	if !ok {
		return fmt.Errorf("multimodal-mcp: missing route-log service")
	}
	gw, ok := ctx.Get("model-gateway").(*modelgateway.Service)
	if !ok || gw == nil {
		return fmt.Errorf("multimodal-mcp: missing model-gateway service")
	}
	hub, ok := ctx.Get("mcp-hub").(*mcphub.Service)
	if !ok || hub == nil {
		return fmt.Errorf("multimodal-mcp: missing mcp-hub service")
	}
	repo, err := db.NewRepository(database)
	if err != nil {
		return fmt.Errorf("multimodal-mcp: 初始化仓储失败: %w", err)
	}
	svc := NewService(st, repo, lg)
	svc.SetRouteLog(routeLog)
	svc.SetGateway(gw)
	svc.SetMcpHub(hub)
	ctx.Set("multimodal-mcp", svc)

	// MCP 端点：精确路径 POST /mcp/multimodal 优先于 servercore 的 /mcp/ 前缀分发器，
	// 多模态独立于 mcp-hub。AuthMCPHeader 由 servercore 用 gateway-keys 的 MCPKeyMiddleware 包装。
	ctx.RegisterRoute(plugin.RouteSpec{
		Method:  "POST",
		Pattern: "/mcp/multimodal",
		Auth:    plugin.AuthMCPHeader,
		Handler: svc.MCPServer(),
	})

	// 配置路由（管理后台会话认证）。
	ctx.RegisterRoute(plugin.RouteSpec{
		Method:  "GET",
		Pattern: "/api/multimodal/config",
		Auth:    plugin.AuthSession,
		Handler: svc.HandlerConfig(),
	})
	ctx.RegisterRoute(plugin.RouteSpec{
		Method:  "PUT",
		Pattern: "/api/multimodal/config",
		Auth:    plugin.AuthSession,
		Handler: svc.HandlerConfig(),
	})
	// 启动时按当前配置注册/注销内置 server（工具进 $smart 聚合）。失败不阻断启动。
	if err := svc.syncHubRegistration(); err != nil {
		lg.Warn("multimodal-mcp: 启动时同步内置 server 注册失败", "err", err)
	}
	return nil
}
