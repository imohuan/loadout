// Package forcestream 实现 Loadout 的强制流式能力适配器（plugins/force-stream）。
//
// 强制流式订阅 model-gateway 的 proxy:before-attempt waterfall 事件：命中能力路由
// （capability="force_stream"）且客户端为非流式 chat/completions 请求时，把上游请求体
// stream 改为 true 并打 __force_stream 标记。model-gateway 核心读标记后把上游 SSE 缓冲成
// 一份完整非流式 JSON 整包返回客户端（见 model-gateway/proxy.go readBufferedSSE）。
package forcestream

import (
	"database/sql"
	"fmt"
	"log/slog"

	"loadout/core/db"
	"loadout/core/plugin"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
)

// forceStreamPlugin 是 force-stream 插件的实现，编译期由 core 装配。
type forceStreamPlugin struct{}

// New 创建 force-stream 插件实例。
func New() plugin.Plugin {
	return &forceStreamPlugin{}
}

// Manifest 返回插件清单：依赖 store/logger/db，提供 force-stream 服务。
func (p *forceStreamPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:    "force-stream",
		Version: "0.1.0",
		Inject:  []string{"store", "logger", "db"},
		Provide: []string{"force-stream"},
	}
}

// Apply 装配插件：从容器取 store/logger/db，构造 Service 并注册为 force-stream 服务，
// 订阅每次渠道尝试安检事件（proxy:before-attempt）。
func (p *forceStreamPlugin) Apply(ctx plugin.Context) error {
	st := ctx.Get("store").(*store.Store)
	lg, ok := ctx.Get("logger").(*slog.Logger)
	if !ok || lg == nil {
		return fmt.Errorf("force-stream: missing logger service")
	}
	if st == nil {
		return fmt.Errorf("force-stream: missing store service")
	}
	svc := NewService(st, lg)
	if database, ok := ctx.Get("db").(*sql.DB); ok && database != nil {
		if repo, err := db.NewRepository(database); err == nil {
			svc.SetRepository(repo)
		}
	}
	ctx.Set("force-stream", svc)
	ctx.On(modelgateway.ProxyBeforeAttempt, svc.HandleProxyBeforeUpstream)
	return nil
}
