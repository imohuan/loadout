package aggregate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"loadout/core/db"
	"loadout/core/plugin"
	"loadout/core/store"
	"loadout/plugins/contracts"
	modelgateway "loadout/plugins/model-gateway"
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

// HandleBeforeUpstream 拦截 chat:before-upstream 事件，检测聚合模型并改写为第一个可用的真实模型。
func (s *Service) HandleBeforeUpstream(payload any) (any, error) {
	pipe, ok := payload.(*modelgateway.Pipeline)
	if !ok || pipe == nil || pipe.Request == nil {
		return payload, nil
	}

	model := pipe.Request.Model
	s.lg.Debug("[aggregate] HandleBeforeUpstream 开始", "model", model)

	agg, err := s.findAggregate(model)
	if err != nil {
		s.lg.Error("[aggregate] 查询聚合模型失败", "model", model, "err", err)
		return nil, &modelgateway.GatewayError{
			Status: http.StatusInternalServerError,
			Type:   "internal_error",
			Msg:    err.Error(),
		}
	}
	if agg == nil {
		// 非聚合模型，交还给下游
		s.lg.Debug("[aggregate] 非聚合模型，跳过", "model", model)
		return payload, nil
	}

	s.lg.Info("[aggregate] 检测到聚合模型", "model", model, "targets", len(agg.Targets))

	if len(agg.Targets) == 0 {
		s.lg.Warn("[aggregate] 聚合模型无目标", "model", model)
		return nil, &modelgateway.GatewayError{
			Status: http.StatusBadGateway,
			Type:   "no_targets",
			Msg:    fmt.Sprintf("聚合模型 %q 无可用目标", model),
		}
	}

	// 初始化 Metadata
	if pipe.Metadata == nil {
		pipe.Metadata = make(map[string]any)
	}

	// 检查是否已处理（避免递归）
	if pipe.Metadata["__aggregate_processed"] != nil {
		s.lg.Debug("[aggregate] 已处理过，跳过")
		return payload, nil
	}
	pipe.Metadata["__aggregate_processed"] = true
	pipe.Metadata["__virtual_model"] = model
	pipe.Metadata["__aggregate_targets"] = agg.Targets
	pipe.Metadata["__failed_targets"] = []string{}
	pipe.Metadata["__retry_count"] = 0

	// 选择第一个可用模型（跳过已禁用的）
	target := s.selectAvailableTarget(agg.Targets, nil)
	if target == nil {
		s.lg.Error("[aggregate] 无可用目标")
		return nil, &modelgateway.GatewayError{
			Status: http.StatusServiceUnavailable,
			Type:   "no_available_model",
			Msg:    fmt.Sprintf("聚合模型 %q 的所有目标当前不可用", model),
		}
	}

	s.lg.Info("[aggregate] 选择目标模型", "virtual", model, "selected", target.Model, "channel", target.ChannelID)

	// 改写为真实模型，让事件链继续（vision 等插件会自动处理）
	pipe.Request.Model = target.Model
	pipe.Metadata["__current_channel"] = target.ChannelID

	s.lg.Debug("[aggregate] HandleBeforeUpstream 完成", "new_model", target.Model)
	return pipe, nil
}

// HandleUpstreamFailed 处理上游失败，分析原因并切换到下一个模型
func (s *Service) HandleUpstreamFailed(payload any) (any, error) {
	s.lg.Debug("[aggregate] HandleUpstreamFailed 开始")

	failure, ok := payload.(*modelgateway.FailurePayload)
	if !ok || failure == nil || failure.Pipe == nil {
		s.lg.Debug("[aggregate] payload 类型不匹配，跳过")
		return payload, nil
	}

	pipe := failure.Pipe

	// 检查是否是聚合模型的失败
	virtualModel, ok := pipe.Metadata["__virtual_model"].(string)
	if !ok || virtualModel == "" {
		s.lg.Debug("[aggregate] 非聚合模型失败，跳过")
		return payload, nil // 不是聚合模型，交给其他插件
	}

	s.lg.Info("[aggregate] 检测到聚合模型失败", "virtual", virtualModel, "failed_model", failure.Model, "channel", failure.ChannelID, "status", failure.StatusCode, "error", failure.ErrorBody)

	// 分析失败策略
	strategy := s.analyzeFailure(failure)
	s.lg.Info("[aggregate] 失败策略", "model", failure.Model, "action", strategy.Action, "reason", strategy.Reason, "cooldown", strategy.CooldownTime)

	// 更新健康状态；生产装配由 model-health 统一持久化，旧 map 仅服务兼容单测。
	modelKey := fmt.Sprintf("%s@%s", failure.Model, failure.ChannelID)
	if s.health != nil {
		errorMessage := ""
		if failure.Error != nil {
			errorMessage = failure.Error.Error()
		}
		_, healthErr := s.health.RecordFailure(context.Background(), contracts.RouteFailure{RequestID: failure.Pipe.RequestID, Model: failure.Model, ChannelID: failure.ChannelID, StatusCode: failure.StatusCode, ErrorBody: failure.ErrorBody, Error: errorMessage})
		if healthErr != nil {
			s.lg.Warn("model health failure update failed", "err", healthErr)
		}
	} else {
		s.updateHealth(modelKey, strategy, failure.Error)
	}

	// 记录失败目标
	failedTargets, _ := pipe.Metadata["__failed_targets"].([]string)
	failedTargets = append(failedTargets, modelKey)
	pipe.Metadata["__failed_targets"] = failedTargets
	s.lg.Debug("[aggregate] 已失败目标", "count", len(failedTargets), "list", failedTargets)

	// 选择下一个可用模型
	targets, _ := pipe.Metadata["__aggregate_targets"].([]types.AggregateTarget)
	nextTarget := s.selectAvailableTarget(targets, failedTargets)

	if nextTarget == nil {
		s.lg.Error("[aggregate] 所有目标模型均失败", "virtual", virtualModel, "failed_count", len(failedTargets))
		return nil, fmt.Errorf("聚合模型 %q 的所有目标均失败", virtualModel)
	}

	s.lg.Info("[aggregate] 切换到下一个模型", "virtual", virtualModel, "next_model", nextTarget.Model, "channel", nextTarget.ChannelID)

	// 改写并返回重试载荷
	pipe.Request.Model = nextTarget.Model
	pipe.Metadata["__current_channel"] = nextTarget.ChannelID

	s.lg.Debug("[aggregate] HandleUpstreamFailed 完成，返回重试载荷")
	return &modelgateway.RetryPayload{Pipe: pipe}, nil
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
				value.Targets = append(value.Targets, types.AggregateTarget{Model: target.Model, ChannelID: target.ChannelID})
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
