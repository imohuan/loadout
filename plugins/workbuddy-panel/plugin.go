// Package workbuddypanel 实现 WorkBuddy（CodeBuddy / copilot.tencent.com）渠道的
// 积分与用量监控面板。
//
// 数据分两路，均不依赖定时轮询上游：
//
//  1. 积分余额（资源包）：只在用户点「刷新」时按渠道 Key 调上游
//     /billing/meter/get-user-resource 与 /console/enterprises/personal/models，
//     结果缓存到 wb_panel_credits，面板读取走缓存。
//  2. 消耗流水：订阅 model-gateway 的事件，从每个成功请求的 usage.credit 里
//     提取消耗，写入 wb_panel_usage（含 token 数）；趋势 / 排行 / 明细全部由
//     本地表聚合，不打上游。
package workbuddypanel

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"loadout/core/plugin"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
)

// wbPanel 是 workbuddy-panel 插件实例（编译期由 core 装配）。
type wbPanel struct{}

// New 创建 workbuddy-panel 插件实例。
func New() plugin.Plugin { return &wbPanel{} }

// Manifest 返回插件清单。
func (p *wbPanel) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:    "workbuddy-panel",
		Version: "0.1.0",
		Inject:  []string{"store", "db", "logger"},
		Provide: []string{"workbuddy-panel"},
	}
}

