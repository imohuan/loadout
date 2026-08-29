// Package sensitivefilter 实现 Loadout 的敏感词过滤能力适配器（plugins/sensitive-filter）。
//
// 敏感词过滤订阅 model-gateway 的 proxy:before-upstream waterfall 事件，
// 在请求转发上游前对原始请求体做整体字符串替换（stringify → 替换 → 校验 → 写回）。
//
// 路由语义（capability_routes.json，capability="sensitive_filter"）：
//   - native：原样透传，不做任何处理；
//   - proxy ：按 replacements 顺序对请求体做字符串/正则替换，替换后必须是合法 JSON；
//   - error ：请求体命中任一敏感词规则直接拒绝（不替换）。
//
// 与 vision 插件的差异：
//   - 不限对话格式，任意 /v1/{path...} 只要 body 是合法 JSON 即生效；
//   - 不调用上游模型、不查渠道、不解析请求结构，纯本地字符串替换。
package sensitivefilter

import (
	"database/sql"
	"fmt"
	"log/slog"

	"loadout/core/db"
	"loadout/core/plugin"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
)

// sensitiveFilterPlugin 是 sensitive-filter 插件的实现，编译期由 core 装配。
type sensitiveFilterPlugin struct{}

// New 创建 sensitive-filter 插件实例。
func New() plugin.Plugin {
	return &sensitiveFilterPlugin{}
}

// Manifest 返回插件清单：依赖 store/logger/db，提供 sensitive-filter 服务。
//
// Inject 额外声明依赖 field-filter 与 message-inject，仅为**排序约束**，并非取用其服务：
// 三者都订阅 proxy:before-attempt 改同一份请求体，sensitive-filter 做的是整体字符串替换，
// 必须**最后**执行，才能把 message-inject 新注入内容里的敏感词一并过滤掉。
// 依赖声明让 topoSort 强制本插件排在其后（不再依赖插件名字典序巧合）；若这两插件被移除，
// 本插件会因依赖无人提供而启动失败，防止顺序契约被无意破坏。
func (p *sensitiveFilterPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:    "sensitive-filter",
		Version: "0.1.0",
		Inject:  []string{"store", "logger", "db", "field-filter", "message-inject"},
		Provide: []string{"sensitive-filter"},
	}
}

// Apply 装配插件：从容器取 store/logger/db，构造 Service 并注册为 sensitive-filter 服务，
// 订阅透明代理输入 hook（proxy:before-upstream）。
func (p *sensitiveFilterPlugin) Apply(ctx plugin.Context) error {
	st := ctx.Get("store").(*store.Store)
	lg, ok := ctx.Get("logger").(*slog.Logger)
	if !ok || lg == nil {
		return fmt.Errorf("sensitive-filter: missing logger service")
	}
	if st == nil {
		return fmt.Errorf("sensitive-filter: missing store service")
	}
	svc := NewService(st, lg)
	if database, ok := ctx.Get("db").(*sql.DB); ok && database != nil {
		if repo, err := db.NewRepository(database); err == nil {
			svc.SetRepository(repo)
		}
	}
	ctx.Set("sensitive-filter", svc)
	// 订阅每次渠道尝试安检事件（proxy:before-attempt）：切换渠道/切换模型后
	// 敏感词过滤仍按当前渠道上下文重新匹配路由执行。
	ctx.On(modelgateway.ProxyBeforeAttempt, svc.HandleProxyBeforeUpstream)
	return nil
}
