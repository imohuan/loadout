// Package requestlog 插件装配入口：开独立库、注入仓储、注册服务与事件订阅。
package requestlog

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"

	"loadout/core/db"
	"loadout/core/plugin"
	"loadout/core/store"
)

type requestLogPlugin struct{}

// New 创建 request-log 插件实例。
func New() plugin.Plugin { return &requestLogPlugin{} }

// Manifest 返回插件清单：依赖 store/logger/db，提供 request-log 服务。
func (p *requestLogPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:    "request-log",
		Version: "0.1.0",
		Inject:  []string{"store", "logger", "db"},
		Provide: []string{"request-log"},
	}
}

// Apply 装配插件：开独立库 request-log.db（与 loadout.db 同级，不跑 loadout 迁移）、
// 注入能力路由仓储、注册服务并订阅 model-gateway 事件。
func (p *requestLogPlugin) Apply(ctx plugin.Context) error {
	st, ok := ctx.Get("store").(*store.Store)
	if !ok || st == nil {
		return fmt.Errorf("request-log: missing store service")
	}
	lg, ok := ctx.Get("logger").(*slog.Logger)
	if !ok || lg == nil {
		return fmt.Errorf("request-log: missing logger service")
	}
	database, ok := ctx.Get("db").(*sql.DB)
	if !ok || database == nil {
		return fmt.Errorf("request-log: missing db service")
	}

	reqDB, err := openRequestLogDB(filepath.Join(filepath.Dir(st.Dir()), "request-log.db"))
	if err != nil {
		return fmt.Errorf("request-log: open request-log.db: %w", err)
	}
	ctx.Effect(func() { _ = reqDB.Close() })

	svc := NewService(st, lg, reqDB, database)
	if repo, err := db.NewRepository(database); err == nil {
		svc.SetRepository(repo)
	}
	ctx.Set("request-log", svc)

	svc.subscribe(ctx)

	// API（Auth 由框架按 plugin.AuthSession 自动挂 session 中间件，server.go:107）
	ctx.RegisterRoute(plugin.RouteSpec{Method: http.MethodGet, Pattern: "GET /api/request-logs", Auth: plugin.AuthSession, Handler: http.HandlerFunc(svc.handleList)})
	ctx.RegisterRoute(plugin.RouteSpec{Method: http.MethodGet, Pattern: "GET /api/request-logs/{id}", Auth: plugin.AuthSession, Handler: http.HandlerFunc(svc.handleDetail)})

	ctx.RegisterCheck("request-log 完整性", func() []plugin.Issue {
		var issues []plugin.Issue
		if _, err := reqDB.Exec(`SELECT 1 FROM request_logs LIMIT 1`); err != nil {
			issues = append(issues, plugin.Issue{Level: "error", Message: "request-log.db 读取失败: " + err.Error()})
		}
		return issues
	})
	return nil
}
