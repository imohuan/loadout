// Package forcestream 实现 Loadout 的「强制流式」能力适配器（plugins/force-stream）。
//
// 强制流式订阅 model-gateway 的 proxy:before-attempt waterfall 事件，在请求转发上游前：
//   - 若当前请求命中能力路由表（capability="force_stream"，route=proxy），且
//   - 客户端发的是非流式请求（body 未显式 stream:true，pipe.Request.Stream==false），且
//   - 请求 path 是 chat/completions（OpenAI SSE 协议），
//
// 则把请求体里的 stream 字段改写为 true（上游走流式），并在 pipe.Metadata 打
// modelgateway.MetadataForceStream 标记，交给 model-gateway 核心做「缓冲整段 SSE →
// 还原成一份完整非流式 JSON」处理（见 model-gateway/proxy.go readBufferedSSE）。
//
// 用途：某些渠道/平台只接受（或只允许）流式请求，但客户端想要非流式响应。本插件让这类
// 渠道+模型组合对外表现为「支持非流式」：客户端照常发 stream:false，网关内部转流式请求、
// 缓冲后整包非流式返回，客户端无感知。
//
// 不命中 / native 路由 / 客户端本来就是流式 / 非 chat/completions path → 原样透传，绝不误伤。
package forcestream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"loadout/core/db"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// capabilityName 能力路由表中强制流式能力的固定名称。
const capabilityName = "force_stream"

// supportedPath force_stream 只对 OpenAI chat/completions SSE 协议生效
// （readBufferedSSE 只按该协议的 chunk 结构拼装）。
const supportedPath = "chat/completions"

// Service 强制流式适配器：查能力路由，命中且为非流式 chat/completions 时改 body stream:true
// 并打标记，交由核心缓冲拼装。
type Service struct {
	st   *store.Store
	lg   *slog.Logger
	repo *db.Repository // SQLite 能力路由数据源（装配后注入；nil 时回退 JSON）
}

// NewService 创建强制流式适配器。
func NewService(st *store.Store, lg *slog.Logger) *Service {
	return &Service{st: st, lg: lg}
}

// SetRepository 注入 SQLite 仓储（由装配层在 db 就绪后调用；测试可省略）。
func (s *Service) SetRepository(repo *db.Repository) { s.repo = repo }

// decideRoutes 查能力路由：model + 请求渠道上下文的 force_stream 路由。
// 未命中返回空；命中 native（及历史 error 降级）立即返回该项，跳过后续匹配；
// 多个 proxy 路由返回全部（叠加）。
// 读表/解析失败 fail-open：记录日志并返回空，不拒绝请求。
// 选择策略统一走 types.SelectCapabilityRoutesEx（与 sensitive-filter / field-filter 一致，
// 支持聚合模型的 __channel_candidates / 渠道级 base_url 命中）。
func (s *Service) decideRoutes(pipe *modelgateway.ProxyPipeline, virtualModel string) ([]*types.CapabilityRoute, error) {
	if pipe == nil || pipe.Request == nil {
		return nil, nil
	}
	scope := types.ChannelScopeFromMetadata(pipe.Metadata, s.requestChannelBaseURLs)
	if s.repo != nil {
		routes, err := s.repo.ListCapabilityRoutes(context.Background())
		if err == nil {
			return types.SelectCapabilityRoutesEx(routes, capabilityName, pipe.Request.Model, virtualModel, scope), nil
		}
		s.lg.Warn("force-stream: 从 SQLite 读能力路由表失败，回退 JSON", "err", err)
	}
	var routes []types.CapabilityRoute
	if err := s.st.Read(types.FileCapabilityRoutes, &routes); err != nil {
		if errors.Is(err, store.ErrNotExist) {
			return nil, nil
		}
		s.lg.Warn("force-stream: 读取能力路由表失败，按透传处理", "err", err)
		return nil, nil
	}
	return types.SelectCapabilityRoutesEx(routes, capabilityName, pipe.Request.Model, virtualModel, scope), nil
}

// requestChannelBaseURLs 反查渠道 base_url 列表：term 可为渠道 key id（精确匹配，返回该 key
// 所在渠道组的 base_url）或渠道名 ChannelName（返回组内全部启用 Key 共享的 base_url，去重）。
// 无渠道或查不到返回空 slice。repo 为 nil（无 db 环境/测试）时从 store 渠道表兜底读取，
// 保证 channel_base_urls 约束在 JSON 模式下同样生效。实现与 field-filter 一致。
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
		return find(channels)
	}
	return nil
}

// toTypesChannels 把 db.Channel 转为 types.Channel（store 兜底查询复用 find）。
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

// origBodyMetaKey 记录「客户端原始 body 快照」的 metadata 键。跨渠道尝试（failover）时
// 从快照重改写/还原，避免基于已被上一渠道改过的 body 再操作（message-inject 同款思路）。
const origBodyMetaKey = "__force_stream_orig_body"

