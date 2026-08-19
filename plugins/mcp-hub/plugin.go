// Package mcphub 实现 Loadout 的 MCP 聚合网关插件：
// 把所有上游 MCP 工具与已安装技能聚合进一个索引，每个端点只暴露
// status/get/invoke 三个工具，支持单 MCP / 分组 / $smart 三种路由方式。
package mcphub

import (
	"database/sql"
	"fmt"
	"log/slog"

	"loadout/core/plugin"
	"loadout/core/store"
)

// mcpHubPlugin 是 mcp-hub 插件的实现：在 Apply 中组装 Service 并注册为 "mcp-hub"。
type mcpHubPlugin struct{}

// New 创建 mcp-hub 插件（符合插件约定：导出 func New() plugin.Plugin）。
func New() plugin.Plugin {
	return &mcpHubPlugin{}
}

// Manifest 声明插件元数据：名称为 "mcp-hub"，依赖 store/logger/db，提供 "mcp-hub" 服务。
func (p *mcpHubPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:    "mcp-hub",
		Version: "0.1.0",
		Inject:  []string{"store", "logger", "db"},
		Provide: []string{"mcp-hub"},
	}
}

// Apply 组装插件：从容器取 store、logger 与共享 db，建 mcp_invocations 表，
// 创建聚合网关服务并注册到容器。
func (p *mcpHubPlugin) Apply(ctx plugin.Context) error {
	st := ctx.Get("store").(*store.Store)
	lg := ctx.Get("logger").(*slog.Logger)
	db := ctx.Get("db").(*sql.DB)
	if err := migrate(db); err != nil {
		return fmt.Errorf("mcp-hub: migrate: %w", err)
	}
	svc := NewService(st, lg, db)
	ctx.Set("mcp-hub", svc)
	return nil
}
