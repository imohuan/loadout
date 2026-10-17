// Package requestcapture 请求捕获插件（调试抓包）：命中 request_capture 能力路由的
// 请求，在进入中转最前端（proxy:before-upstream，任何安检/改写之前）把完整原始数据
// 打包落盘（gzip JSON），文件名 = 请求 ID（与 request-log 的 request_id 一致，可互相检索）。
package requestcapture

import (
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"

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
		Inject:  []string{"store", "logger", "db"},
		Provide: []string{"request-capture"},
	}
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
	if repo, err := db.NewRepository(database); err == nil {
		svc.SetRepository(repo)
	}
	ctx.Set("request-capture", svc)
	// 入口 hook：proxy:before-upstream 只在请求进入时触发一次，抓的是最原始数据。
	ctx.On(modelgateway.ProxyBeforeUpstream, svc.HandleProxyBeforeUpstream)
	return nil
}

