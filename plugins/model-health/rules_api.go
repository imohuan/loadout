package modelhealth

import (
	"context"

	failure "loadout/plugins/failure-rules"
)

// ==== 规则引擎 API 委托（contracts.ModelHealth 扩展方法，admin-api 调用） ====

// ListFailureRules 全部规则。
func (s *Service) ListFailureRules(ctx context.Context) ([]failure.Rule, error) {
	if s.decisions == nil {
		return nil, nil
	}
	return s.decisions.List(ctx)
}

// CreateFailureRule 新建规则。
func (s *Service) CreateFailureRule(ctx context.Context, in failure.RuleInput) (failure.Rule, error) {
	rule, err := s.decisions.Create(ctx, in)
	if err == nil {
		s.rules.Reload(ctx)
	}
	return rule, err
}

// UpdateFailureRule 更新规则。
func (s *Service) UpdateFailureRule(ctx context.Context, id string, in failure.RuleInput) (failure.Rule, error) {
	rule, err := s.decisions.Update(ctx, id, in)
	if err == nil {
		s.rules.Reload(ctx)
	}
	return rule, err
}

// DeleteFailureRule 删除规则。
func (s *Service) DeleteFailureRule(ctx context.Context, id string) error {
	err := s.decisions.Delete(ctx, id)
	if err == nil {
		s.rules.Reload(ctx)
	}
	return err
}

// SetFailureRuleEnabled 启停规则。
func (s *Service) SetFailureRuleEnabled(ctx context.Context, id string, enabled bool) error {
	err := s.decisions.SetEnabled(ctx, id, enabled)
	if err == nil {
		s.rules.Reload(ctx)
	}
	return err
}

// ConfirmFailureRule AI 草稿转正。
func (s *Service) ConfirmFailureRule(ctx context.Context, id string) error {
	err := s.decisions.Confirm(ctx, id)
	if err == nil {
		s.rules.Reload(ctx)
	}
	return err
}

// VerifyFailureRule 规则样本校验（dry-run，不入库）。
func (s *Service) VerifyFailureRule(rule failure.Rule, ev failure.Evidence) bool {
	return s.rules.VerifyRule(rule, ev)
}

// ListRuleDecisions AI 判定日志。
// SetRuleAIModel 更新 AI 兜底模型（设置保存后调用；空 = 关闭 AI 兜底）。
func (s *Service) SetRuleAIModel(model string) {
	if s.aiResolver != nil {
		s.aiResolver.SetModel(model)
	}
}

func (s *Service) ListRuleDecisions(ctx context.Context, limit int) ([]map[string]any, error) {
	return s.decisions.ListDecisions(ctx, limit)
}

// EngineSnapshot 引擎状态（调试/前端展示）。
func (s *Service) RuleEngineRuleCount() int { return s.rules.Count() }
