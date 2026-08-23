package fieldfilter

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"loadout/core/db"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// capabilityName 能力路由表中字段过滤能力的固定名称。
const capabilityName = "field_filter"

// Service 字段过滤适配器：查能力路由，按配置对请求体/非流式响应体应用字段规则。
type Service struct {
	st   *store.Store
	lg   *slog.Logger
	repo *db.Repository
}

func NewService(st *store.Store, lg *slog.Logger) *Service {
	return &Service{st: st, lg: lg}
}

func (s *Service) SetRepository(repo *db.Repository) { s.repo = repo }

// decideRoutes 查能力路由：model + 请求渠道上下文的 field_filter 路由。
// 未命中返回空列表；命中 native（及历史 error 降级）立即返回该项，跳过后续匹配；
// 若有多个匹配的 proxy 路由，返回全部匹配项（叠加规则）。
// 读表/解析失败 fail-open：记录日志并返回 nil，不拒绝请求。
// 选择策略统一走 types.SelectCapabilityRoutes（与 sensitive-filter/vision/request-log 一致）。
func (s *Service) decideRoutes(pipe *modelgateway.ProxyPipeline) ([]*types.CapabilityRoute, error) {
	if pipe == nil || pipe.Request == nil {
		// 无请求上下文，返回空列表
		return nil, nil
	}
	scope := types.ChannelScopeFromMetadata(pipe.Metadata, s.requestChannelBaseURLs)
	if s.repo != nil {
		routes, err := s.repo.ListCapabilityRoutes(context.Background())
		if err == nil {
			return types.SelectCapabilityRoutes(routes, capabilityName, pipe.Request.Model, scope), nil
		}
		s.lg.Warn("field-filter: 从 SQLite 读能力路由表失败，回退 JSON", "err", err)
	}

	var routes []types.CapabilityRoute
	if err := s.st.Read(types.FileCapabilityRoutes, &routes); err != nil {
		if errors.Is(err, store.ErrNotExist) {
			return nil, nil
		}
		s.lg.Warn("field-filter: 读取能力路由表失败，按透传处理", "err", err)
		return nil, nil
	}

	selected := types.SelectCapabilityRoutes(routes, capabilityName, pipe.Request.Model, scope)
	s.lg.Debug("field-filter: 未命中 field_filter 路由（透传）",
		"model", pipe.Request.Model, "routes", len(routes),
		"scope_ids", scope.IDs, "scope_base_urls", scope.BaseURLs)

	// 返回匹配结果（native 短路或 proxy 全收集）
	return selected, nil
}

// requestChannelBaseURLs 反查渠道 base_url 列表：term 可为渠道 key id（精确匹配，返回该 key
// 所在渠道组的 base_url）或渠道名 ChannelName（返回组内全部启用 Key 共享的 base_url，去重）。
// 无渠道或查不到返回空 slice。入口阶段（BeforeUpstream）只有 __channel_hint 渠道名时
// 也能反查，供渠道级约束（channel_base_urls）路由匹配。
// repo 为 nil（无 db 环境/测试）时从 store 渠道表兜底读取，保证 channel_base_urls
// 约束在 JSON 模式下同样生效。
func (s *Service) requestChannelBaseURLs(term string) []string {
	if term == "" {
		return nil
	}
	find := func(channels []types.Channel) []string {
		var byID string
		var byName []string
		for _, ch := range channels {
			if ch.ID == term {
				byID = ch.BaseURL
			}
			if ch.Enabled && ch.ChannelName == term && ch.BaseURL != "" {
				byName = append(byName, ch.BaseURL)
			}
		}
		if byID != "" {
			return []string{byID}
		}
		return byName
	}
	if s.repo != nil {
		channels, err := s.repo.ListChannels(context.Background())
		if err == nil {
			if out := find(toTypesChannels(channels)); len(out) > 0 {
				return out
			}
		}
	}
	var channels []types.Channel
	if err := s.st.Read(types.FileChannels, &channels); err == nil {
		if out := find(channels); len(out) > 0 {
			return out
		}
	}
	return nil
}

// toTypesChannels 把 db.Channel 转为 types.Channel（field-filter 的 store 兜底查询复用 find）。
func toTypesChannels(channels []db.Channel) []types.Channel {
	out := make([]types.Channel, 0, len(channels))
	for _, ch := range channels {
		out = append(out, types.Channel{
			ID:          ch.ID,
			Name:        ch.Name,
			ChannelName: ch.ChannelName,
			BaseURL:     ch.BaseURL,
			Enabled:     ch.ManualEnabled,
		})
	}
	return out
}

