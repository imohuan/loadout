// Package app 负责 Wails 应用生命周期管理：创建窗口、启动服务、事件循环。
package app

import (
	"embed"
	"runtime"

	"proxyui/backend"
	"proxyui/backend/server"
	"proxyui/backend/singleton"
	"proxyui/icons"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// Run 启动 Wails 应用。assets 为前端构建产物的 embed 文件系统。
// 启动顺序：杀旧进程 → 启动 Loadout Server → 创建服务 → 创建 Wails 应用 → 创建窗口 → 事件循环。
func Run(assets embed.FS) {
	singleton.KillExistingInstance()

	// 启动 Loadout Server 后端服务
	if err := server.StartLoadoutServer(); err != nil {
		// 不阻塞启动，仅记录错误
	}

	svc := server.New()

	// 图标直接用 embed 的 appicon.ico（不受运行 cwd 影响；从 dist/ 启动也能命中）
	appIcon := icons.AppIcon

	app := application.New(application.Options{
		Name:        config.App.Name,
		Description: config.App.Description,
		Icon:        appIcon,
		Assets: application.AssetOptions{
			// 使用代理 handler 包装静态资源服务器
			// API 请求 (/api/*, /v1/*, /mcp/*) 代理到 Loadout Server (127.0.0.1:3000)
			// 其他请求走静态资源服务 (开发模式自动代理到 Vite)
			Handler: server.NewProxyHandler(
				application.AssetFileServerFS(assets),
				"http://127.0.0.1:3000",
			),
		},
	})

	// 给 HTTP 服务注入 Wails app 引用，使其可以控制窗口
	svc.SetApp(app)
	svc.Start()

	// WebView URL 始终为 "/"，开发模式由 FRONTEND_DEVSERVER_URL 控制
	win := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:     config.App.Custom.Window.Title,
		Width:     config.App.Custom.Window.Width,
		Height:    config.App.Custom.Window.Height,
		MinWidth:  config.App.Custom.Window.MinWidth,
		MinHeight: config.App.Custom.Window.MinHeight,
		URL:       "/",
		Frameless: config.App.Custom.Window.Frameless,
	})
	win.Show()

	// 开发阶段自动打开 DevTools，生产构建时建议注释掉
	win.OpenDevTools()

	// Wails v3 窗口图标：application.Options.Icon 在 v3 alpha 上不完全生效，
	// 且 Wails 内部 setIcon 只设 ICON_BIG（不设 ICON_SMALL），而注册窗口类时
	// IconSm = IDI_APPLICATION（系统默认占位）→ 标题栏左侧小图标是浅色方块。
	// 我们补 ICON_SMALL：用 Wails v3 默认窗口类名 WailsWebviewWindow 找 HWND。
	if runtime.GOOS == "windows" {
		setWindowIconAfterCreate("WailsWebviewWindow", config.App.Custom.Window.Title, appIcon)
	}

	// 阻塞直到窗口关闭
	app.Run()

	// 清理：停止 Loadout Server
	server.StopLoadoutServer()
}
