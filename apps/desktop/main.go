package main

import (
	"embed"
	"flag"
	"strings"

	"loadout/core/config"
	"proxyui/backend/app"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	port := flag.String("port", "", "监听端口，例如 --port 8080（覆盖 LOADOUT_SERVER_ADDR）")
	flag.Parse()
	if strings.TrimSpace(*port) != "" {
		config.ServerAddr = ":" + strings.TrimSpace(*port)
	}
	app.Run(assets)
}