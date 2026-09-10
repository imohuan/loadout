package plugin

import (
	"context"
	"log/slog"
)

// Handler 事件处理器。返回 (新 payload, error)。
//   - Emit 分发时忽略返回值、仅记录错误；
//   - Waterfall 分发时把返回值作为下一个处理器的输入，遇错即停。
type Handler func(payload any) (any, error)

// Context 是插件与框架交互的唯一入口。插件不 import 任何具体实现，
// 只通过本接口取用服务、挂载路由、订阅事件、注册副作用与自检。
type Context interface {
	// Get 按服务名取用已注册的服务；不存在返回 nil。
	Get(name string) any
	// Set 注册一个服务，返回撤销器；卸载时框架会自动逆序调用。
	Set(name string, svc any) Disposer
	// On 订阅事件，返回取消订阅的 Disposer。
	On(event string, h Handler) Disposer
	// Emit 触发事件：顺序调用所有处理器，忽略返回值，错误仅记日志。
	Emit(event string, payload any)
	// Waterfall 触发事件：返回值作为下一个处理器输入，遇错即停。
	Waterfall(event string, payload any) (any, error)
	// Effect 注册可逆副作用，卸载时逆序执行返回的清理函数。
	Effect(fn func()) Disposer
	// Logger 返回带当前插件名的日志器（源码位置显示 plugins/xxx/plugin.go:行号）。
	Logger() *slog.Logger
	// RegisterCheck 注册插件自检项，供启动日志与管理后台展示。
	RegisterCheck(name string, fn func() []Issue)
	// RegisterRoute 注册 HTTP 路由，返回注销器。
	RegisterRoute(spec RouteSpec) Disposer
	// SetRouteLogPresenceHook 登记「完整日志存在性查询」回调。
	//
	// route-log 需要知道 request-log 库里的日志是否还在（列表要隐藏失效的「进入日志」
	// 入口），但装配顺序上 route-log 早于 request-log，往前拿不到对方实例。于是
	// route-log 先登记一个安装器，request-log 装配完成后回填真正的查询函数。
	// 未装配 request-log 时安装器不会被调用，route-log 侧保持 nil（退化为不做校验）。
	SetRouteLogPresenceHook(install func(fn func(ctx context.Context, ids []string) (map[string]bool, error)))
	// InstallRouteLogPresence 回填上面登记的存在性查询（由 request-log 装配时调用）。
	// 返回是否找到已登记的安装器（false = route-log 未装配，本次回填被忽略）。
	InstallRouteLogPresence(fn func(ctx context.Context, ids []string) (map[string]bool, error)) bool
}
