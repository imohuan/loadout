// Loadout 服务器入口。
package main

import (
	"flag"
	"log/slog"
	"net/http"
	"strings"

	"loadout/core/config"
	"loadout/core/plugin"
	"loadout/core/servercore"
	"loadout/core/store"
)

// Run 启动服务器。
// 支持 --port 指定监听端口（优先级高于环境变量 LOADOUT_SERVER_ADDR，高于默认 :3000）。
func Run() error {
	port := flag.String("port", "", "监听端口，例如 --port 8080（覆盖 LOADOUT_SERVER_ADDR）")
	flag.Parse()
	if strings.TrimSpace(*port) != "" {
		config.ServerAddr = ":" + strings.TrimSpace(*port)
	}
	return servercore.Run()
}

func assemble(lg *slog.Logger, st *store.Store) (*plugin.Assembly, http.Handler, error) {
	return servercore.Assemble(lg, st)
}
