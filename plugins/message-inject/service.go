package messageinject

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"loadout/core/db"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// capabilityName 能力路由表中消息注入能力的固定名称。
const capabilityName = "message_inject"

// Service 消息注入适配器：查能力路由，往请求 messages 注入自定义内容。
type Service struct {
	st   *store.Store
	lg   *slog.Logger
	repo *db.Repository // SQLite 能力路由数据源（装配后注入；nil 时回退 JSON）
}

// NewService 创建消息注入适配器。
func NewService(st *store.Store, lg *slog.Logger) *Service {
	return &Service{st: st, lg: lg}
}

// SetRepository 注入 SQLite 仓储（由装配层在 db 就绪后调用；测试可省略）。
func (s *Service) SetRepository(repo *db.Repository) { s.repo = repo }

// decideRoutes 查能力路由：model + 请求渠道上下文的 message_inject 路由。
// 未命中返回空列表；命中 native（及历史 error 降级）立即返回该项，跳过后续匹配；
// 若有多个匹配的 proxy 路由，返回全部匹配项（叠加规则）。
// 读表/解析失败 fail-open：记录日志并返回空，不拒绝请求。
// 选择策略统一走 types.SelectCapabilityRoutes（与 sensitive-filter / field-filter 一致）。
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
		s.lg.Warn("message-inject: 从 SQLite 读能力路由表失败，回退 JSON", "err", err)
	}

	var routes []types.CapabilityRoute
	if err := s.st.Read(types.FileCapabilityRoutes, &routes); err != nil {
		if errors.Is(err, store.ErrNotExist) {
			return nil, nil
		}
		s.lg.Warn("message-inject: 读取能力路由表失败，按透传处理", "err", err)
		return nil, nil
	}
	return types.SelectCapabilityRoutesEx(routes, capabilityName, pipe.Request.Model, virtualModel, scope), nil
}

// requestChannelBaseURLs 反查渠道 base_url 列表（渠道级路由约束用，与 sensitive-filter 同实现）。
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

// HandleProxyBeforeUpstream 每次渠道尝试安检 hook（proxy:before-attempt）：往请求
// messages 注入自定义内容。仅处理合法 JSON body（非 JSON 原样透传）；未命中路由、
// 非 proxy 路由（native / 历史 error 降级）原样透传。
func (s *Service) HandleProxyBeforeUpstream(payload any) (any, error) {
	pipe, ok := payload.(*modelgateway.ProxyPipeline)
	if !ok || pipe == nil || pipe.Request == nil || len(pipe.Request.Body) == 0 {
		return payload, nil
	}
	// 子请求（vision_v2 视觉识别/续流走网关通道）：跳过——不干扰识别/续流请求体。
	if pipe.Metadata != nil {
		if v, _ := pipe.Metadata["__sub_request_skip_security"].(bool); v {
			return payload, nil
		}
	}
	if !json.Valid(pipe.Request.Body) {
		return payload, nil
	}

	routes, err := s.decideRoutes(pipe, types.VirtualModelFromMetadata(pipe.Metadata))
	if err != nil || len(routes) == 0 {
		return payload, nil
	}
	// native / 历史 error 降级：原样透传。
	if routes[0].Route != types.RouteProxy {
		s.lg.Debug("message-inject: 非 proxy 路由，原样透传", "model", pipe.Request.Model, "route", routes[0].Route)
		return payload, nil
	}

	// 合并所有 proxy 路由的注入配置（按路由顺序叠加）。
	var all []types.MessageInjection
	for _, route := range routes {
		all = append(all, route.Injections...)
	}
	// 过滤空配置（role 或 content 为空的行跳过，配置防御）。
	active := all[:0:0]
	for _, inj := range all {
		if inj.Content != "" {
			active = append(active, inj)
		}
	}
	if len(active) == 0 {
		return payload, nil
	}

	// 每次渠道尝试（proxy:before-attempt，聚合 failover 会多次触发）都应基于【原始请求体】
	// 注入，而不是基于上一次尝试注入后的 body——否则注入内容会被反复叠加（"接力棒"效应）。
	// 首次尝试时把原始 body 快照存进 pipe.Metadata（跨 attempt 共享），后续尝试从快照重注入。
	if pipe.Metadata == nil {
		pipe.Metadata = map[string]any{}
	}
	const origBodyKey = "__message_inject_orig_body"
	origBody, ok := pipe.Metadata[origBodyKey].(string)
	if !ok {
		origBody = string(pipe.Request.Body)
		pipe.Metadata[origBodyKey] = origBody
	}
	before := origBody
	replaced, err := applyInjections(before, active)
	if err != nil {
		// 结构化注入失败：不拒绝请求，记录日志并原样透传（fail-open）。
		s.lg.Warn("message-inject: 注入失败，原样透传", "model", pipe.Request.Model, "err", err)
		return payload, nil
	}
	if replaced == before {
		s.lg.Info("message-inject: 命中路由但未注入（无 messages 且无 input）", "model", pipe.Request.Model, "rules", len(active))
		return payload, nil
	}
	s.lg.Info("message-inject: 注入完成", "model", pipe.Request.Model, "rules", len(active))
	pipe.Request.Body = []byte(replaced)
	return pipe, nil
}