// HandleProxyBeforeUpstream 每次渠道尝试安检 hook（proxy:before-attempt）：
// 对**当前渠道**按能力路由重新匹配；命中 proxy 路由且为非流式 chat/completions 时，
// 从原始 body 快照改 stream:true + 打标记，交由核心缓冲拼装；未命中则还原原始 body、
// 清标记（该渠道按原生非流式透传，不强加 force_stream）。
//
// 每次渠道尝试都重跑 decideRoutes（对标 sensitive-filter 的逐渠道重匹配）：failover 切到
// 未配 force_stream 的渠道时，该渠道不被强制转流式、按它原生支持的方式透传，不误伤。
// 未命中/native/本来流式/其它 path → 原样透传，绝不拒绝请求。
func (s *Service) HandleProxyBeforeUpstream(payload any) (any, error) {
	pipe, ok := payload.(*modelgateway.ProxyPipeline)
	if !ok || pipe == nil || pipe.Request == nil || len(pipe.Request.Body) == 0 {
		return payload, nil
	}
	// 子请求（vision_v2 视觉识别/续流走网关通道）：跳过安检——识别 body 含数 MB base64，
	// 且子请求本就按自身语义走，不应被强制改流式。
	if pipe.Metadata != nil {
		if v, _ := pipe.Metadata["__sub_request_skip_security"].(bool); v {
			return payload, nil
		}
	}
	// 客户端本来就是流式请求：本能力只服务于「非流式客户端 + 只能流式上游」的场景，
	// 流式请求保持现状（SSE 透传），无需打标记。
	if pipe.Request.Stream {
		return payload, nil
	}
	// 只对 chat/completions 生效（readBufferedSSE 仅支持该协议），其它 path 原样透传。
	if pipe.Request.Path != supportedPath {
		return payload, nil
	}
	if !json.Valid(pipe.Request.Body) {
		return payload, nil
	}
	if pipe.Metadata == nil {
		pipe.Metadata = map[string]any{}
	}

	// 首次尝试时快照客户端原始 body；后续渠道尝试（failover，同 pipe）从快照重改写/还原，
	// 保证不基于已被上一渠道改过的 body 操作、也不叠加。快照一旦建立即保持不变。
	orig, ok := pipe.Metadata[origBodyMetaKey].(string)
	if !ok || orig == "" {
		orig = string(pipe.Request.Body)
		pipe.Metadata[origBodyMetaKey] = orig
	}
	channelID, _ := pipe.Metadata["__current_channel"].(string)

	routes, err := s.decideRoutes(pipe, types.VirtualModelFromMetadata(pipe.Metadata))
	// 命中 force_stream proxy 路由 → 从快照转流式 + 打标记。
	if err == nil && len(routes) > 0 && routes[0].Route == types.RouteProxy {
		rewritten, rerr := setStreamTrue([]byte(orig))
		if rerr != nil {
			// 改写失败（理论不会，json.Valid 已过）：fail-open 原样透传，不拒绝请求。
			s.lg.Warn("force-stream: body 改写失败，按透传处理", "model", pipe.Request.Model, "channel_id", channelID, "err", rerr)
			return payload, nil
		}
		pipe.Request.Body = rewritten
		pipe.Metadata[modelgateway.MetadataForceStream] = true
		s.lg.Info("force-stream: 命中路由，上游转流式 + 打标记（缓冲后整包非流式返回）",
			"model", pipe.Request.Model, "channel_id", channelID, "path", pipe.Request.Path)
		return pipe, nil
	}

	// 未命中 / native（历史 error 降级）等：**还原**客户端原始 body 并清标记——
	// 若上一渠道已转流式，切到未配本能力的渠道时应复原为原生非流式透传，不强加缓冲。
	if pipe.Request.Body != nil && string(pipe.Request.Body) != orig {
		pipe.Request.Body = []byte(orig)
		s.lg.Debug("force-stream: 当前渠道未命中，还原原始 body", "model", pipe.Request.Model, "channel_id", channelID, "path", pipe.Request.Path)
	}
	delete(pipe.Metadata, modelgateway.MetadataForceStream)
	return pipe, nil
}

// setStreamTrue 解析请求体 JSON，把顶层 stream 字段设为 true（原已是 true 或不存在都设为 true）。
// body 非 JSON 对象时报错（调用方已先 json.Valid 校验，正常不会走到）。返回重序列化后的字节。
// 会重排字段顺序（与 sensitive-filter/message-inject 的全量 marshal 语义一致），上游为 JSON API，
// 无字节级保序需求。
func setStreamTrue(body []byte) ([]byte, error) {
	root := map[string]any{}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		return nil, err
	}
	if b, ok := root["stream"].(bool); ok && b {
		// 已是 true：仍返回（幂等），不额外处理。
		return body, nil
	}
	root["stream"] = true
	out, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}
	return out, nil
}
