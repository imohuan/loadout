package visionv2

import (
	"context"
	"errors"
	"fmt"

	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// capabilityName 能力路由表中视觉能力的固定名称。
const capabilityName = "vision"

// channelScopeFromMetadata 从 pipe.Metadata 解析请求渠道上下文（统一入口，见 types.ChannelScopeFromMetadata）。
func channelScopeFromMetadata(md map[string]any, resolveBaseURL func(string) string) types.ChannelRequestScope {
	return types.ChannelScopeFromMetadata(md, resolveBaseURL)
}

// requestChannelBaseURL 取请求渠道的 base_url（用于渠道级匹配），无渠道或查不到返回空串。
func (s *Service) requestChannelBaseURL(channelID string) string {
	if channelID == "" || s.repo == nil {
		return ""
	}
	channels, err := s.repo.ListChannels(context.Background())
	if err != nil {
		return ""
	}
	for _, ch := range channels {
		if ch.ID == channelID {
			return ch.BaseURL
		}
	}
	return ""
}

// DecideRouteScope 查能力路由表：model + 请求渠道上下文（含聚合模型的候选 Key 集合）。
// scope.IDs 为实际命中的渠道 key 集合（单 key 或 __channel_candidates），
// scope.BaseURLs 为渠道组地址（__current_channel_base_url / 按 id 查表）。
// 只要路由约束（channel_ids / channel_base_urls）与请求上下文有交集即命中。
// SQLite repo 优先，store JSON 兜底。
func (s *Service) DecideRouteScope(model string, scope types.ChannelRequestScope) (*types.CapabilityRoute, error) {
	if s.repo != nil {
		routes, err := s.repo.ListCapabilityRoutes(context.Background())
		if err == nil {
			for i := range routes {
				if routes[i].Capability == capabilityName &&
					types.MatchModels(routes[i].Models, model) &&
					types.MatchChannelScopeEx(routes[i].ChannelIDs, routes[i].ChannelBaseURLs, scope) {
					return &routes[i], nil
				}
			}
			return nil, nil
		}
		s.lg.Warn("vision_v2: 从 SQLite 读能力路由表失败，回退 JSON", "err", err)
	}
	var routes []types.CapabilityRoute
	if err := s.st.Read(types.FileCapabilityRoutes, &routes); err != nil {
		if errors.Is(err, store.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("vision_v2: 读取能力路由表失败: %w", err)
	}
	for i := range routes {
		if routes[i].Capability == capabilityName &&
			types.MatchModels(routes[i].Models, model) &&
			types.MatchChannelScopeEx(routes[i].ChannelIDs, routes[i].ChannelBaseURLs, scope) {
			return &routes[i], nil
		}
	}
	return nil, nil
}

// visionError 构造统一的视觉能力错误（OpenAI error.type = vision_capability_error）。
func visionError(msg string) *modelgateway.GatewayError {
	return &modelgateway.GatewayError{Type: "vision_capability_error", Msg: msg}
}