// routeMetaKey 命中路由在 pipe.Metadata 的暂存 key（before hook 写入，after hook 复用，
// 避免同一请求 before/after 各查一次路由表）。
const routeMetaKey = "__field_filter_routes"

// HandleProxyBeforeUpstream 每次渠道尝试安检 hook（proxy:before-attempt）：
// 转发上游前按配置剔除/保留请求体字段、剔除请求头。仅处理合法 JSON body；
// 未命中路由/native/error/无 FieldRules → 原样透传。
// 路由查询结果写入 pipe.Metadata（含未命中 nil），供响应方向 hook 复用。
func (s *Service) HandleProxyBeforeUpstream(payload any) (any, error) {
	pipe, ok := payload.(*modelgateway.ProxyPipeline)
	if !ok || pipe == nil || pipe.Request == nil {
		return payload, nil
	}

	routes, err := s.decideRoutes(pipe)
	if err != nil {
		pipe.Metadata[routeMetaKey] = nil
		return payload, nil
	}

	// 无匹配路由，直接返回
	if len(routes) == 0 {
		pipe.Metadata[routeMetaKey] = nil
		return payload, nil
	}

	// 处理 native/error 路由：直接透传
	if routes[0].Route != types.RouteProxy {
		pipe.Metadata[routeMetaKey] = routes
		return payload, nil
	}

	// 合并所有 proxy 路由的规则
	var mergedRules types.FieldRules
	seenHeaders := make(map[string]bool)
	seenStrip := make(map[string]bool)
	seenKeep := make(map[string]bool)

	for _, route := range routes {
		if route.FieldRules == nil {
			continue
		}
		// 合并请求头剔除规则（去重）
		for _, h := range route.FieldRules.RequestHeaderStrip {
			if !seenHeaders[h] {
				mergedRules.RequestHeaderStrip = append(mergedRules.RequestHeaderStrip, h)
				seenHeaders[h] = true
			}
		}
		// 合并请求体剔除规则（去重）
		for _, f := range route.FieldRules.RequestStrip {
			if !seenStrip[f] {
				mergedRules.RequestStrip = append(mergedRules.RequestStrip, f)
				seenStrip[f] = true
			}
		}
		// 合并请求体保留规则（去重，多个 keep 取并集）
		for _, f := range route.FieldRules.RequestKeep {
			if !seenKeep[f] {
				mergedRules.RequestKeep = append(mergedRules.RequestKeep, f)
				seenKeep[f] = true
			}
		}
		// 合并响应头剔除规则（去重）
		for _, h := range route.FieldRules.ResponseHeaderStrip {
			if !seenHeaders[h] {
				mergedRules.ResponseHeaderStrip = append(mergedRules.ResponseHeaderStrip, h)
				seenHeaders[h] = true
			}
		}
		// 合并响应体剔除规则（去重）
		for _, f := range route.FieldRules.ResponseStrip {
			if !seenStrip[f] {
				mergedRules.ResponseStrip = append(mergedRules.ResponseStrip, f)
				seenStrip[f] = true
			}
		}
		// 合并响应体保留规则（去重，多个 keep 取并集）
		for _, f := range route.FieldRules.ResponseKeep {
			if !seenKeep[f] {
				mergedRules.ResponseKeep = append(mergedRules.ResponseKeep, f)
				seenKeep[f] = true
			}
		}
	}

	// 应用合并后的规则
	if len(mergedRules.RequestHeaderStrip) > 0 && pipe.Request.Header != nil {
		for _, h := range mergedRules.RequestHeaderStrip {
			pipe.Request.Header.Del(h) // http.Header.Del 大小写不敏感
		}
		s.lg.Info("field-filter: 请求头剔除", "model", pipe.Request.Model, "headers", mergedRules.RequestHeaderStrip)
	}

	hasBodyRules := len(mergedRules.RequestKeep) > 0 || len(mergedRules.RequestStrip) > 0
	if hasBodyRules && len(pipe.Request.Body) > 0 {
		before := pipe.Request.Body
		pipe.Request.Body = applyFieldRules(before, mergedRules.RequestKeep, mergedRules.RequestStrip)
		if string(pipe.Request.Body) == string(before) {
			s.warnRulesMiss(pipe.Request.Model, "request", mergedRules.RequestKeep, mergedRules.RequestStrip)
		} else {
			s.lg.Info("field-filter: 请求体字段过滤", "model", pipe.Request.Model,
				"keep", mergedRules.RequestKeep, "strip", mergedRules.RequestStrip)
		}
	}

	pipe.Metadata[routeMetaKey] = routes
	return pipe, nil
}

