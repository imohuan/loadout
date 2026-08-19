// Loadout 服务器入口。
package main

import (
	"log/slog"
	"net/http"

	"loadout/core/plugin"
	"loadout/core/servercore"
	"loadout/core/store"
)

// Run 启动服务器。
func Run() error {
	return servercore.Run()
}

func assemble(lg *slog.Logger, st *store.Store) (*plugin.Assembly, http.Handler, error) {
	return servercore.Assemble(lg, st)
}
