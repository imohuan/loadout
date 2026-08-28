// Package messageinject 实现 Loadout 的消息注入能力适配器（plugins/message-inject）。
//
// 消息注入订阅 model-gateway 的 proxy:before-attempt waterfall 事件，在请求转发
// 上游前往请求体 messages 数组注入自定义内容（指定 role + 注入位置）。
//
// 路由语义（capability_routes.json，capability="message_inject"）：
//   - native：原样透传，不做任何注入；
//   - proxy ：按 injections 配置逐条注入（prepend / append / prepend_first / append_first）。
//
// 与 sensitive-filter / field-filter 的差异：不改动现有消息文本，而是往 messages
// 新增消息（prepend/append）或把内容拼到原始第一条消息的开头/结尾
// （prepend_first/append_first），用于注入 system 约束、固定示例等。
package messageinject

import (
	"database/sql"
	"fmt"
	"log/slog"

	"loadout/core/db"
	"loadout/core/plugin"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
)

// messageInjectPlugin 是 message-inject 插件的实现，编译期由 core 装配。
type messageInjectPlugin struct{}

// New 创建 message-inject 插件实例。
func New() plugin.Plugin {
	return &messageInjectPlugin{}
}

// Manifest 返回插件清单：依赖 store/logger/db，提供 message-inject 服务。
func (p *messageInjectPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:    "message-inject",
		Version: "0.1.0",
		Inject:  []string{"store", "logger", "db"},
		Provide: []string{"message-inject"},
	}
}

// Apply 装配插件：从容器取 store/logger/db，构造 Service 并注册为 message-inject 服务，
// 订阅每次渠道尝试安检事件（proxy:before-attempt）。
func (p *messageInjectPlugin) Apply(ctx plugin.Context) error {
	st := ctx.Get("store").(*store.Store)
	lg, ok := ctx.Get("logger").(*slog.Logger)
	if !ok || lg == nil {
		return fmt.Errorf("message-inject: missing logger service")
	}
	if st == nil {
		return fmt.Errorf("message-inject: missing store service")
	}
	svc := NewService(st, lg)
	if database, ok := ctx.Get("db").(*sql.DB); ok && database != nil {
		if repo, err := db.NewRepository(database); err == nil {
			svc.SetRepository(repo)
		}
	}
	ctx.Set("message-inject", svc)
	ctx.On(modelgateway.ProxyBeforeAttempt, svc.HandleProxyBeforeUpstream)
	return nil
}
