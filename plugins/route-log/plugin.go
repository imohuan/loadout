// Package routelog persists sanitized routing timelines without affecting forwarding.
package routelog

import (
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
	ctx.Set("route-log", NewService(database, logger))
	return nil
}
