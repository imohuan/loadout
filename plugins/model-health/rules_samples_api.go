package modelhealth

import (
	"context"
	"fmt"
	"time"

	failure "loadout/plugins/failure-rules"
)

// ==== 样本回放 / AI 生成规则（「回撤」）委托 ====

// ListRuleSamples 样本列表。
func (s *Service) ListRuleSamples(ctx context.Context, source, model string, limit int) ([]failure.Sample, error) {
	if s.samples == nil {
		return nil, nil
	}
	return s.samples.List(ctx, source, model, limit)
}

// ImportRuleSamples 从历史判定日志导入样本（去重）。
func (s *Service) ImportRuleSamples(ctx context.Context, limit int) (int, int, error) {
	if s.samples == nil {
		return 0, 0, fmt.Errorf("model-health: 样本库未装配")
	}
	return s.samples.ImportFromDecisions(ctx, limit)
}

// CreateRuleSample 新增构造样本。
func (s *Service) CreateRuleSample(ctx context.Context, sm failure.Sample) (failure.Sample, error) {
	if s.samples == nil {
		return failure.Sample{}, fmt.Errorf("model-health: 样本库未装配")
	}
	return s.samples.CreateBuiltin(ctx, sm)
}

// SetRuleSampleExpectation 标注预期判定。
func (s *Service) SetRuleSampleExpectation(ctx context.Context, id, expected string, confirmed bool) error {
	if s.samples == nil {
		return fmt.Errorf("model-health: 样本库未装配")
	}
	return s.samples.SetExpectation(ctx, id, expected, confirmed)
}

// DeleteRuleSample 删除样本。
func (s *Service) DeleteRuleSample(ctx context.Context, id string) error {
	if s.samples == nil {
		return fmt.Errorf("model-health: 样本库未装配")
	}
	return s.samples.Delete(ctx, id)
}

// ReplayRuleSamples 批量回放：对样本跑规则，返回通过/未命中/不一致的结果表。
// 纯匹配，无副作用（不写状态、不调上游、不调 AI）。
func (s *Service) ReplayRuleSamples(ctx context.Context, ids []string) (failure.ReplaySummary, error) {
	if s.samples == nil || s.rules == nil {
		return failure.ReplaySummary{}, fmt.Errorf("model-health: 样本库或规则引擎未装配")
	}
	list, err := s.samples.ListByIDs(ctx, ids)
	if err != nil {
		return failure.ReplaySummary{}, err
	}
	return s.rules.Replay(ctx, list), nil
}

// AuthorRuleFromSample 用一条样本启动 AI 多轮生成规则（异步）。
// 立即返回会话（前端按 session_id 轮询进度），生成物是 confirmed=0 草稿。
func (s *Service) AuthorRuleFromSample(ctx context.Context, sampleID string) (failure.AuthorSession, error) {
	if s.samples == nil || s.authors == nil || s.rules == nil {
		return failure.AuthorSession{}, fmt.Errorf("model-health: 样本库/会话库未装配")
	}
	if s.aiResolver == nil || !s.aiResolver.Enabled() {
		return failure.AuthorSession{}, fmt.Errorf("model-health: 未配置 AI 兜底模型，请先在设置页选择")
	}
	sm, err := s.samples.Get(ctx, sampleID)
	if err != nil {
		return failure.AuthorSession{}, fmt.Errorf("model-health: 样本不存在: %w", err)
	}
	sess, err := s.authors.Create(ctx, sm.ID, "", 0)
	if err != nil {
		return failure.AuthorSession{}, err
	}
	// 后台跑多轮（每轮一次完整推理 9~40s），不阻塞 HTTP 请求。
	go func(sessID string, sample failure.Sample) {
		// 每轮是一次完整推理（实测 9~40s），最多 authorMaxRounds 轮；
		// 总超时按「轮数 × 单轮上限」给足余量，避免跑到一半被整体掐断。
		bg, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), 10*time.Minute)
		defer cancel()
		if _, err := s.rules.Author(bg, s.aiResolver, s.decisions, s.authors, sample); err != nil {
			s.lg.Warn("failure-rules: AI 生成规则失败", "session", sessID, "err", err)
		}
	}(sess.ID, sm)
	return sess, nil
}

// GetRuleAuthorSession 读生成会话进度。
func (s *Service) GetRuleAuthorSession(ctx context.Context, id string) (failure.AuthorSession, error) {
	if s.authors == nil {
		return failure.AuthorSession{}, fmt.Errorf("model-health: 会话库未装配")
	}
	return s.authors.Get(ctx, id)
}

// ListRuleAuthorSessions 最近生成会话。
func (s *Service) ListRuleAuthorSessions(ctx context.Context, limit int) ([]failure.AuthorSession, error) {
	if s.authors == nil {
		return nil, nil
	}
	return s.authors.ListRecent(ctx, limit)
}
