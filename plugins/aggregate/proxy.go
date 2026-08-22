package aggregate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"loadout/plugins/contracts"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// rewriteBodyModel 修改请求体里的 model 字段（其他字段原样保留）。
// 用 UseNumber 解析，避免 map[string]any 把大整数转 float64 丢精度；
// json.Number 序列化时原样输出，key 顺序按 map 序列化（不保证原始顺序，
// 但字段值语义不变）。
func rewriteBodyModel(body []byte, model string) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	m["model"] = model
	return json.Marshal(m)
}

// HandleProxyBeforeUpstream 透明代理输入 hook：检测聚合模型并改写为第一个可用真实模型。
// 与旧 chat 版差异：改的是请求体里的 model 字段（透明代理不做结构化解析）。
func (s *Service) HandleProxyBeforeUpstream(payload any) (any, error) {
	pipe, ok := payload.(*modelgateway.ProxyPipeline)
	if !ok || pipe == nil || pipe.Request == nil {
		return payload, nil
	}

	model := pipe.Request.Model
	s.lg.Debug("[aggregate] HandleProxyBeforeUpstream 开始", "model", model)

	agg, err := s.findAggregate(model)
	if err != nil {
		s.lg.Error("[aggregate] 查询聚合模型失败", "model", model, "err", err)
		return nil, &modelgateway.GatewayError{Status: http.StatusInternalServerError, Type: "internal_error", Msg: err.Error()}
	}
	if agg == nil {
		s.lg.Debug("[aggregate] 非聚合模型，跳过", "model", model)
		return payload, nil
	}
	s.lg.Info("[aggregate] 检测到聚合模型", "model", model, "targets", len(agg.Targets))

	if len(agg.Targets) == 0 {
		s.lg.Warn("[aggregate] 聚合模型无目标", "model", model)
		return nil, &modelgateway.GatewayError{Status: http.StatusBadGateway, Type: "no_targets", Msg: fmt.Sprintf("聚合模型 %q 无可用目标", model)}
	}

	if pipe.Metadata == nil {
		pipe.Metadata = make(map[string]any)
	}
	if pipe.Metadata["__aggregate_processed"] != nil {
		s.lg.Debug("[aggregate] 已处理过，跳过")
		return payload, nil
	}
	pipe.Metadata["__aggregate_processed"] = true
	pipe.Metadata["__virtual_model"] = model
	pipe.Metadata["__aggregate_targets"] = agg.Targets
	pipe.Metadata["__failed_targets"] = []string{}
	pipe.Metadata["__retry_count"] = 0

	target, candidates, err := s.selectAvailableTarget(agg.Targets, nil)
	if err != nil {
		s.lg.Error("[aggregate] 选择目标失败", "err", err)
		return nil, &modelgateway.GatewayError{Status: http.StatusInternalServerError, Type: "internal_error", Msg: err.Error()}
	}
	if target == nil {
		s.lg.Error("[aggregate] 无可用目标")
		return nil, &modelgateway.GatewayError{Status: http.StatusServiceUnavailable, Type: "no_available_model", Msg: fmt.Sprintf("聚合模型 %q 的所有目标当前不可用", model)}
	}

	s.lg.Info("[aggregate] 选择目标模型", "virtual", model, "selected", target.Model, "channel", target.ChannelID, "base_url", target.ChannelBaseURL, "candidates", candidates)

	// 改写请求体里的 model 为真实模型名，并锁定渠道。
	body, err := rewriteBodyModel(pipe.Request.Body, target.Model)
	if err != nil {
		s.lg.Error("[aggregate] 改写请求体 model 失败", "err", err)
		return nil, &modelgateway.GatewayError{Status: http.StatusInternalServerError, Type: "internal_error", Msg: fmt.Sprintf("改写请求体失败: %v", err)}
	}
	pipe.Request.Body = body
	pipe.Request.Model = target.Model
	applyTargetMetadata(pipe.Metadata, target, candidates)
	return pipe, nil
}

