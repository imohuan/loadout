// Package vision 实现 Loadout 的视觉能力适配器（plugins/vision）。
//
// 视觉能力适配器订阅 model-gateway 的 chat:before-upstream waterfall 事件，
// 在请求转发上游前完成：检出图片 → 能力路由 → 调用视觉模型生成描述 →
// 改写 messages → 写入 VisionText（供流式 reasoning 注入）。
//
// 路由语义（capability_routes.json，见 DESIGN.md 5.5）：
//   - native：目标模型原生支持视觉，直接透传；
//   - proxy ：转发给 via_model 的视觉模型，改写后再发主模型；
//   - error ：明确不支持，直接报错。
//
// 视觉描述带 md5 缓存（config.VisionCacheEnabled 开启时）：图片内容 md5 →
// 描述文本，命中且未过期（config.VisionCacheTTLHours）直接复用。
package vision

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

// visionPlugin 是 vision 插件的实现，编译期由 core 装配。
type visionPlugin struct{}

// New 创建 vision 插件实例。
func New() plugin.Plugin {
	return &visionPlugin{}
}

// Manifest 返回插件清单：依赖 store/db/logger/route-log，提供 vision 服务。
func (p *visionPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:    "vision",
		Version: "0.1.0",
		Inject:  []string{"store", "db", "logger", "route-log"},
		Provide: []string{"vision"},
	}
}

// Apply 装配插件：从容器取 store/db 与 logger，构造 Service 并注册为 vision 服务，
// 订阅透明代理输入 hook（proxy:before-upstream，三种对话格式）与
// 旧 chat 管线事件（chat:before-upstream，过渡期保留，旧 HandleChat 下线后移除）。
func (p *visionPlugin) Apply(ctx plugin.Context) error {
	st := ctx.Get("store").(*store.Store)
	lg := ctx.Get("logger").(*slog.Logger)
	database, ok := ctx.Get("db").(*sql.DB)
	if !ok || database == nil {
		return fmt.Errorf("vision: missing db service")
	}
	routeLog, ok := ctx.Get("route-log").(contracts.RouteLog)
	if !ok {
		return fmt.Errorf("vision: missing route-log service")
	}
	repo, err := db.NewRepository(database)
	if err != nil {
		return fmt.Errorf("vision: 初始化渠道仓储失败: %w", err)
	}

	svc := NewService(st, repo, lg)
	svc.SetRouteLog(routeLog)
	ctx.Set("vision", svc)
	ctx.On(modelgateway.ProxyBeforeUpstream, svc.HandleProxyBeforeUpstream)
	ctx.On(modelgateway.EventBeforeUpstream, svc.HandleBeforeUpstream)
	return nil
}
