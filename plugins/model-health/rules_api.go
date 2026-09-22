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
// 只校验「匹配条件」，跳过作用域：编辑弹窗里的样本只有状态码/业务码/文案，
// 没有 provider/model，作用域是保存规则时的平台范围，不参与此处预测。
func (s *Service) VerifyFailureRule(rule failure.Rule, ev failure.Evidence) bool {
	return s.rules.VerifyRuleMatchOnly(rule, ev)
}

// ListRuleDecisions AI 判定日志。
// SetRuleAIModel 更新 AI 兜底模型（设置保存后调用；空 = 关闭 AI 兜底）。
// SetKeyResolver 注入 SK key 明文解析器（plugin.go 装配时注入）。
func (s *Service) SetKeyResolver(fn func() string) {
	s.keyResolver = fn
	if s.aiResolver != nil {
		s.aiResolver.SetKeyProvider(fn)
	}
}

func (s *Service) SetRuleAIModel(model string) {
	// 空串 = 用户在设置页关掉 AI 兜底：记「关过」，否则重启会被内置模型重新打开。
	if model == "" {
		s.aiFallbackOff = true
	} else {
		s.aiFallbackOff = false
	}
	if s.aiResolver != nil {
		s.aiResolver.SetModel(model)
	}
}

// SetBuiltinFallbackModel 用「内置模型」兜底 AI 判定模型。
//
// 仅当用户没有在设置页显式配置 rule_ai_model 时生效——用户的选择永远优先。
// 有了它，开箱即用：多模态插件里配了图片/音频模型，失败规则就能直接用 AI 兜底，
// 不必再去设置页手动选一遍。
func (s *Service) SetBuiltinFallbackModel(model string) {
	if s.aiResolver == nil || model == "" {
		return
	}
	// 用户显式关过（存了关闭哨兵）就不要再自动打开：
	// 否则「设置页清空 = 关闭」只能撑到本次进程结束，重启又被内置模型顶开，
	// 用户无法持久关闭 AI 兜底。
	if s.aiFallbackOff {
		return
	}
	if s.aiResolver.Enabled() {
		return // 用户已显式配置，尊重用户
	}
	s.aiResolver.SetModel(model)
	s.lg.Info("failure-rules: AI 兜底使用内置模型", "model", model)
}

// RestoreDefaultFailureRules 恢复内置默认规则并热重载引擎（UI「恢复默认规则」）。
func (s *Service) RestoreDefaultFailureRules(ctx context.Context) (int, error) {
	n, err := s.decisions.RestoreDefaults(ctx)
	if err != nil {
		return n, err
	}
	s.rules.Reload(ctx)
	return n, nil
}

func (s *Service) ListRuleDecisions(ctx context.Context, limit int) ([]map[string]any, error) {
	return s.decisions.ListDecisions(ctx, limit)
}

// EngineSnapshot 引擎状态（调试/前端展示）。
func (s *Service) RuleEngineRuleCount() int { return s.rules.Count() }
