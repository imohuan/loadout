// Package plugin 是 Loadout 的插件框架——Cordis 思想的 Go 实现（编译期装配，方案 A）。
//
// 五个核心概念在 Go 侧的落地：
//
//	插件声明依赖 inject   → Manifest.Inject，依赖就绪才启动
//	服务容器 ctx.xxx       → Context.Get / Context.Set，按接口取用，不 import 实现
//	可逆副作用 ctx.effect() → Context.Effect(fn) Disposer，卸载时逆序执行
//	事件总线（emit/waterfall/serial）→ Context.On/Emit/Waterfall
//	一切皆插件             → 业务全部以插件形式挂在 core 上，core 不 import 任何业务
//
// 一个插件 = 一个目录（plugins/vision/），通过 plugin.go 里的 Apply(ctx) 挂载。
// 装配由 Load() 完成：读 Manifest → 按 inject/provide 拓扑排序 → 逐个 Apply。
package plugin

import (
	"net/http"
)

// Disposer 撤销一次副作用（卸载某服务、取消某监听、注销某路由等）。
type Disposer func()

// Manifest 插件清单，对应 plugins/*/plugin.yaml 的 name/version/inject/provide。
type Manifest struct {
	Name    string   `yaml:"name"`    // 插件唯一名，如 "vision"
	Version string   `yaml:"version"` // 语义化版本，如 "0.1.0"
	Inject  []string `yaml:"inject"`  // 依赖的服务名，就绪后才启动
	Provide []string `yaml:"provide"` // 提供的服务名，供其他插件 inject
}

// Plugin 是任何插件的统一入口：声明 Manifest，并在 Apply 中挂载能力。
type Plugin interface {
	Manifest() Manifest
	Apply(ctx Context) error // 启动入口；返回 error 时装配中止
}

// Issue 插件自检结果（插件「自己检查问题所在」的产物）。
type Issue struct {
	Level   string `json:"level"` // info / warn / error
	Message string `json:"message"`
}

// PluginCheck 一个插件的自检结果：插件名 + 它注册的全部检查项。
type PluginCheck struct {
	Plugin string        `json:"plugin"`
	Checks []CheckResult `json:"checks"`
}

// CheckResult 一个检查项的运行结果。
type CheckResult struct {
	Name   string  `json:"name"`
	Issues []Issue `json:"issues"`
}

// AuthKind 认证类别，框架据此为路由挂认证中间件并分发。
type AuthKind string

const (
	AuthSession   AuthKind = "session" // 管理后台登录（session/JWT）
	AuthSkKey     AuthKind = "sk-key"  // 模型 API
	AuthMCPHeader AuthKind = "mcp-key" // MCP 端点
	AuthPublic    AuthKind = "public"  // 无需认证
)

// RouteSpec 插件注册的 HTTP 路由。Method 为空表示匹配任意方法。
type RouteSpec struct {
	Method  string   // GET / POST / ...（空 = 全部方法）
	Pattern string   // 路由路径：/v1/chat/completions、/mcp/github、/api/...
	Auth    AuthKind // 认证类别
	Handler http.Handler
}
