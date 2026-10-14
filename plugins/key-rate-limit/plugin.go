// Package keyratelimit 插件装配入口：注入仓储、注册服务、订阅候选解析与尝试前事件。
package keyratelimit

import (
	"database/sql"
	"fmt"
	"log/slog"

	"loadout/core/db"
	"loadout/core/plugin"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
)

// keyRateLimitPlugin 是 key-rate-limit 插件的实现，编译期由 core 装配。
type keyRateLimitPlugin struct{}

// New 创建 key-rate-limit 插件实例。
func New() plugin.Plugin { return &keyRateLimitPlugin{} }

// Manifest 返回插件清单：依赖 store/logger/db，提供 key-rate-limit 服务。
//
// 订阅的是 model-gateway 的事件（走 ctx.On），不需要它的服务实例，因此 Inject 里不列。
func (p *keyRateLimitPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:    "key-rate-limit",
		Version: "0.1.0",
		Inject:  []string{"store", "logger", "db"},
		Provide: []string{"key-rate-limit"},
	}
}

// Apply 装配插件：构造 Service、注入 SQLite 仓储、注册服务、订阅事件、启动计数桶清扫。
// 仓储仅用于读能力路由/渠道表，拿不到时插件自动回退 JSON 数据源。
func (p *keyRateLimitPlugin) Apply(ctx plugin.Context) error {
	st, ok := ctx.Get("store").(*store.Store)
	if !ok || st == nil {
		return fmt.Errorf("key-rate-limit: missing store service")
	}
	lg, ok := ctx.Get("logger").(*slog.Logger)
	if !ok || lg == nil {
		return fmt.Errorf("key-rate-limit: missing logger service")
	}
	svc := NewService(st, lg)
	if database, ok := ctx.Get("db").(*sql.DB); ok && database != nil {
		if repo, err := db.NewRepository(database); err == nil {
			svc.SetRepository(repo)
		}
	}
	ctx.Set("key-rate-limit", svc)
	// 候选解析完成：剔除已达上限的 Key（必要时返回 429 拒绝本次请求）。
	ctx.On(modelgateway.ProxyChannelsResolved, svc.HandleChannelsResolved)
	// 每次渠道尝试前：对本次真实要用的 Key 记一次消费。
	ctx.On(modelgateway.ProxyBeforeAttempt, svc.HandleBeforeAttempt)
	// 每次尝试结束（成功/失败/拒绝均由核心 defer 触发）：释放并发占位。
	ctx.On(modelgateway.ProxyAttemptFinished, svc.HandleAttemptFinished)
	// 后台清扫空闲计数桶（内存有界）。
	ctx.Effect(svc.StartSweeper())
	return nil
}

