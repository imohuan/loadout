// Package gatewaykeys 实现 sk- key（模型 API）与 MCP endpoint key 的签发与校验。
//
// 通过 core/store 读写 api_keys.json / mcp_keys.json（JSON 数组）：完整 key 只在
// 创建时展示一次，落盘只存 sha256 哈希与展示前缀；并提供两个 HTTP 认证中间件
// 供 model-gateway / mcp-hub 挂载。
package gatewaykeys

import (
	"database/sql"

	"loadout/core/db"
	"loadout/core/plugin"
	"loadout/core/store"
)

// Plugin 是 gateway-keys 插件：向服务容器注册 *Manager。
type Plugin struct{}

// New 创建 gateway-keys 插件实例。
func New() plugin.Plugin {
	return &Plugin{}
}

// Manifest 声明插件元信息：依赖 store/db，提供 gateway-keys。
func (p *Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:    "gateway-keys",
		Version: "0.1.0",
		Inject:  []string{"store", "db"},
		Provide: []string{"gateway-keys"},
	}
}

// Apply 启动插件：从容器取 *store.Store，构造 Manager 并以 "gateway-keys" 暴露。
func (p *Plugin) Apply(ctx plugin.Context) error {
	st := ctx.Get("store").(*store.Store)
	m := NewManager(st)
	if database, ok := ctx.Get("db").(*sql.DB); ok && database != nil {
		if repo, err := db.NewRepository(database); err == nil {
			m.SetRepository(repo)
		}
	}
	ctx.Set("gateway-keys", m)
	return nil
}
