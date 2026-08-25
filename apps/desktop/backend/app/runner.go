// Package app 负责 Wails 应用生命周期管理：创建窗口、启动服务、事件循环。
package app

import (
	"embed"
	"runtime"

	"loadout/core/servercore"
	"proxyui/backend"
	"proxyui/backend/server"
	"proxyui/backend/singleton"
	"proxyui/icons"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
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
			//
			// 外层再套一层 SPA fallback：让 wails.localhost 或本地浏览器直接刷新
			// 任意前端路由（如 /settings）时，回退到 index.html 让 Vue Router 接管，
			// 避免裸 FileServer 报 404。资源文件（.js / .css / .png）的 404 不会
			// 被改写，前端资源缺失时仍能看到真实 404。
			Handler: server.NewProxyHandler(
				server.SPAFallback(
					application.BundledAssetFileServer(assets),
					assets,
				),
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

		// 关掉 Edge/Chromium 在 webview 内置的右键菜单（中文「返回/刷新/另存为/打印...」）。
		// 桌面端 UX 是应用本身，不需要把浏览器 chrome 暴露给用户；若以后想提供
		// 「选中复制」之类的菜单，在前端用 @contextmenu + 自绘菜单实现即可。
		DefaultContextMenuDisabled: true,

		// 全局快捷键：Ctrl+R 触发 webview 刷新（等价于浏览器 F5）。
		// wails KeyBindings 走 accelerator 解析（大小写不敏感），同一份配置在
		// macOS 上会被自动换为 Cmd+R，在 Linux 上保持 Ctrl+R。
		KeyBindings: withDebugKeyBindings(map[string]func(window application.Window){
			"Ctrl+R": func(w application.Window) {
				w.Reload()
			},
		}), // 所有版本共享 Ctrl+R；debug 版追加 Ctrl+Shift+I 打开 DevTools
	})
	// 点窗口关闭按钮（X）→ 拦截关闭事件，隐藏到系统托盘，进程保持驻留。
	// Wails v3 事件流程：WM_CLOSE 触发 WindowClosing → 先执行 RegisterHook 注册的
	// hook（此处 Cancel 后内置的「真正关闭」逻辑不会执行），窗口仅 Hide。
	// 唯一退出途径：托盘菜单「退出」（app.Quit() 走 destroy，不经过此拦截）。
	win.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		e.Cancel()
		win.Hide()
	})

	// 系统托盘：左键单击恢复窗口，右键菜单提供 打开 Loadout / 打开网页 / 退出
	setupTray(app, win)

	// 链接跳转守卫：应用内链接在窗口内打开，外部链接转系统默认浏览器
	injectLinkGuardScript(win)

	win.Show()

	// debug 版：自动打开 DevTools 供调试；release 版为空操作。
	debugOpenDevTools(win)

	// Wails v3 窗口图标：application.Options.Icon 在 v3 alpha 上不完全生效，
	// 且 Wails 内部 setIcon 只设 ICON_BIG（不设 ICON_SMALL），而注册窗口类时
	// IconSm = IDI_APPLICATION（系统默认占位）→ 标题栏左侧小图标是浅色方块。
	// 我们补 ICON_SMALL：用 Wails v3 默认窗口类名 WailsWebviewWindow 找 HWND。
	if runtime.GOOS == "windows" {
		setWindowIconAfterCreate("WailsWebviewWindow", config.App.Custom.Window.Title, appIcon)
	}

	// 阻塞直到窗口关闭
	app.Run()

	// 清理：触发 servercore 优雅退出（终止子进程、断开活跃连接、关闭 server）。
	// Wails 桌面版退出不会向 servercore 的 Run() goroutine 发送信号，
	// 必须显式调用 TriggerShutdown() 让其中的信号清理逻辑执行，否则子进程成为孤儿残留。
	servercore.TriggerShutdown()
	// 清理：停止 Loadout Server
	server.StopLoadoutServer()
}