// warnRulesMiss 规则配置未命中（keep 含点路径不受支持，或字段在 body 中不存在）时告警。
func (s *Service) warnRulesMiss(model, dir string, keep, strip []string) {
	for _, k := range keep {
		if strings.Contains(k, ".") {
			s.lg.Warn("field-filter: keep 仅支持顶层 key，含点路径的配置按字面 key 处理",
				"model", model, "dir", dir, "key", k)
			return
		}
	}
	s.lg.Warn("field-filter: 字段规则未命中（目标字段不存在或 keep 为字面 key）",
		"model", model, "dir", dir, "keep", keep, "strip", strip)
}

// HandleProxyAfterUpstream 非流式响应方向 hook：返回前按配置剔除/保留响应体
// 字段与响应头。未命中路由/native/error/无 FieldRules → 原样透传，绝不拒绝请求。
// 路由复用 before hook 暂存于 Metadata 的结果（__field_filter_routes），
// 不重复查表；key 不存在时（hook 单独触发场景）回退查表。
func (s *Service) HandleProxyAfterUpstream(payload any) (any, error) {
	after, ok := payload.(*modelgateway.AfterUpstreamPayload)
	if !ok || after == nil || after.Pipe == nil || after.Response == nil || after.Pipe.Metadata == nil {
		return payload, nil
	}

	var routes []*types.CapabilityRoute
	if r, ok := after.Pipe.Metadata[routeMetaKey]; ok {
		routes, _ = r.([]*types.CapabilityRoute)
	}

	// 若无缓存路由，回退查询
	if len(routes) == 0 {
		var err error
		routes, err = s.decideRoutes(after.Pipe)
		if err != nil || len(routes) == 0 {
			return payload, nil
		}
	}

	// 处理 native/error 路由：直接透传
	if routes[0].Route != types.RouteProxy {
		return payload, nil
	}

	// 合并所有 proxy 路由的响应规则
	var mergedRules types.FieldRules
	seenHeaders := make(map[string]bool)
	seenStrip := make(map[string]bool)
	seenKeep := make(map[string]bool)

	for _, route := range routes {
		if route.FieldRules == nil {
			continue
		}
		// 合并响应头剔除规则（去重）
		for _, h := range route.FieldRules.ResponseHeaderStrip {
			if !seenHeaders[h] {
				mergedRules.ResponseHeaderStrip = append(mergedRules.ResponseHeaderStrip, h)
				seenHeaders[h] = true
			}
		}
		// 合并响应体剔除规则（去重）
		for _, f := range route.FieldRules.ResponseStrip {
			if !seenStrip[f] {
				mergedRules.ResponseStrip = append(mergedRules.ResponseStrip, f)
				seenStrip[f] = true
			}
		}
		// 合并响应体保留规则（去重，多个 keep 取并集）
		for _, f := range route.FieldRules.ResponseKeep {
			if !seenKeep[f] {
				mergedRules.ResponseKeep = append(mergedRules.ResponseKeep, f)
				seenKeep[f] = true
			}
		}
	}

	// 应用合并后的响应规则
	if len(mergedRules.ResponseHeaderStrip) > 0 && after.Response.Header != nil {
		for _, h := range mergedRules.ResponseHeaderStrip {
			after.Response.Header.Del(h) // http.Header.Del 大小写不敏感
		}
	}
	if len(after.Response.Body) > 0 && (len(mergedRules.ResponseKeep) > 0 || len(mergedRules.ResponseStrip) > 0) {
		before := after.Response.Body
		after.Response.Body = applyFieldRules(before, mergedRules.ResponseKeep, mergedRules.ResponseStrip)
		if string(after.Response.Body) == string(before) {
			s.warnRulesMiss(after.Pipe.Request.Model, "response", mergedRules.ResponseKeep, mergedRules.ResponseStrip)
		} else {
			s.lg.Info("field-filter: 响应体字段过滤", "model", after.Pipe.Request.Model,
				"keep", mergedRules.ResponseKeep, "strip", mergedRules.ResponseStrip)
		}
	}
	return after, nil
}

