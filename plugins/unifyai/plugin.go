// Package unifyai 插件：把 Loadout 管理后台的「UnifyAI 配置同步」页面
// 连接到本地 unifyai CLI（模型/MCP 配置同步工具）。
//
// 插件装配（core/plugin 框架）：
//   - 依赖 logger；
//   - 提供服务 unifyai（供上层拿取 *Service）；
//   - Apply 阶段组装 Service 并注册到容器。
package unifyai

import (
	"log/slog"

	"loadout/core/plugin"
)

// unifyaiPlugin 是 unifyai 插件的实现，编译期由 core 装配。
type unifyaiPlugin struct{}

// New 创建 unifyai 插件实例。
func New() plugin.Plugin {
	return &unifyaiPlugin{}
}

// Manifest 返回插件清单：依赖 logger，提供 unifyai。
func (p *unifyaiPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:    "unifyai",
		Version: "0.1.0",
		Inject:  []string{"logger"},
		Provide: []string{"unifyai"},
	}
}

// Apply 装配插件：从容器取 logger，创建并注册 Service。
func (p *unifyaiPlugin) Apply(ctx plugin.Context) error {
	lg := ctx.Get("logger").(*slog.Logger)
	svc := NewService(lg)
	ctx.Set("unifyai", svc)
	return nil
}
