package modelhealth

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"loadout/plugins/contracts"
	failure "loadout/plugins/failure-rules"
)

// recordFailureRuled 规则引擎版失败记录：引擎裁决 → 执行器写状态。
// 返回裁决 verdict 作为 class（兼容旧返回值语义：调用方仅透传）。
func (s *Service) recordFailureRuled(ctx context.Context, f contracts.RouteFailure) (string, error) {
	// 幽灵模型防护：与 legacy catalogAllows 一致，不在渠道目录的模型不写状态。
	if ok, err := s.catalogAllows(ctx, f.ChannelID, f.Model); err != nil {
		return "", err
	} else if !ok {
		s.lg.Debug("模型不在渠道目录，跳过健康状态记录", "channel_id", f.ChannelID, "model", f.Model)
		return "", nil
	}

	// 内部路由错误（网关自身 no-candidates）不是上游故障，不写状态。
	if isInternalRoutingError(f.Error, f.ErrorBody) {
		return "internal", nil
	}

	message := strings.TrimSpace(strings.Join([]string{f.Error, f.ErrorBody}, " "))
	ev := failure.Evidence{
		RequestID:   f.RequestID,
		Model:       f.Model,
		ChannelID:   f.ChannelID,
		ProviderURL: s.providerURL(f.ChannelID),
		StatusCode:  f.StatusCode,
		BodyCode:    extractBodyCode(f.ErrorBody),
		Message:     message,
		FailCount:   s.currentFailCount(ctx, f.ChannelID, f.Model),
	}

	decision := s.rules.Evaluate(ctx, ev)
	// AI 裁决的附加参数（recover/cooldown_seconds）从 Reason 透传字段解析。
	action := actionFromDecision(decision)
	// legacy 兼容：渠道连坐语义（channel_billing + sync_billing → 渠道禁用）。
	if !executedCompat(ctx, s, f, ev, decision, action) {
		s.recordDecisionLog(ctx, ev, decision, true)
		return decision.Verdict, nil
	}
	executed, err := s.executor.Execute(ctx, failure.ActionContext{
		Evidence: ev, Decision: decision, Action: action,
	})
	if err != nil {
		return decision.Verdict, fmt.Errorf("model-health: rule action: %w", err)
	}
	if executed {
		// legacy 状态列维护：last_checked_at（UI 展示用）。
		s.touchChecked(ctx, f.ChannelID, f.Model)
	}
	s.recordDecisionLog(ctx, ev, decision, executed)
	return decision.Verdict, nil
}

