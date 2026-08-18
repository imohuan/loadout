// Package adminauth 实现 Loadout 的管理员登录、会话 JWT 与首启流程。
//
// 插件装配（core/plugin 框架）：
//   - 依赖 store（JSON 数据存储）与 logger（slog 日志器）；
//   - 提供服务 admin-auth（供 admin-api 等插件取用 *Service）；
//   - Apply 阶段执行首启流程：users.json 不存在时生成随机密码并创建 admin 账号。
package adminauth

import (
	"log/slog"

	"loadout/core/plugin"
	"loadout/core/store"
)

// adminAuth 是 admin-auth 插件的实现，编译期由 core 装配。
type adminAuth struct{}

// New 创建 admin-auth 插件实例。
func New() plugin.Plugin {
	return &adminAuth{}
}

// Manifest 返回插件清单：依赖 store/logger，提供 admin-auth 服务。
func (p *adminAuth) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:    "admin-auth",
		Version: "0.1.0",
		Inject:  []string{"store", "logger"},
		Provide: []string{"admin-auth"},
	}
}

// Apply 装配插件：创建认证服务，执行首启流程，并注册到容器。
// 首启流程失败会中止装配。
func (p *adminAuth) Apply(ctx plugin.Context) error {
	st := ctx.Get("store").(*store.Store)
	log := ctx.Get("logger").(*slog.Logger)

	svc := NewService(st, log)
	if _, err := svc.EnsureFirstRun(); err != nil {
		return err
	}
	ctx.Set("admin-auth", svc)
	return nil
}
