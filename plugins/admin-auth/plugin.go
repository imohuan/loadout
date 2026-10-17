// Package adminauth 实现 Loadout 的管理员登录、会话 JWT 与首启流程。
//
// 插件装配（core/plugin 框架）：
//   - 依赖 store（JSON 数据存储）与 logger（slog 日志器）；
//   - 提供服务 admin-auth（供 admin-api 等插件取用 *Service）；
//   - Apply 阶段装配认证服务（是否已初始化由 Initialized() 按需查询，
//     初始访问密钥由用户在首次打开管理台时自行设置）。
package adminauth

import (
	"database/sql"
	"log/slog"

	"loadout/core/db"
	"loadout/core/plugin"
	"loadout/core/store"
)

// adminAuth 是 admin-auth 插件的实现，编译期由 core 装配。
type adminAuth struct{}

// New 创建 admin-auth 插件实例。
func New() plugin.Plugin {
	return &adminAuth{}
}

// Manifest 返回插件清单：依赖 store/logger/db，提供 admin-auth 服务。
func (p *adminAuth) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:    "admin-auth",
		Version: "0.1.0",
		Inject:  []string{"store", "logger", "db"},
		Provide: []string{"admin-auth"},
	}
}

// Apply 装配插件：创建认证服务并注册到容器。
func (p *adminAuth) Apply(ctx plugin.Context) error {
	st := ctx.Get("store").(*store.Store)
	log := ctx.Get("logger").(*slog.Logger)

	svc := NewService(st, log)
	if database, ok := ctx.Get("db").(*sql.DB); ok && database != nil {
		if repo, err := db.NewRepository(database); err == nil {
			svc.SetRepository(repo)
		}
	}
	ctx.Set("admin-auth", svc)
	return nil
}
