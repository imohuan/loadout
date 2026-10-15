package aggregate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"loadout/core/db"
	"loadout/core/plugin"
	"loadout/core/store"
	"loadout/plugins/contracts"
	"loadout/plugins/types"
)

// Service 聚合模型服务：拦截聚合模型请求，按优先级轮询多个真实模型。
type Service struct {
	st        *store.Store
	lg        *slog.Logger
	ctx       plugin.Context
	healthMu  sync.RWMutex
	healthMap map[string]*types.ModelHealth // key: "{model}@{channel_id}"
	routing   *db.Repository
	health    contracts.ModelHealth
}

// NewService 创建聚合模型服务。
func NewService(st *store.Store, lg *slog.Logger, ctx plugin.Context) *Service {
	svc := &Service{
		st:        st,
		lg:        lg,
		ctx:       ctx,
		healthMap: make(map[string]*types.ModelHealth),
	}
	svc.loadHealth()
	return svc
}

func (s *Service) SetRoutingServices(database *sql.DB, health contracts.ModelHealth) {
	if database != nil {
		s.routing, _ = db.NewRepository(database)
	}
	s.health = health
}

// findAggregate 查找聚合模型配置，未命中返回 nil。
func (s *Service) findAggregate(model string) (*types.AggregateModel, error) {
	if s.routing != nil {
		aggregates, err := s.routing.ListAggregates(context.Background())
		if err != nil {
			return nil, err
		}
		for _, aggregate := range aggregates {
			if aggregate.Name != model || !aggregate.Enabled {
				continue
			}
			value := &types.AggregateModel{Name: aggregate.Name, Targets: make([]types.AggregateTarget, 0, len(aggregate.Targets))}
			for _, target := range aggregate.Targets {
				value.Targets = append(value.Targets, types.AggregateTarget{
					Model:          target.Model,
					ChannelID:      target.ChannelID,
					ChannelIDs:     target.ChannelIDs,
					ChannelBaseURL: target.ChannelBaseURL,
				})
			}
			return value, nil
		}
		return nil, nil
	}
	var aggs []types.AggregateModel
	if err := s.st.Read(types.FileAggregates, &aggs); err != nil {
		if errors.Is(err, store.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("aggregate: 读取聚合模型表失败: %w", err)
	}
	for i := range aggs {
		if aggs[i].Name == model {
			return &aggs[i], nil
		}
	}
	return nil, nil
}
