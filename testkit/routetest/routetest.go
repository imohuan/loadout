// Package routetest 提供能力路由测试的共享辅助，避免各插件测试包重复定义。
package routetest

import "loadout/plugins/types"

// FirstRoute 取首个命中路由（原 DecideRoute 便捷方法的语义：多命中取首条）。
// 若 err 非空或没有命中，返回 nil, err。
func FirstRoute(routes []*types.CapabilityRoute, err error) (*types.CapabilityRoute, error) {
	if err != nil || len(routes) == 0 {
		return nil, err
	}
	return routes[0], nil
}

// ScopeWithChannelID 构造单渠道的 ChannelRequestScope：channelID 非空 -> IDs=[channelID]，
// 并把已反查的渠道 base_url 列表挂到 BaseURLs。调用方需自行反查 base_url（各插件的
// requestChannelBaseURLs 反查逻辑依赖具体插件的数据源，故在此只接收结果）。
func ScopeWithChannelID(channelID string, baseURLs []string) types.ChannelRequestScope {
	sc := types.ChannelRequestScope{}
	if channelID != "" {
		sc.IDs = []string{channelID}
		if len(baseURLs) > 0 {
			sc.BaseURLs = baseURLs
		}
	}
	return sc
}
