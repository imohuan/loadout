// Package routelog persists sanitized routing timelines without affecting forwarding.
package routelog

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"loadout/core/plugin"
)

type routeLogPlugin struct{}

func New() plugin.Plugin { return &routeLogPlugin{} }

func (p *routeLogPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{Name: "route-log", Version: "0.1.0", Inject: []string{"db", "logger"}, Provide: []string{"route-log"}}
}

func (p *routeLogPlugin) Apply(ctx plugin.Context) error {
	database, ok := ctx.Get("db").(*sql.DB)
	if !ok || database == nil {
		return fmt.Errorf("route-log: missing db service")
	}
	logger, ok := ctx.Get("logger").(*slog.Logger)
	if !ok || logger == nil {
		return fmt.Errorf("route-log: missing logger service")
	}
	svc := NewService(database, logger)
	// 装配顺序上 route-log 早于 request-log，这里拿不到对方实例；登记回调由
	// request-log 装配完成后反向注入（见 plugins/request-log/plugin.go）。
	ctx.SetRouteLogPresenceHook(func(fn func(context.Context, []string) (map[string]bool, error)) {
		svc.SetRequestLogLookup(fn)
	})
	ctx.Set("route-log", svc)
	return nil
}