// HandleProxyUpstreamFailed 透明代理失败事件：分析失败原因并切换到下一个目标模型。
func (s *Service) HandleProxyUpstreamFailed(payload any) (any, error) {
	s.lg.Debug("[aggregate] HandleProxyUpstreamFailed 开始")

	failure, ok := payload.(*modelgateway.ProxyFailurePayload)
	if !ok || failure == nil || failure.Pipe == nil || failure.Pipe.Metadata == nil {
		s.lg.Debug("[aggregate] payload 类型不匹配，跳过")
		return payload, nil
	}
	pipe := failure.Pipe

	virtualModel, ok := pipe.Metadata["__virtual_model"].(string)
	if !ok || virtualModel == "" {
		s.lg.Debug("[aggregate] 非聚合模型失败，跳过")
		return payload, nil
	}

	s.lg.Info("[aggregate] 检测到聚合模型失败", "virtual", virtualModel, "failed_model", failure.Model, "channel", failure.ChannelID, "status", failure.StatusCode, "error", failure.ErrorBody)

	strategy := s.analyzeProxyFailure(failure)
	s.lg.Info("[aggregate] 失败策略", "model", failure.Model, "action", strategy.Action, "reason", strategy.Reason, "cooldown", strategy.CooldownTime)

	modelKey := fmt.Sprintf("%s@%s", failure.Model, failure.ChannelID)
	if s.health != nil {
		errorMessage := ""
		if failure.Error != nil {
			errorMessage = failure.Error.Error()
		}
		_, healthErr := s.health.RecordFailure(context.Background(), contracts.RouteFailure{
			RequestID: failure.Pipe.RequestID, Model: failure.Model, ChannelID: failure.ChannelID,
			StatusCode: failure.StatusCode, ErrorBody: failure.ErrorBody, Error: errorMessage,
		})
		if healthErr != nil {
			s.lg.Warn("model health failure update failed", "err", healthErr)
		}
	} else {
		s.updateHealth(modelKey, strategy, failure.Error)
	}

	failedTargets, _ := pipe.Metadata["__failed_targets"].([]string)
	failedTargets = append(failedTargets, modelKey)
	pipe.Metadata["__failed_targets"] = failedTargets

	targets, _ := pipe.Metadata["__aggregate_targets"].([]types.AggregateTarget)
	nextTarget, nextCandidates, err := s.selectAvailableTarget(targets, failedTargets)
	if err != nil {
		s.lg.Error("[aggregate] 选择下一个目标失败", "err", err)
		return nil, err
	}
	if nextTarget == nil {
		s.lg.Error("[aggregate] 所有目标模型均失败", "virtual", virtualModel, "failed_count", len(failedTargets))
		return nil, fmt.Errorf("聚合模型 %q 的所有目标均失败", virtualModel)
	}

	s.lg.Info("[aggregate] 切换到下一个模型", "virtual", virtualModel, "next_model", nextTarget.Model, "channel", nextTarget.ChannelID, "candidates", nextCandidates)

	body, err := rewriteBodyModel(pipe.Request.Body, nextTarget.Model)
	if err != nil {
		s.lg.Error("[aggregate] 切换模型改写请求体失败", "err", err)
		return nil, err
	}
	pipe.Request.Body = body
	pipe.Request.Model = nextTarget.Model
	applyTargetMetadata(pipe.Metadata, nextTarget, nextCandidates)

	return &modelgateway.ProxyRetry{Pipe: pipe}, nil
}

// HandleProxyUpstreamSucceeded 透明代理成功事件：将成功转发的聚合目标标记为可用。
// 注：model-health 的 RecordSuccess 已由 model-gateway 转发路径调用（含聚合请求），
// 这里只在未装配 model-health（单测兜底 healthMap）时更新本地状态，避免重复写库。
func (s *Service) HandleProxyUpstreamSucceeded(payload any) (any, error) {
	success, ok := payload.(*modelgateway.ProxySuccessPayload)
	if !ok || success == nil || success.Pipe == nil || success.Pipe.Metadata == nil {
		return payload, nil
	}
	if _, ok := success.Pipe.Metadata["__virtual_model"].(string); !ok {
		return payload, nil
	}
	if s.health != nil {
		return payload, nil
	}

	modelKey := fmt.Sprintf("%s@%s", success.Model, success.ChannelID)
	s.healthMu.Lock()
	s.healthMap[modelKey] = &types.ModelHealth{
		Model:       modelKey,
		Status:      "available",
		FailCount:   0,
		LastChecked: time.Now().Format(time.RFC3339),
	}
	s.healthMu.Unlock()
	s.lg.Info("[aggregate] 模型转发成功，标记可用", "model", modelKey)
	s.saveHealth()
	return payload, nil
}
