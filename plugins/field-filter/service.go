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

// decideRoute 查能力路由：model + 请求渠道上下文的 field_filter 路由。
// 未命中返回 nil；native/error 返回非 nil route（由调用方按 Route != proxy 排除，
// 均视为原样透传）。读表/解析失败 fail-open：记录日志并返回 nil，不拒绝请求。
// 逻辑照 sensitive-filter 的 DecideRouteScope（含聚合模型渠道上下文）。
func (s *Service) decideRoute(pipe *modelgateway.ProxyPipeline) (*types.CapabilityRoute, error) {
	if pipe == nil || pipe.Request == nil {
		return nil, nil
	}
	scope := types.ChannelScopeFromMetadata(pipe.Metadata, s.requestChannelBaseURL)
	if s.repo != nil {
		routes, err := s.repo.ListCapabilityRoutes(context.Background())
		if err == nil {
			for i := range routes {
				if routes[i].Capability == capabilityName &&
					types.MatchModels(routes[i].Models, pipe.Request.Model) &&
					types.MatchChannelScopeEx(routes[i].ChannelIDs, routes[i].ChannelBaseURLs, scope) {
					return &routes[i], nil
				}
			}
			return nil, nil
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
	for i := range routes {
		if routes[i].Capability == capabilityName &&
			types.MatchModels(routes[i].Models, pipe.Request.Model) &&
			types.MatchChannelScopeEx(routes[i].ChannelIDs, routes[i].ChannelBaseURLs, scope) {
			return &routes[i], nil
		}
	}
	s.lg.Debug("field-filter: 未命中 field_filter 路由（透传）",
		"model", pipe.Request.Model, "routes", len(routes),
		"scope_ids", scope.IDs, "scope_base_urls", scope.BaseURLs)
	return nil, nil
}

// requestChannelBaseURL 取请求渠道的 base_url（用于渠道级匹配）。
// repo 为 nil（无 db 环境/测试）时从 store 渠道表兜底读取，保证 channel_base_urls
// 约束在 JSON 模式下同样生效。
func (s *Service) requestChannelBaseURL(channelID string) string {
	if channelID == "" {
		return ""
	}
	if s.repo != nil {
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
	var channels []types.Channel
	if err := s.st.Read(types.FileChannels, &channels); err != nil {
		return ""
	}
	for _, ch := range channels {
		if ch.ID == channelID {
			return ch.BaseURL
		}
	}
	return ""
}

// routeMetaKey 命中路由在 pipe.Metadata 的暂存 key（before hook 写入，after hook 复用，
// 避免同一请求 before/after 各查一次路由表）。
const routeMetaKey = "__field_filter_route"

// HandleProxyBeforeUpstream 请求方向 hook：转发上游前按配置剔除/保留请求体字段、
// 剔除请求头。仅处理合法 JSON body；未命中路由/native/error/无 FieldRules → 原样透传。
// 路由查询结果写入 pipe.Metadata（含未命中 nil），供响应方向 hook 复用。
func (s *Service) HandleProxyBeforeUpstream(payload any) (any, error) {
	pipe, ok := payload.(*modelgateway.ProxyPipeline)
	if !ok || pipe == nil || pipe.Request == nil {
		return payload, nil
	}
	route, err := s.decideRoute(pipe)
	if err != nil || route == nil || route.Route != types.RouteProxy || route.FieldRules == nil {
		pipe.Metadata[routeMetaKey] = route // 可能为 nil：after hook 仅做透传
		return payload, nil
	}
	r := route.FieldRules
	// 请求头剔除（大小写不敏感；替代 proxy.go 写死的 stripAltAuth，按渠道配置即可）。
	if len(r.RequestHeaderStrip) > 0 && pipe.Request.Header != nil {
		for _, h := range r.RequestHeaderStrip {
			pipe.Request.Header.Del(h) // http.Header.Del 大小写不敏感
		}
	}
	hasBodyRules := len(r.RequestKeep) > 0 || len(r.RequestStrip) > 0
	if hasBodyRules && len(pipe.Request.Body) > 0 {
		before := pipe.Request.Body
		pipe.Request.Body = applyFieldRules(before, r.RequestKeep, r.RequestStrip)
		if string(pipe.Request.Body) == string(before) {
			s.warnRulesMiss(pipe.Request.Model, "request", r.RequestKeep, r.RequestStrip)
		} else {
			s.lg.Info("field-filter: 请求体字段过滤", "model", pipe.Request.Model,
				"keep", r.RequestKeep, "strip", r.RequestStrip)
		}
	}
	if len(r.RequestHeaderStrip) > 0 {
		s.lg.Info("field-filter: 请求头剔除", "model", pipe.Request.Model, "headers", r.RequestHeaderStrip)
	}
	pipe.Metadata[routeMetaKey] = route
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
// 路由复用 before hook 暂存于 Metadata 的结果（__field_filter_route），
// 不重复查表；key 不存在时（hook 单独触发场景）回退查表。
func (s *Service) HandleProxyAfterUpstream(payload any) (any, error) {
	after, ok := payload.(*modelgateway.AfterUpstreamPayload)
	if !ok || after == nil || after.Pipe == nil || after.Response == nil {
		return payload, nil
	}
	var route *types.CapabilityRoute
	if r, ok := after.Pipe.Metadata[routeMetaKey]; ok {
		route, _ = r.(*types.CapabilityRoute)
	} else {
		var err error
		route, err = s.decideRoute(after.Pipe)
		if err != nil {
			return payload, nil
		}
	}
	if route == nil || route.Route != types.RouteProxy || route.FieldRules == nil {
		return payload, nil
	}
	r := route.FieldRules
	if len(r.ResponseHeaderStrip) > 0 && after.Response.Header != nil {
		for _, h := range r.ResponseHeaderStrip {
			after.Response.Header.Del(h) // http.Header.Del 大小写不敏感
		}
	}
	if len(after.Response.Body) > 0 && (len(r.ResponseKeep) > 0 || len(r.ResponseStrip) > 0) {
		before := after.Response.Body
		after.Response.Body = applyFieldRules(before, r.ResponseKeep, r.ResponseStrip)
		if string(after.Response.Body) == string(before) {
			s.warnRulesMiss(after.Pipe.Request.Model, "response", r.ResponseKeep, r.ResponseStrip)
		} else {
			s.lg.Info("field-filter: 响应体字段过滤", "model", after.Pipe.Request.Model,
				"keep", r.ResponseKeep, "strip", r.ResponseStrip)
		}
	}
	return after, nil
}
