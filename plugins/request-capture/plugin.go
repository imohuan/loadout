// Package requestcapture 请求抓包捕获插件（调试用途）：命中 request_capture 能力路由
// 的请求，在进入中转最前端（proxy:before-upstream，任何安检/改写之前）把完整原始
// 数据打包落盘（gzip JSON），文件名 = 请求 ID（与 request-log 的 request_id 一致）。
//
// 与 request-log 的区别：这里不做任何脱敏——Authorization 原文、图片 base64 全部
// 保留，目标是"逐字节重放当时的请求"；因此只建议在排障期间对指定模型临时开启。
package requestcapture

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"loadout/core/db"
	"loadout/core/plugin"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
)

type requestCapturePlugin struct{}

// New 创建 request-capture 插件实例。
func New() plugin.Plugin { return &requestCapturePlugin{} }

func (p *requestCapturePlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:    "request-capture",
		Version: "0.1.0",
		// 只依赖基础设施服务；「填入测试」复用的是 request-log 包里的纯函数
		// （编译期 import，不需要它的运行时实例），因此不额外声明 inject。
		Inject:  []string{"store", "logger", "db"},
		Provide: []string{"request-capture"},
	}
}

// envInt 读正整数环境变量；缺省/非法返回 def。
func envInt(key string, def int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return def
	}
	return n
}

func (p *requestCapturePlugin) Apply(ctx plugin.Context) error {
	st, ok := ctx.Get("store").(*store.Store)
	if !ok || st == nil {
		return fmt.Errorf("request-capture: missing store service")
	}
	lg, ok := ctx.Get("logger").(*slog.Logger)
	if !ok || lg == nil {
		return fmt.Errorf("request-capture: missing logger service")
	}
	database, ok := ctx.Get("db").(*sql.DB)
	if !ok || database == nil {
		return fmt.Errorf("request-capture: missing db service")
	}

	// 捕获目录：<data>/request-captures/（与 request-logs/ 同级）。
	dir := filepath.Join(filepath.Dir(st.Dir()), "request-captures")
	svc := NewService(st, lg, dir)
	// 保留策略：默认 0/0（不清），避免把正在排查的证据删掉。
	// 需要时用环境变量开启：LOADOUT_CAPTURE_MAX_AGE_DAYS / LOADOUT_CAPTURE_MAX_SIZE_MB。
	svc.SetRetention(
		envInt("LOADOUT_CAPTURE_MAX_AGE_DAYS", 0),
		envInt("LOADOUT_CAPTURE_MAX_SIZE_MB", 0),
	)
	if repo, err := db.NewRepository(database); err == nil {
		svc.SetRepository(repo)
	}
	ctx.Set("request-capture", svc)

	// 入口 hook：proxy:before-upstream 只在请求进入时触发一次，抓的是最原始数据。
	ctx.On(modelgateway.ProxyBeforeUpstream, svc.HandleProxyBeforeUpstream)

	// 启动时按保留策略清理一次（0/0 时是 no-op）。
	go func() {
		defer func() { _ = recover() }()
		if n := svc.ApplyRetention(); n > 0 {
			lg.Info("request-capture: 启动清理完成", "removed", n)
		}
	}()
	// 进程长期运行时的兜底清理（写盘路径已带 5 分钟节流的清理）。
	ticker := time.NewTicker(30 * time.Minute)
	ctx.Effect(func() { ticker.Stop() })
	go func() {
		defer func() { _ = recover() }()
		for range ticker.C {
			if n := svc.ApplyRetention(); n > 0 {
				lg.Info("request-capture: 定时清理完成", "removed", n)
			}
		}
	}()

	// API：列表 / 详情 / 删除单条 / 清空。注意静态路径要排在 /{id} 之前。
	ctx.RegisterRoute(plugin.RouteSpec{Method: http.MethodGet, Pattern: "GET /api/request-captures", Auth: plugin.AuthSession, Handler: http.HandlerFunc(svc.handleList)})
	ctx.RegisterRoute(plugin.RouteSpec{Method: http.MethodDelete, Pattern: "DELETE /api/request-captures", Auth: plugin.AuthSession, Handler: http.HandlerFunc(svc.handleClear)})
	ctx.RegisterRoute(plugin.RouteSpec{Method: http.MethodGet, Pattern: "GET /api/request-captures/{id}", Auth: plugin.AuthSession, Handler: http.HandlerFunc(svc.handleDetail)})
	ctx.RegisterRoute(plugin.RouteSpec{Method: http.MethodDelete, Pattern: "DELETE /api/request-captures/{id}", Auth: plugin.AuthSession, Handler: http.HandlerFunc(svc.handleDelete)})
	ctx.RegisterRoute(plugin.RouteSpec{Method: http.MethodPost, Pattern: "POST /api/request-captures/{id}/replay-script", Auth: plugin.AuthSession, Handler: http.HandlerFunc(svc.handleReplayScript)})
	ctx.RegisterRoute(plugin.RouteSpec{Method: http.MethodGet, Pattern: "GET /api/request-captures/{id}/download", Auth: plugin.AuthSession, Handler: http.HandlerFunc(svc.handleDownload)})

	ctx.RegisterCheck("request-capture 可用性", func() []plugin.Issue {
		var issues []plugin.Issue
		if err := os.MkdirAll(dir, 0o755); err != nil {
			issues = append(issues, plugin.Issue{Level: "error", Message: "捕获目录不可写: " + err.Error()})
		}
		return issues
	})
	return nil
}

