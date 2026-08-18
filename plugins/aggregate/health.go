package aggregate

import (
	"context"
	"errors"
	"fmt"
	"time"

	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// loadHealth 从持久化文件加载健康状态
func (s *Service) loadHealth() {
	var healthList []types.ModelHealth
	if err := s.st.Read(types.FileModelHealth, &healthList); err != nil {
		if !errors.Is(err, store.ErrNotExist) {
			s.lg.Warn("[aggregate] 加载健康状态失败", "err", err)
		} else {
			s.lg.Info("[aggregate] 健康状态文件不存在，使用默认状态")
		}
		return
	}

	s.healthMu.Lock()
	defer s.healthMu.Unlock()

	for i := range healthList {
		s.healthMap[healthList[i].Model] = &healthList[i]
	}
	s.lg.Info("[aggregate] 加载健康状态完成", "count", len(healthList))
}

// saveHealth 保存健康状态到持久化文件
func (s *Service) saveHealth() {
	s.healthMu.RLock()
	healthList := make([]types.ModelHealth, 0, len(s.healthMap))
	for _, h := range s.healthMap {
		healthList = append(healthList, *h)
	}
	s.healthMu.RUnlock()

	if err := s.st.Write(types.FileModelHealth, healthList); err != nil {
		s.lg.Error("[aggregate] 保存健康状态失败", "err", err)
	} else {
		s.lg.Debug("[aggregate] 保存健康状态成功", "count", len(healthList))
	}
}

// selectAvailableTarget 选择第一个可用的目标模型（跳过手动禁用与自动熔断的目标）。
// 不可用（冷却/禁用）的目标不发起真实请求，由调用方直接记录日志说明原因。
func (s *Service) selectAvailableTarget(targets []types.AggregateTarget, failedKeys []string) *types.AggregateTarget {
	if s.health != nil {
		for i := range targets {
			key := fmt.Sprintf("%s@%s", targets[i].Model, targets[i].ChannelID)
			if contains(failedKeys, key) {
				continue
			}
			availability, err := s.health.Check(context.Background(), targets[i].ChannelID, targets[i].Model)
			if err == nil && availability.EffectiveAvailable {
				return &targets[i]
			}
		}
		return nil
	}
	s.healthMu.RLock()
	defer s.healthMu.RUnlock()

	now := time.Now()
	s.lg.Info("[aggregate] 开始选择可用目标", "total_targets", len(targets), "failed_count", len(failedKeys))

	for i, target := range targets {
		key := fmt.Sprintf("%s@%s", target.Model, target.ChannelID)
		s.lg.Info("[aggregate] 检查目标", "index", i, "key", key)

		// 跳过本次已失败的
		if contains(failedKeys, key) {
			s.lg.Info("[aggregate] 跳过（本次已失败）", "key", key)
			continue
		}

		// 检查健康状态
		health := s.healthMap[key]
		if health == nil {
			// 未记录过，视为可用
			s.lg.Info("[aggregate] 选中（首次使用）", "key", key)
			return &target
		}

		s.lg.Info("[aggregate] 健康状态", "key", key, "status", health.Status, "fail_count", health.FailCount)

		if health.Status == "available" {
			s.lg.Info("[aggregate] 选中（可用）", "key", key)
			return &target
		}

		// 检查冷却是否结束
		if health.Status == "cooling" && health.DisabledUntil != nil {
			disabledUntil, err := time.Parse(time.RFC3339, *health.DisabledUntil)
			if err == nil && now.After(disabledUntil) {
				// 冷却结束，视为可用（实际恢复由后台检查器完成）
				s.lg.Info("[aggregate] 选中（冷却已结束）", "key", key, "was_until", *health.DisabledUntil)
				return &target
			}
			s.lg.Info("[aggregate] 跳过（冷却中）", "key", key, "until", *health.DisabledUntil)
		} else {
			s.lg.Info("[aggregate] 跳过（已禁用）", "key", key, "status", health.Status)
		}

		// disabled 或 cooling 中，跳过
	}

	s.lg.Warn("[aggregate] 无可用目标")
	return nil
}

// updateHealth 更新模型健康状态
func (s *Service) updateHealth(modelKey string, strategy types.FailureStrategy, err error) {
	s.healthMu.Lock()

	health := s.healthMap[modelKey]
	if health == nil {
		health = &types.ModelHealth{
			Model:       modelKey,
			Status:      "available",
			FailCount:   0,
			LastChecked: time.Now().Format(time.RFC3339),
		}
		s.healthMap[modelKey] = health
		s.lg.Debug("[aggregate] 创建健康记录", "key", modelKey)
	}

	health.FailCount++
	health.LastError = ""
	if err != nil {
		health.LastError = err.Error()
	}
	health.LastChecked = time.Now().Format(time.RFC3339)

	s.lg.Debug("[aggregate] 更新健康状态前", "key", modelKey, "old_status", health.Status, "fail_count", health.FailCount)

	switch strategy.Action {
	case "disable":
		health.Status = "disabled"
		health.DisabledUntil = nil
		s.lg.Warn("[aggregate] 模型已禁用", "model", modelKey, "reason", strategy.Reason, "fail_count", health.FailCount)

	case "cooldown":
		health.Status = "cooling"
		until := time.Now().Add(time.Duration(strategy.CooldownTime) * time.Minute).Format(time.RFC3339)
		health.DisabledUntil = &until
		s.lg.Warn("[aggregate] 模型进入冷却", "model", modelKey, "until", until, "reason", strategy.Reason, "fail_count", health.FailCount)

	case "skip":
		// skip 不改变状态，只是本次跳过
		s.lg.Info("[aggregate] 跳过模型（本次）", "model", modelKey, "reason", strategy.Reason, "fail_count", health.FailCount)
	}

	// 先释放写锁再持久化（saveHealth 内部需要读锁，持写锁调用会死锁）。
	s.healthMu.Unlock()
	s.saveHealth()
}

// HandleUpstreamSucceeded 将成功转发的聚合目标标记为可用。
func (s *Service) HandleUpstreamSucceeded(payload any) (any, error) {
	success, ok := payload.(*modelgateway.SuccessPayload)
	if !ok || success == nil || success.Pipe == nil || success.Pipe.Metadata == nil {
		return payload, nil
	}
	if _, ok := success.Pipe.Metadata["__virtual_model"].(string); !ok {
		return payload, nil
	}

	modelKey := fmt.Sprintf("%s@%s", success.Model, success.ChannelID)
	if s.health != nil {
		if err := s.health.RecordSuccess(context.Background(), success.ChannelID, success.Model); err != nil {
			s.lg.Warn("model health success update failed", "err", err)
		}
		return payload, nil
	}
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

// contains 检查字符串切片是否包含指定元素
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
