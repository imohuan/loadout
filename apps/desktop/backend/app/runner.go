// Package app 负责 Wails 应用生命周期管理：创建窗口、启动服务、事件循环。
package app

import (
	"embed"
	"os"
	"runtime"

	"proxyui/backend"
	"proxyui/backend/server"
	"proxyui/backend/singleton"

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

	// 从 wails.json 配置中读取任务栏图标
	appIcon, _ := os.ReadFile(config.App.Custom.Icons.Taskbar)

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

	// Windows 下通过 WM_SETICON 设置任务栏和 Alt+Tab 图标
	// application.Options.Icon 在 Windows 上只影响 system tray
	if runtime.GOOS == "windows" {
		wndClass := config.App.Name + "Wnd"
		setWindowIconAfterCreate(wndClass, config.App.Custom.Window.Title, appIcon)
	}

	// 阻塞直到窗口关闭
	app.Run()

	// 清理：停止 Loadout Server
	server.StopLoadoutServer()
}
