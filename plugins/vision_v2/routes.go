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
func channelScopeFromMetadata(md map[string]any, resolveBaseURLs func(string) []string) types.ChannelRequestScope {
	return types.ChannelScopeFromMetadata(md, resolveBaseURLs)
}

// requestChannelBaseURLs 反查渠道 base_url 列表：term 可为渠道 key id（精确匹配，返回该 key
// 所在渠道组的 base_url）或渠道名 ChannelName（返回组内全部启用 Key 共享的 base_url，去重）。
// 无渠道或查不到返回空 slice。入口阶段（BeforeUpstream）只有 __channel_hint 渠道名时
// 也能反查，供渠道级约束（channel_base_urls）路由匹配。
func (s *Service) requestChannelBaseURLs(term string) []string {
	if term == "" || s.repo == nil {
		return nil
	}
	channels, err := s.repo.ListChannels(context.Background())
	if err != nil {
		return nil
	}
	var byID string
	var byName []string
	for _, ch := range channels {
		if ch.ID == term {
			byID = ch.BaseURL
		}
		if ch.ManualEnabled && ch.ChannelName == term && ch.BaseURL != "" {
			byName = append(byName, ch.BaseURL)
		}
	}
	if byID != "" {
		return []string{byID}
	}
	return byName
}

// DecideRouteScope 查能力路由表：model + 请求渠道上下文（含聚合模型的候选 Key 集合）。
// scope.IDs 为实际命中的渠道 key 集合（单 key 或 __channel_candidates），
// scope.BaseURLs 为渠道组地址（__current_channel_base_url / 按 id 查表）。
// 选择策略统一走 types.SelectCapabilityRoutes：native（及历史 error 降级）优先短路，
// proxy 取首个候选。未命中返回 nil（透传）。
// SQLite repo 优先，store JSON 兜底。
func (s *Service) DecideRouteScope(model string, scope types.ChannelRequestScope) (*types.CapabilityRoute, error) {
	if s.repo != nil {
		routes, err := s.repo.ListCapabilityRoutes(context.Background())
		if err == nil {
			selected := types.SelectCapabilityRoutes(routes, capabilityName, model, scope)
			if len(selected) == 0 {
				return nil, nil
			}
			return selected[0], nil
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
	selected := types.SelectCapabilityRoutes(routes, capabilityName, model, scope)
	if len(selected) == 0 {
		return nil, nil
	}
	return selected[0], nil
}

// visionError 构造统一的视觉能力错误（OpenAI error.type = vision_capability_error）。
func visionError(msg string) *modelgateway.GatewayError {
	return &modelgateway.GatewayError{Type: "vision_capability_error", Msg: msg}
}
