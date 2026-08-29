// Package adminapi 实现 Loadout 管理后台 REST API 插件：提供登录会话与
// 各类运行时数据（渠道/能力路由/MCP/工具分组/密钥/技能/预设/设置）的增删改查。
//
// 插件装配（core/plugin 框架）：
//   - 依赖 store、logger 与 admin-auth / gateway-keys / skills 三个业务服务；
//   - 提供服务 admin-api（供上层拿取 *Service）；
//   - Apply 阶段组装 Service、注册全部 API 路由与一条配置自检项。
package adminapi

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"loadout/core/db"
	"loadout/core/plugin"
	"loadout/core/store"
	"loadout/plugins/admin-auth"
	"loadout/plugins/contracts"
	"loadout/plugins/gateway-keys"
	mcphub "loadout/plugins/mcp-hub"
	"loadout/plugins/skills"
	unifyai "loadout/plugins/unifyai"
)

// adminAPI 是 admin-api 插件的实现，编译期由 core 装配。
type adminAPI struct{}

// New 创建 admin-api 插件实例。
func New() plugin.Plugin {
	return &adminAPI{}
}

// Manifest 返回插件清单：依赖 store/logger 与各业务服务，提供 admin-api。
func (p *adminAPI) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:    "admin-api",
		Version: "0.1.0",
		Inject:  []string{"store", "db", "logger", "admin-auth", "gateway-keys", "skills", "mcp-hub", "model-health", "route-log", "unifyai"},
		Provide: []string{"admin-api"},
	}
}

// Apply 装配插件：从容器取各服务并断言类型（缺失或类型不符则中止），
// 组装 Service 后注册到容器，再逐个挂载 API 路由并注册自检项。
func (p *adminAPI) Apply(ctx plugin.Context) error {
	st, err := require[*store.Store](ctx, "store")
	if err != nil {
		return err
	}
	lg, err := require[*slog.Logger](ctx, "logger")
	if err != nil {
		return err
	}
	auth, err := require[*adminauth.Service](ctx, "admin-auth")
	if err != nil {
		return err
	}
	keys, err := require[*gatewaykeys.Manager](ctx, "gateway-keys")
	if err != nil {
		return err
	}
	skill, err := require[*skills.Service](ctx, "skills")
	if err != nil {
		return err
	}
	hub, err := require[*mcphub.Service](ctx, "mcp-hub")
	if err != nil {
		return err
	}
	unify, err := require[*unifyai.Service](ctx, "unifyai")
	if err != nil {
		return err
	}
	database, err := require[*sql.DB](ctx, "db")
	if err != nil {
		return err
	}
	health, err := require[contracts.ModelHealth](ctx, "model-health")
	if err != nil {
		return err
	}
	routeLog, err := require[contracts.RouteLog](ctx, "route-log")
	if err != nil {
		return err
	}
	routing, err := db.NewRepository(database)
	if err != nil {
		return err
	}

	svc := NewService(st, lg, auth, keys, skill, hub, unify)
	svc.SetRoutingServices(database, routing, health, routeLog)
	// 依赖状态：先同步全局指令开关，再后台自动检查一次（unifyai / skills）。
	svc.syncUseGlobal(context.Background())
	plugin.RunBackground("deps-startup-check", func() error {
		svc.RefreshDeps()
		return nil
	})
	ctx.Set("admin-api", svc)
	for _, spec := range svc.Routes() {
		ctx.RegisterRoute(spec)
	}
	ctx.RegisterCheck("配置完整性", svc.selfCheck)
	return nil
}

// require 从容器按名取服务并做类型断言；缺失或类型不符返回错误。
func require[T any](ctx plugin.Context, name string) (T, error) {
	var zero T
	v := ctx.Get(name)
	if v == nil {
		return zero, fmt.Errorf("admin-api: 缺少服务 %q", name)
	}
	svc, ok := v.(T)
	if !ok {
		return zero, fmt.Errorf("admin-api: 服务 %q 类型不符", name)
	}
	return svc, nil
}
