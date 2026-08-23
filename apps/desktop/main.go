package main

import (
	"embed"
	"proxyui/backend/app"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app.Run(assets)
}