// actionFromDecision 裁决 → 动作（规则命中时用规则动作；AI 时从透传字段构造）。
// executedCompat legacy 渠道连坐兜底：channel_billing（402 + 账户余额文案）且
// 渠道开了 sync_billing 时，除 key 禁用外把整个渠道（channel_states）置 disabled。
// 返回 true = 继续常规动作；false = 已执行连坐，跳过常规动作。
func executedCompat(ctx context.Context, s *Service, f contracts.RouteFailure, ev failure.Evidence, d failure.Decision, a failure.Action) bool {
	if f.StatusCode != 402 {
		return true
	}
	msg := strings.ToLower(strings.Join([]string{f.Error, f.ErrorBody}, " "))
	if !strings.Contains(msg, "account balance") && !strings.Contains(msg, "账户余额") && !strings.Contains(msg, "channel billing") {
		return true
	}
	var syncBilling bool
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(sync_billing,0) FROM channels WHERE id = ?`, f.ChannelID).Scan(&syncBilling); err != nil || !syncBilling {
		return true
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `INSERT INTO channel_states(channel_id, status, disabled_until, fail_count, last_error, last_failure_class, last_checked_at, updated_at) VALUES (?, 'disabled', NULL, 1, ?, 'channel_billing', ?, ?) ON CONFLICT(channel_id) DO UPDATE SET status='disabled', disabled_until=NULL, fail_count=channel_states.fail_count+1, last_error=excluded.last_error, last_failure_class=excluded.last_failure_class, last_checked_at=excluded.last_checked_at, updated_at=excluded.updated_at`,
		f.ChannelID, redact(f.Error), now, now)
	if err != nil {
		s.lg.Warn("failure-rules: channel_billing 连坐失败", "err", err)
	}
	return true
}

func actionFromDecision(d failure.Decision) failure.Action {
	a := failure.Action{Verdict: d.Verdict}
	if d.MatchedRuleID != "" {
		// 规则命中：使用规则配置的完整动作（recover/switch_account/cooldown）。
		return d.Action
	}
	// AI 裁决：reason 附加 "recover=xxx" / "cooldown_seconds=n"。
	if d.Reason != "" {
		for _, part := range strings.Split(d.Reason, "|") {
			if v, ok := strings.CutPrefix(part, "recover="); ok {
				a.Recover = v
			}
			if v, ok := strings.CutPrefix(part, "cooldown_seconds="); ok {
				fmt.Sscanf(v, "%d", &a.CooldownSeconds)
			}
		}
	}
	if a.Recover == "" {
		a.Recover = "fixed"
		a.CooldownSeconds = 120
	}
	return a
}

// providerURL 渠道 base_url（组身份）。
func (s *Service) providerURL(channelID string) string {
	if channelID == "" {
		return ""
	}
	var u string
	_ = s.db.QueryRowContext(context.WithoutCancel(context.Background()),
		`SELECT COALESCE(base_url,'') FROM channels WHERE id=?`, channelID).Scan(&u)
	return strings.TrimRight(u, "/")
}

// currentFailCount 当前连续失败计数（限速升级判断用）。
func (s *Service) currentFailCount(ctx context.Context, channelID, model string) int {
	var n int
	_ = s.db.QueryRowContext(ctx,
		`SELECT COALESCE(fail_count,0) FROM model_states WHERE channel_id=? AND model=?`,
		channelID, model).Scan(&n)
	return n
}

// touchChecked 更新 last_checked_at（legacy UI 列）。
func (s *Service) touchChecked(ctx context.Context, channelID, model string) {
	_, _ = s.db.ExecContext(ctx,
		`UPDATE model_states SET last_checked_at = strftime('%Y-%m-%dT%H:%M:%f','now') WHERE channel_id=? AND model=?`,
		channelID, model)
}

// recordDecisionLog 裁决日志（rule_decisions 表 + 结构化日志）。
func (s *Service) recordDecisionLog(ctx context.Context, ev failure.Evidence, d failure.Decision, executed bool) {
	if s.decisions != nil {
		s.decisions.RecordDecision(ctx, ev, d)
	}
	s.lg.Info("failure-rules: 裁决",
		"request_id", ev.RequestID, "model", ev.Model, "channel", ev.ChannelID,
		"rule", d.MatchedRuleName, "ai_model", d.AIModel, "verdict", d.Verdict,
		"executed", executed, "reason", d.Reason)
}

// isInternalRoutingError 网关自身 no-candidates 错误识别。
func isInternalRoutingError(errText string, body string) bool {
	msg := strings.ToLower(strings.Join([]string{errText, body}, " "))
	return strings.Contains(msg, "没有可用渠道支持模型") ||
		strings.Contains(msg, "no available channel for model")
}

// extractBodyCode 从错误体提取平台业务码（{"error":{"data":{"code":14018,...}}}
// 或 {"code":14018} 等常见形态；非 JSON/无码返回空）。
func extractBodyCode(body string) string {
	if body == "" || !strings.HasPrefix(strings.TrimSpace(body), "{") {
		return ""
	}
	var probe struct {
		Code  any `json:"code"`
		Error struct {
			Code any `json:"code"`
			Data struct {
				Code any `json:"code"`
			} `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &probe); err != nil {
		return ""
	}
	for _, v := range []any{probe.Error.Data.Code, probe.Error.Code, probe.Code} {
		switch t := v.(type) {
		case float64:
			return fmt.Sprintf("%d", int64(t))
		case string:
			if t != "" {
				return t
			}
		}
	}
	return ""
}
