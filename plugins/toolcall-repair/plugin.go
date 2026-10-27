package toolcallrepair

import (
	"database/sql"
	"fmt"
	"log/slog"

	"loadout/core/db"
	"loadout/core/plugin"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
)

// repairPlugin 是 toolcall-repair 插件的实现，编译期由 core 装配。
type repairPlugin struct{}

func New() plugin.Plugin { return &repairPlugin{} }

func (p *repairPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:    "toolcall-repair",
		Version: "0.1.0",
		Inject:  []string{"store", "logger", "db"},
		Provide: []string{"toolcall-repair"},
	}
}

// Apply 装配插件并订阅「每次渠道尝试前」的 hook（proxy:before-attempt）。
//
// 为什么不用入口（proxy:before-upstream）：入口 metadata 里还没有真实渠道，
// channel_ids 级规则永远匹配不上。补全必须按当前渠道决定是否启用，所以在
// __current_channel 写入后的 before-attempt 阶段执行；未命中的渠道会还原原始 body。
func (p *repairPlugin) Apply(ctx plugin.Context) error {
	st := ctx.Get("store").(*store.Store)
	lg, ok := ctx.Get("logger").(*slog.Logger)
	if !ok || lg == nil {
		return fmt.Errorf("toolcall-repair: missing logger service")
	}
	if st == nil {
		return fmt.Errorf("toolcall-repair: missing store service")
	}
	svc := NewService(st, lg)
	if database, ok := ctx.Get("db").(*sql.DB); ok && database != nil {
		if repo, err := db.NewRepository(database); err == nil {
			svc.SetRepository(repo)
		}
	}
	ctx.Set("toolcall-repair", svc)
	ctx.On(modelgateway.ProxyBeforeAttempt, svc.HandleProxyBeforeAttempt)
	return nil
}
