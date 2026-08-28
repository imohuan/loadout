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
	fieldfilter "loadout/plugins/field-filter"
	gatewaykeys "loadout/plugins/gateway-keys"
	mcphub "loadout/plugins/mcp-hub"
	messageinject "loadout/plugins/message-inject"
	modelgateway "loadout/plugins/model-gateway"
	modelhealth "loadout/plugins/model-health"
	requestlog "loadout/plugins/request-log"
	routelog "loadout/plugins/route-log"
	sensitivefilter "loadout/plugins/sensitive-filter"
	skills "loadout/plugins/skills"
	translate "loadout/plugins/translate"
	unifyai "loadout/plugins/unifyai"
	// vision "loadout/plugins/vision"
	visionv2 "loadout/plugins/vision_v2"
	volcfreequota "loadout/plugins/volc-free-quota"
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
		messageinject.New(),
		routelog.New(),
		requestlog.New(),
		aggregate.New(),
		// 原 vision 插件已停用，由 visionv2 取代
		visionv2.New(),
		sensitivefilter.New(),
		fieldfilter.New(),
		mcphub.New(),
		adminapi.New(),
		volcfreequota.New(),
		translate.New(),
	}
}
