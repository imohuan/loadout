// Package requestlog 插件装配入口：开独立库、注入仓储、注册服务与事件订阅。
package requestlog

import (
	"context"
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

// Manifest 返回插件清单：依赖 store/logger/db/route-log，提供 request-log 服务。
//
// Inject 里带上 "route-log" 是**顺序约束**而非取用依赖（本插件不读它的实例）：
// 装配顺序由拓扑排序决定、不看注册顺序，不确定的话 request-log 可能先于 route-log
// 应用，导致下面 InstallRouteLogPresence 回调时安装器还没登记（静默失效）。
// 显式声明后 route-log 必定先装配、先登记安装器。
func (p *requestLogPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:    "request-log",
		Version: "0.1.0",
		Inject:  []string{"store", "logger", "db", "route-log"},
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

	reqPath := filepath.Join(filepath.Dir(st.Dir()), "request-log.db")
	reqDB, err := openRequestLogDB(reqPath)
	if err != nil {
		return fmt.Errorf("request-log: open request-log.db: %w", err)
	}
	ctx.Effect(func() { _ = reqDB.Close() })

	svc := NewService(st, lg, reqDB, database)
	svc.SetDBPath(reqPath)
	if repo, err := db.NewRepository(database); err == nil {
		svc.SetRepository(repo)
	}
	ctx.Set("request-log", svc)

	// 回填 route-log 的存在性查询：列表要隐藏「进入日志」入口里已被清理的日志。
	// 装配顺序上 route-log 在前，这里用安装器反向注入（见 plugin.Context 的说明）。
	if installed := ctx.InstallRouteLogPresence(svc.ExistingIDs); !installed {
		lg.Warn("request-log: route-log 未登记存在性钩子，进入日志入口不做校验")
	} else {
		lg.Info("request-log: 已注入完整日志存在性查询")
	}

	svc.subscribe(ctx)

	// 启动时按当前保留策略清理一次：上次运行遗留的超期/超量日志不必等到下一个请求才消失。
	// 后台 goroutine 执行，不阻塞装配（日志库可能很大，删起来要时间）。
	go svc.ApplyRetention(context.Background(), svc.currentRetention())

	// API（Auth 由框架按 plugin.AuthSession 自动挂 session 中间件，server.go:107）
	// 注意注册顺序：/stats 是静态路径，必须早于 /{id} 通配，否则会被当成 id 吃掉。
	ctx.RegisterRoute(plugin.RouteSpec{Method: http.MethodGet, Pattern: "GET /api/request-logs", Auth: plugin.AuthSession, Handler: http.HandlerFunc(svc.handleList)})
	ctx.RegisterRoute(plugin.RouteSpec{Method: http.MethodGet, Pattern: "GET /api/request-logs/stats", Auth: plugin.AuthSession, Handler: http.HandlerFunc(svc.handleStats)})
	ctx.RegisterRoute(plugin.RouteSpec{Method: http.MethodPost, Pattern: "POST /api/request-logs/retention/apply", Auth: plugin.AuthSession, Handler: http.HandlerFunc(svc.handleApplyRetention)})
	ctx.RegisterRoute(plugin.RouteSpec{Method: http.MethodDelete, Pattern: "DELETE /api/request-logs", Auth: plugin.AuthSession, Handler: http.HandlerFunc(svc.handleClear)})
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