// migrate 幂等建表（插件内迁移，不进核心 migrate.go）。
func migrate(database *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS wb_panel_credits (
			channel_id    TEXT PRIMARY KEY,
			label         TEXT NOT NULL DEFAULT '',
			remain        REAL NOT NULL DEFAULT 0,
			total         REAL NOT NULL DEFAULT 0,
			packages_json TEXT NOT NULL DEFAULT '[]',
			models_json   TEXT NOT NULL DEFAULT '[]',
			synced_at     TEXT NOT NULL DEFAULT '',
			last_error    TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS wb_panel_usage (
			id                INTEGER PRIMARY KEY AUTOINCREMENT,
			request_id        TEXT    NOT NULL DEFAULT '',
			channel_id        TEXT    NOT NULL DEFAULT '',
			model             TEXT    NOT NULL DEFAULT '',
			credit            REAL    NOT NULL DEFAULT 0,
			prompt_tokens     INTEGER NOT NULL DEFAULT 0,
			completion_tokens INTEGER NOT NULL DEFAULT 0,
			cached_tokens     INTEGER NOT NULL DEFAULT 0,
			created_at        TEXT    NOT NULL
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_wb_panel_usage_request ON wb_panel_usage(request_id) WHERE request_id != ''`,
		`CREATE INDEX IF NOT EXISTS idx_wb_panel_usage_created ON wb_panel_usage(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_wb_panel_usage_channel ON wb_panel_usage(channel_id)`,
		// 远程流水表（上游拉取，以它为权威基线）；wb_panel_usage 只存本地拦截。
		`CREATE TABLE IF NOT EXISTS wb_panel_usage_remote (
			id                INTEGER PRIMARY KEY AUTOINCREMENT,
			request_id        TEXT    NOT NULL DEFAULT '',
			channel_id        TEXT    NOT NULL DEFAULT '',
			model             TEXT    NOT NULL DEFAULT '',
			credit            REAL    NOT NULL DEFAULT 0,
			prompt_tokens     INTEGER NOT NULL DEFAULT 0,
			completion_tokens INTEGER NOT NULL DEFAULT 0,
			cached_tokens     INTEGER NOT NULL DEFAULT 0,
			created_at        TEXT    NOT NULL
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_wb_panel_usage_remote_request ON wb_panel_usage_remote(request_id) WHERE request_id != ''`,
		`CREATE INDEX IF NOT EXISTS idx_wb_panel_usage_remote_created ON wb_panel_usage_remote(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_wb_panel_usage_remote_channel ON wb_panel_usage_remote(channel_id)`,
		// 面板自身设置（单行表）：刷新并发数。
		`CREATE TABLE IF NOT EXISTS wb_panel_settings (
			id          INTEGER PRIMARY KEY CHECK (id = 1),
			concurrency INTEGER NOT NULL DEFAULT 3
		)`,
		`INSERT OR IGNORE INTO wb_panel_settings(id, concurrency) VALUES (1, 3)`,
	}
	for _, stmt := range stmts {
		if _, err := database.Exec(stmt); err != nil {
			return fmt.Errorf("workbuddy-panel: migrate: %w", err)
		}
	}
	// 旧库补列：auto_refresh（自动刷新开关，默认开）。
	if _, err := database.Exec("ALTER TABLE wb_panel_settings ADD COLUMN auto_refresh INTEGER NOT NULL DEFAULT 1"); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("workbuddy-panel: migrate auto_refresh: %w", err)
		}
	}
	// 增量拉水：记录每个 Key 上次流水同步时间（只拉这段时间之后的流水）。
	if _, err := database.Exec("ALTER TABLE wb_panel_credits ADD COLUMN usage_synced_at TEXT NOT NULL DEFAULT ''"); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("workbuddy-panel: migrate usage_synced_at: %w", err)
		}
	}
	return nil
}

// Apply 装配插件：建表、订阅网关事件、注册管理后台路由。
func (p *wbPanel) Apply(ctx plugin.Context) error {
	st, ok := ctx.Get("store").(*store.Store)
	if !ok || st == nil {
		return fmt.Errorf("workbuddy-panel: 缺少 store 服务")
	}
	lg, ok := ctx.Get("logger").(*slog.Logger)
	if !ok || lg == nil {
		return fmt.Errorf("workbuddy-panel: 缺少 logger 服务")
	}
	database, ok := ctx.Get("db").(*sql.DB)
	if !ok || database == nil {
		return fmt.Errorf("workbuddy-panel: 缺少 db 服务")
	}
	if err := migrate(database); err != nil {
		return err
	}
	svc := NewService(database, st, lg)
	ctx.Set("workbuddy-panel", svc)

	ctx.On(modelgateway.ProxyUpstreamSucceeded, svc.HandleProxyUpstreamSucceeded)

	// 管理后台路由（AuthSession 由 core/servercore 自动包装）。
	ctx.RegisterRoute(plugin.RouteSpec{
		Method:  http.MethodGet,
		Pattern: "GET /api/workbuddy-panel/data",
		Auth:    plugin.AuthSession,
		Handler: http.HandlerFunc(svc.HandleData),
	})
	ctx.RegisterRoute(plugin.RouteSpec{
		Method:  http.MethodPost,
		Pattern: "POST /api/workbuddy-panel/refresh",
		Auth:    plugin.AuthSession,
		Handler: http.HandlerFunc(svc.HandleRefresh),
	})
	ctx.RegisterRoute(plugin.RouteSpec{
		Method:  http.MethodGet,
		Pattern: "GET /api/workbuddy-panel/settings",
		Auth:    plugin.AuthSession,
		Handler: http.HandlerFunc(svc.HandleSettings),
	})
	ctx.RegisterRoute(plugin.RouteSpec{
		Method:  http.MethodPost,
		Pattern: "POST /api/workbuddy-panel/settings",
		Auth:    plugin.AuthSession,
		Handler: http.HandlerFunc(svc.HandleSaveSettings),
	})
	ctx.RegisterRoute(plugin.RouteSpec{
		Method:  http.MethodPost,
		Pattern: "POST /api/workbuddy-panel/settings/auto-refresh",
		Auth:    plugin.AuthSession,
		Handler: http.HandlerFunc(svc.HandleSaveAutoRefresh),
	})
	ctx.RegisterRoute(plugin.RouteSpec{
		Method:  http.MethodGet,
		Pattern: "GET /api/workbuddy-panel/settings/auto-refresh",
		Auth:    plugin.AuthSession,
		Handler: http.HandlerFunc(svc.HandleAutoRefreshState),
	})
	return nil
}
