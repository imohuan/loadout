//go:build debug

package app

import "github.com/wailsapp/wails/v3/pkg/application"

// withDebugKeyBindings 在通用快捷键之上追加 debug 版的 Ctrl+Shift+I（切换 DevTools）。
func withDebugKeyBindings(base map[string]func(application.Window)) map[string]func(application.Window) {
	if base == nil {
		base = map[string]func(application.Window){}
	}
	base["Ctrl+Shift+I"] = func(w application.Window) {
		w.OpenDevTools()
	}
	return base
}

// debugOpenDevTools 在 debug 版启动时自动打开 DevTools，方便立即调试。
func debugOpenDevTools(win application.Window) {
	win.OpenDevTools()
}
