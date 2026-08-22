// Package modelhealth owns channel and model availability state.
package modelhealth

import (
	"database/sql"
	"log/slog"

	"loadout/core/plugin"
)

type healthPlugin struct{}

func New() plugin.Plugin { return &healthPlugin{} }

func (p *healthPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{Name: "model-health", Version: "0.1.0", Inject: []string{"db", "logger"}, Provide: []string{"model-health"}}
}

func (p *healthPlugin) Apply(ctx plugin.Context) error {
	database, ok := ctx.Get("db").(*sql.DB)
	if !ok || database == nil {
		return pluginError("db")
	}
	logger, ok := ctx.Get("logger").(*slog.Logger)
	if !ok || logger == nil {
		return pluginError("logger")
	}
	service := NewService(database, logger)
	ctx.Set("model-health", service)
	ctx.Effect(service.Start())
	return nil
}
