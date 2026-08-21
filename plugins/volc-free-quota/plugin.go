// Package volcfreequota 实现火山引擎（方舟 Ark）免费模型额度监控：
//
//   - 在配置面板为每个火山引擎渠道 Key 配置 AK/SK（控制台访问授权）。
//   - 定时 + 手动触发查询 billing.ListResourcePackages，列出每个免费模型的剩余额度。
//   - 模型请求结束后记录 (channel_id, model) 到 volc_quota_usage，便于人工审计。
//   - 额度耗尽的免费模型：在 model_states 写入 冷却至次日 0:00 的禁用状态，
//     并在 before-upstream 钩子检测到目标模型全部候选渠道本地余额耗尽时直接报 "模型免费额度用完"。
//
// 数据存储：DB 迁移 v10 三张表（volc_quota_config / volc_quota_packages / volc_quota_usage，
// v17 删除 volc_quota_models 聚合表，全部走 packages）。
package volcfreequota

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"loadout/core/plugin"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
)

// volcQuota 是 volc-free-quota 插件实例（编译期由 core 装配）。
type volcQuota struct{}

// New 创建 volc-free-quota 插件实例。
func New() plugin.Plugin { return &volcQuota{} }

// Manifest 返回插件清单。
func (p *volcQuota) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:    "volc-free-quota",
		Version: "0.1.0",
		// 依赖：核心服务（store 用于加密 secret_key）。
		// 注：model-gateway 事件通过 ctx.On 订阅；model-health 不直接依赖，禁用通过
		// 直接写 model_states 表实现（与 model-health 的语义完全一致）。
		Inject:  []string{"store", "db", "logger"},
		Provide: []string{"volc-free-quota"},
	}
}

// Apply 装配插件：构造 Service、订阅 model-gateway 事件、注册管理后台路由、启动后台刷新。
//
// 管理后台路由使用 AuthSession 鉴权，由 core/servercore 在挂载时自动包装
// admin-auth.SessionMiddleware（无需在本插件内注入 admin-auth）。
func (p *volcQuota) Apply(ctx plugin.Context) error {
	st, ok := ctx.Get("store").(*store.Store)
	if !ok || st == nil {
		return fmt.Errorf("volc-free-quota: 缺少 store 服务")
	}
	lg, ok := ctx.Get("logger").(*slog.Logger)
	if !ok || lg == nil {
		return fmt.Errorf("volc-free-quota: 缺少 logger 服务")
	}
	database, ok := ctx.Get("db").(*sql.DB)
	if !ok || database == nil {
		return fmt.Errorf("volc-free-quota: 缺少 db 服务")
	}
	svc := NewService(database, st, lg)
	ctx.Set("volc-free-quota", svc)

	// 订阅 model-gateway 请求结束事件：记录 (channel_id, model) 使用次数。
	ctx.On(modelgateway.ProxyUpstreamSucceeded, svc.HandleProxyUpstreamSucceeded)
	// 拦截前：当目标免费模型在所有候选渠道上本地余额都耗尽时，返回 "模型免费额度用完"。
	ctx.On(modelgateway.ProxyBeforeUpstream, svc.HandleProxyBeforeUpstream)

	// 注册管理后台路由（AuthSession 由 core/servercore 自动包装）。
	ctx.RegisterRoute(plugin.RouteSpec{
		Method:  http.MethodGet,
		Pattern: "GET /api/volc-quota/status",
		Auth:    plugin.AuthSession,
		Handler: http.HandlerFunc(svc.HandleListStatus),
	})
	ctx.RegisterRoute(plugin.RouteSpec{
		Method:  http.MethodPut,
		Pattern: "PUT /api/volc-quota/config",
		Auth:    plugin.AuthSession,
		Handler: http.HandlerFunc(svc.HandleSaveConfigs),
	})
	ctx.RegisterRoute(plugin.RouteSpec{
		Method:  http.MethodPost,
		Pattern: "POST /api/volc-quota/refresh",
		Auth:    plugin.AuthSession,
		Handler: http.HandlerFunc(svc.HandleRefresh),
	})
	ctx.RegisterRoute(plugin.RouteSpec{
		Method:  http.MethodGet,
		Pattern: "GET /api/volc-quota/recent-usage",
		Auth:    plugin.AuthSession,
		Handler: http.HandlerFunc(svc.HandleRecentUsage),
	})

	// 启动后台刷新 goroutine：每 15 分钟一次；启动后立刻同步一次以尽快拿到首份数据。
	ctx.Effect(svc.StartBackgroundRefresh(15*time.Minute, true))
	return nil
}
