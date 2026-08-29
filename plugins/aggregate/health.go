package aggregate

import (
	"context"
	"errors"
	"fmt"
	"time"

	"loadout/core/db"
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
// 返回 (target, candidates, err)：
//   - target = 选中的目标（渠道级时 ChannelID 为空、ChannelBaseURL 保留；Key 级时 ChannelID 已具体化）；
//   - candidates = 渠道级目标展开后的可用 Key 列表（为空表示 target.ChannelID 已具体化 / 无候选）。
func (s *Service) selectAvailableTarget(targets []types.AggregateTarget, failedKeys []string) (*types.AggregateTarget, []string, error) {
	channels, err := s.loadChannels(context.Background())
	if err != nil {
		return nil, nil, err
	}
	if s.health != nil {
		for i := range targets {
			keys := modelgateway.ExpandCandidateKeys(targets[i].ChannelID, targets[i].ChannelIDs, targets[i].ChannelBaseURL, channels)
			// 兼容：渠道表无该 Key 记录（如 JSON 模式无渠道数据）时，Key 级目标仍按单 Key 直接检查。
			if len(keys) == 0 && targets[i].ChannelBaseURL == "" && targets[i].ChannelID != "" {
				keys = []modelgateway.ResolvedKey{{ChannelID: targets[i].ChannelID}}
			}
			var available []string
			for _, k := range keys {
				// 模型级失败（channelID 为空 = candidates=0 早退，该模型整体无可用渠道）：
				// 跳过该模型的所有 Key，避免死循环永远选中同一个目标（failedKeys 记的是
				// "model@"，与 "model@channelID" 永远匹配不上）。
				if contains(failedKeys, targets[i].Model+"@") {
					continue
				}
				key := fmt.Sprintf("%s@%s", targets[i].Model, k.ChannelID)
				if contains(failedKeys, key) {
					continue
				}
				availability, err := s.health.Check(context.Background(), k.ChannelID, targets[i].Model)
				if err == nil && availability.EffectiveAvailable {
					available = append(available, k.ChannelID)
				}
			}
			if len(available) == 0 {
				continue // 该目标无可用 Key，换下一个
			}
			t := targets[i]
			if t.ChannelBaseURL != "" || len(t.ChannelIDs) > 0 {
				// 渠道级 / Key 多选：不具体化单 Key，交由 proxyForward 对 candidates 逐个 failover。
				return &t, available, nil
			}
			// 单 Key：选中第一个可用 Key（保持现有语义）。
			t.ChannelID = available[0]
			return &t, nil, nil
		}
		return nil, nil, nil
	}

	s.healthMu.RLock()
	defer s.healthMu.RUnlock()

	now := time.Now()
	s.lg.Info("[aggregate] 开始选择可用目标", "total_targets", len(targets), "failed_count", len(failedKeys))

	for i := range targets {
		keys := modelgateway.ExpandCandidateKeys(targets[i].ChannelID, targets[i].ChannelIDs, targets[i].ChannelBaseURL, channels)
		// 兼容：渠道表无该 Key 记录（如 JSON 模式无渠道数据）时，Key 级目标仍按单 Key 直接检查。
		if len(keys) == 0 && targets[i].ChannelBaseURL == "" && targets[i].ChannelID != "" {
			keys = []modelgateway.ResolvedKey{{ChannelID: targets[i].ChannelID}}
		}
		if len(keys) == 0 {
			s.lg.Info("[aggregate] 目标无候选 Key，跳过", "index", i)
			continue
		}
		var available []string
		for _, k := range keys {
			// 模型级失败（channelID 为空 = candidates=0 早退，该模型整体无可用渠道）：
			// 跳过该模型的所有 Key，避免死循环永远选中同一个目标。
			if contains(failedKeys, targets[i].Model+"@") {
				s.lg.Info("[aggregate] 跳过（模型级失败）", "model", targets[i].Model)
				break
			}
			key := fmt.Sprintf("%s@%s", targets[i].Model, k.ChannelID)
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
				available = append(available, k.ChannelID)
				continue
			}

			s.lg.Info("[aggregate] 健康状态", "key", key, "status", health.Status, "fail_count", health.FailCount)

			if health.Status == "available" {
				s.lg.Info("[aggregate] 选中（可用）", "key", key)
				available = append(available, k.ChannelID)
				continue
			}

			// 检查冷却是否结束
			if health.Status == "cooling" && health.DisabledUntil != nil {
				disabledUntil, err := time.Parse(time.RFC3339, *health.DisabledUntil)
				if err == nil && now.After(disabledUntil) {
					// 冷却结束，视为可用（实际恢复由后台检查器完成）
					s.lg.Info("[aggregate] 选中（冷却已结束）", "key", key, "was_until", *health.DisabledUntil)
					available = append(available, k.ChannelID)
					continue
				}
				s.lg.Info("[aggregate] 跳过（冷却中）", "key", key, "until", *health.DisabledUntil)
			} else {
				s.lg.Info("[aggregate] 跳过（已禁用）", "key", key, "status", health.Status)
			}
		}
		if len(available) == 0 {
			s.lg.Info("[aggregate] 目标无可用 Key", "index", i)
			continue
		}
		t := targets[i]
		if t.ChannelBaseURL != "" || len(t.ChannelIDs) > 0 {
			return &t, available, nil
		}
		t.ChannelID = available[0]
		return &t, nil, nil
	}

	s.lg.Warn("[aggregate] 无可用目标")
	return nil, nil, nil
}

// loadChannels 读取全部渠道（DB 优先，JSON 回退）。
func (s *Service) loadChannels(ctx context.Context) ([]db.Channel, error) {
	if s.routing != nil {
		return s.routing.ListChannels(ctx)
	}
	var raw []types.Channel
	if err := s.st.Read(types.FileChannels, &raw); err != nil {
		if errors.Is(err, store.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]db.Channel, 0, len(raw))
	for _, ch := range raw {
		out = append(out, db.Channel{
			ID:            ch.ID,
			Name:          ch.Name,
			ChannelName:   ch.ChannelName,
			BaseURL:       ch.BaseURL,
			APIKeyCipher:  ch.APIKeyCipher,
			ManualEnabled: ch.ManualEnabled || ch.Enabled,
		})
	}
	return out, nil
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
	if _, ok := success.Pipe.Metadata[types.MetadataVirtualModel].(string); !ok {
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

// applyTargetMetadata 把选中的 target 写入管线 metadata：
//   - 渠道级（ChannelBaseURL）或 Key 多选（ChannelIDs）：写 __channel_candidates（可用 Key 列表），
//     model-gateway 据此逐 Key failover；渠道级额外写 base_url 备用；
//   - 单 Key：写 __current_channel（具体 KeyID），保持向后兼容。
func applyTargetMetadata(md map[string]any, target *types.AggregateTarget, candidates []string) {
	if target == nil || md == nil {
		return
	}
	if (target.ChannelBaseURL != "" || len(target.ChannelIDs) > 0) && len(candidates) > 0 {
		md["__current_channel"] = ""
		md["__current_channel_base_url"] = target.ChannelBaseURL
		md["__channel_candidates"] = candidates
		return
	}
	md["__current_channel"] = target.ChannelID
	md["__current_channel_base_url"] = ""
	md["__channel_candidates"] = nil
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
