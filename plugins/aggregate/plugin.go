// Package aggregate 实现聚合模型（轮询）插件：定义虚拟模型名，挂多个真实模型+渠道，
// 按优先级轮询，任一成功即返回。
//
// 配置文件：~/.loadout/data/aggregates.json
// 格式：[{"name": "auto", "targets": [{"model": "gpt-4", "channel_id": "ch-openai"}, ...]}]
//
// 订阅 model-gateway 的 chat:before-upstream 事件，检测聚合模型后拦截请求，
// 自己循环转发到配置的目标列表，不依赖 model-gateway 的渠道解析。
package aggregate

import (
	"database/sql"
	"fmt"
	"log/slog"

	"loadout/core/plugin"
	"loadout/core/store"
	"loadout/plugins/contracts"
	modelgateway "loadout/plugins/model-gateway"
)

// aggregatePlugin 是 aggregate 插件的实现，编译期由 core 装配。
type aggregatePlugin struct{}

// New 创建 aggregate 插件实例。
func New() plugin.Plugin {
	return &aggregatePlugin{}
}

// Manifest 返回插件清单：依赖 store/logger，提供 aggregate 服务。
func (p *aggregatePlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:    "aggregate",
		Version: "0.1.0",
		Inject:  []string{"store", "db", "logger", "model-health"},
		Provide: []string{"aggregate"},
	}
}

// Apply 装配插件：订阅请求前、失败和成功事件，启动后台健康检查。
func (p *aggregatePlugin) Apply(ctx plugin.Context) error {
	st := ctx.Get("store").(*store.Store)
	lg := ctx.Get("logger").(*slog.Logger)

	svc := NewService(st, lg, ctx)
	database, ok := ctx.Get("db").(*sql.DB)
	if !ok || database == nil {
		return fmt.Errorf("aggregate: missing db service")
	}
	health, ok := ctx.Get("model-health").(contracts.ModelHealth)
	if !ok {
		return fmt.Errorf("aggregate: missing model-health service")
	}
	svc.SetRoutingServices(database, health)
	ctx.Set("aggregate", svc)

	// 订阅透明代理事件（当前主力链路）
	ctx.On(modelgateway.ProxyBeforeUpstream, svc.HandleProxyBeforeUpstream)
	ctx.On(modelgateway.ProxyUpstreamFailed, svc.HandleProxyUpstreamFailed)
	ctx.On(modelgateway.ProxyUpstreamSucceeded, svc.HandleProxyUpstreamSucceeded)

	// 订阅旧 chat 事件（过渡期保留，旧 HandleChat 下线后移除）
	ctx.On(modelgateway.EventBeforeUpstream, svc.HandleBeforeUpstream)
	ctx.On(modelgateway.EventUpstreamFailed, svc.HandleUpstreamFailed)
	ctx.On(modelgateway.EventUpstreamSucceeded, svc.HandleUpstreamSucceeded)

	return nil
}
