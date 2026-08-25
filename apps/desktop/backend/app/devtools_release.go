//go:build !debug

package app

import "github.com/wailsapp/wails/v3/pkg/application"

// withDebugKeyBindings 在 release 版保持通用快捷键原样返回，不追加 DevTools 快捷键。
func withDebugKeyBindings(base map[string]func(application.Window)) map[string]func(application.Window) {
	return base
}

// debugOpenDevTools 在 release 版为空操作，不打开 DevTools。
func debugOpenDevTools(win application.Window) {
	// release 版不自动打开 DevTools
}
