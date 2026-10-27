package toolcallrepair

import (
	"context"
	"errors"
	"log/slog"

	"loadout/core/db"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// Service 工具调用序列补全适配器：查能力路由，命中 proxy 时改写请求体。
type Service struct {
	st   *store.Store
	lg   *slog.Logger
	repo *db.Repository // SQLite 能力路由数据源（装配后注入；nil 时回退 JSON）
}

func NewService(st *store.Store, lg *slog.Logger) *Service {
	return &Service{st: st, lg: lg}
}

// SetRepository 注入 SQLite 仓储（由装配层在 db 就绪后调用；测试可省略）。
func (s *Service) SetRepository(repo *db.Repository) { s.repo = repo }

// decideRoutes 查能力路由：model + 请求渠道上下文的 toolcall_repair 路由。
// 未命中返回空；native 命中即短路（透传）；多条 proxy 返回全部。
// 读表/解析失败 fail-open：记日志并按未命中处理，绝不因为读表失败改写或拒绝请求。
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
		s.lg.Warn("toolcall-repair: 从 SQLite 读能力路由表失败，回退 JSON", "err", err)
	}
	var routes []types.CapabilityRoute
	if err := s.st.Read(types.FileCapabilityRoutes, &routes); err != nil {
		if errors.Is(err, store.ErrNotExist) {
			return nil, nil
		}
		s.lg.Warn("toolcall-repair: 读取能力路由表失败，按透传处理", "err", err)
		return nil, nil
	}
	return types.SelectCapabilityRoutesEx(routes, capabilityName, pipe.Request.Model, virtualModel, scope), nil
}

// requestChannelBaseURLs 反查渠道 base_url（term = 渠道 key id 或渠道名）。
// before-attempt 阶段 __current_channel 已写入，scope 的 ID/BaseURL 本身够用；
// 这里主要服务聚合候选集合与 hint 兜底，保证 channel_base_urls 约束不被漏判。
// 实现与 field-filter / force-stream 一致。
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
			out := make([]types.Channel, 0, len(channels))
			for _, ch := range channels {
				out = append(out, types.Channel{
					ID: ch.ID, Name: ch.Name, ChannelName: ch.ChannelName,
					BaseURL: ch.BaseURL, Enabled: ch.ManualEnabled,
				})
			}
			if urls := find(out); len(urls) > 0 {
				return urls
			}
		}
	}
	var channels []types.Channel
	if err := s.st.Read(types.FileChannels, &channels); err == nil {
		return find(channels)
	}
	return nil
}