// applyInjections 解析请求体 JSON，按配置顺序往 messages（或 responses 的 input）注入。
// 返回注入后的完整请求体字符串；无法解析或不含 messages/input 时返回原字符串（不报错）。
func applyInjections(body string, injections []types.MessageInjection) (string, error) {
	root := map[string]any{}
	dec := json.NewDecoder(bytes.NewReader([]byte(body)))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		return "", fmt.Errorf("message-inject: 解析请求体失败: %w", err)
	}

	// messages 为 OpenAI/Claude 对话格式，input 为 responses 格式（与 sensitive-filter 一致）。
	changed := false
	for _, key := range []string{"messages", "input"} {
		raw, ok := root[key]
		if !ok {
			continue
		}
		arr, ok := raw.([]any)
		if !ok {
			continue
		}
		// 原始第一条消息的 map 引用（注入前捕获，供 prepend_first/append_first 使用）。
		var firstMsg map[string]any
		if len(arr) > 0 {
			if m, ok := arr[0].(map[string]any); ok {
				firstMsg = m
			}
		}
		for _, inj := range injections {
			applied := applyOne(&arr, &firstMsg, inj)
			if applied {
				changed = true
			}
		}
		root[key] = arr
	}
	if !changed {
		return body, nil
	}
	out, err := json.Marshal(root)
	if err != nil {
		return "", fmt.Errorf("message-inject: 重序列化请求体失败: %w", err)
	}
	return string(out), nil
}

// applyOne 应用单条注入配置到 messages 数组。arr 为可变切片（新增项直接 append/插入），
// firstMsg 指向原始第一条消息（注入前捕获，prepend_first/append_first 修改其 content）。
// 返回是否发生了实际修改。
func applyOne(arr *[]any, firstMsg *map[string]any, inj types.MessageInjection) bool {
	msg := map[string]any{
		"role":    inj.Role,
		"content": inj.Content,
	}
	switch inj.Position {
	case types.InjectPrepend:
		*arr = append([]any{msg}, *arr...)
		return true
	case types.InjectAppend:
		*arr = append(*arr, msg)
		return true
	case types.InjectPrependFirst, types.InjectAppendFirst:
		if *firstMsg == nil {
			// 没有原始第一条：退化为新增一条消息作为第一项。
			*arr = append([]any{msg}, *arr...)
			return true
		}
		modified := mergeFirstContent(*firstMsg, inj.Content, inj.Position == types.InjectPrependFirst)
		return modified
	}
	return false
}

// mergeFirstContent 把文本拼到原始第一条消息 content 的开头（prepend=true）或结尾。
// content 为字符串或分段数组；返回是否发生修改。role 不在此处修改（仅拼内容）。
func mergeFirstContent(first map[string]any, text string, prepend bool) bool {
	content, ok := first["content"]
	if !ok || content == nil {
		// 无 content 字段：直接设置。
		first["content"] = text
		return true
	}
	// 纯字符串 content。
	if s, ok := content.(string); ok {
		if prepend {
			first["content"] = text + s
		} else {
			first["content"] = s + text
		}
		return true
	}
	// 分段数组 content：文本块。
	if parts, ok := content.([]any); ok {
		textPart := map[string]any{"type": "text", "text": text}
		if prepend {
			first["content"] = append([]any{textPart}, parts...)
		} else {
			first["content"] = append(parts, textPart)
		}
		return true
	}
	return false
}
