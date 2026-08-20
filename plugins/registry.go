// Package plugins 是业务插件的注册表（方案 A：编译期装配）。
//
// 所有插件在 plugins/ 下以 Go 包形式存在，由本文件统一登记。
// 加插件 = 建目录写代码 → 在 All() 里追加一行 → 重新编译。
package plugins

import (
	"loadout/core/plugin"

	adminapi "loadout/plugins/admin-api"
	adminauth "loadout/plugins/admin-auth"
	aggregate "loadout/plugins/aggregate"
	gatewaykeys "loadout/plugins/gateway-keys"
	mcphub "loadout/plugins/mcp-hub"
	modelgateway "loadout/plugins/model-gateway"
	modelhealth "loadout/plugins/model-health"
	routelog "loadout/plugins/route-log"
	sensitivefilter "loadout/plugins/sensitive-filter"
	skills "loadout/plugins/skills"
	unifyai "loadout/plugins/unifyai"
	vision "loadout/plugins/vision"
)

// All 返回全部业务插件，按装配顺序列出（框架会按 inject/provide 自动拓扑排序）。
func All() []plugin.Plugin {
	return []plugin.Plugin{
		gatewaykeys.New(),
		adminauth.New(),
		skills.New(),
		unifyai.New(),
		modelgateway.New(),
		modelhealth.New(),
		routelog.New(),
		aggregate.New(),
		vision.New(),
		sensitivefilter.New(),
		mcphub.New(),
		adminapi.New(),
	}
}
