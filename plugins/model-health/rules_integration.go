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
	// S3 修复：AI 兜底裁决不做永久禁用——硬动作降级为 cooldown 5 分钟（保底
	// 路由正确），同时异步生成草稿规则（confirmed=0）等人工确认。
	if decision.MatchedRuleID == "" && decision.AIModel != "" {
		switch decision.Verdict {
		case failure.VerdictDisableKey, failure.VerdictDisableModel, failure.VerdictDisableProvider:
			action = failure.Action{Verdict: failure.VerdictCooldown, Recover: "fixed", CooldownSeconds: 300}
			s.spawnDraft(ev, decision)
		}
	}
	// legacy 兼容：channel_billing（402+余额文案+sync_billing）→ 渠道级连坐。
	s.legacyChannelBilling(ctx, f)
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
// spawnDraft AI 判定后异步生成草稿规则（confirmed=0），人工确认后才参与匹配。
// 硬动作（disable_*）已在调用侧降级为 cooldown，这里只落草稿。
func (s *Service) spawnDraft(ev failure.Evidence, d failure.Decision) {
	go func() {
		ctx := context.WithoutCancel(context.Background())
		in := failure.RuleInput{
			Name:            "AI: " + truncateStr(d.Reason, 40) + " (" + ev.Model + " " + errStatusText(ev.StatusCode) + ")",
			Priority:        150,
			ProviderBaseURL: ev.ProviderURL,
			Model:           ev.Model,
			Match: failure.Match{Any: []failure.Condition{
				{Field: "status_code", Op: "eq", Value: ev.StatusCode},
				{Field: "message_text", Op: "contains", Value: firstToken(ev.Message, 24)},
			}},
			Action: failure.Action{Verdict: d.Verdict, Recover: d.Action.Recover, CooldownSeconds: d.Action.CooldownSeconds},
		}
		if _, err := s.decisions.CreateDraft(ctx, in, d.AIModel, d.AIRaw); err != nil {
			s.lg.Warn("failure-rules: 草稿生成失败", "err", err)
		} else {
			s.lg.Info("failure-rules: AI 草稿已生成（待确认）", "model", ev.Model, "verdict", d.Verdict)
		}
	}()
}

func truncateStr(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

func errStatusText(code int) string {
	if code == 0 {
		return "net"
	}
	return "http" + fmt.Sprint(code)
}

// firstToken 取错误消息首段（业务码/短语）作为草稿匹配锚点。
func firstToken(msg string, n int) string {
	msg = strings.TrimSpace(msg)
	for _, sep := range []string{":", "，", ",", "（", "("} {
		if i := strings.Index(msg, sep); i > 0 {
			msg = msg[:i]
		}
	}
	return truncateStr(msg, n)
}

// legacyChannelBilling legacy 渠道连坐兜底：channel_billing（402 + 账户余额文案）且
// 渠道开了 sync_billing 时，除 key 禁用外把整个渠道（channel_states）置 disabled。
func (s *Service) legacyChannelBilling(ctx context.Context, f contracts.RouteFailure) {
	if f.StatusCode != 402 {
		return
	}
	msg := strings.ToLower(strings.Join([]string{f.Error, f.ErrorBody}, " "))
	if !strings.Contains(msg, "account balance") && !strings.Contains(msg, "账户余额") && !strings.Contains(msg, "channel billing") {
		return
	}
	var syncBilling bool
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(sync_billing,0) FROM channels WHERE id = ?`, f.ChannelID).Scan(&syncBilling); err != nil || !syncBilling {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `INSERT INTO channel_states(channel_id, status, disabled_until, fail_count, last_error, last_failure_class, last_checked_at, updated_at) VALUES (?, 'disabled', NULL, 1, ?, 'channel_billing', ?, ?) ON CONFLICT(channel_id) DO UPDATE SET status='disabled', disabled_until=NULL, fail_count=channel_states.fail_count+1, last_error=excluded.last_error, last_failure_class=excluded.last_failure_class, last_checked_at=excluded.last_checked_at, updated_at=excluded.updated_at`,
		f.ChannelID, redact(f.Error), now, now)
	if err != nil {
		s.lg.Warn("failure-rules: channel_billing 连坐失败", "err", err)
	}
}

func actionFromDecision(d failure.Decision) failure.Action {
	a := failure.Action{Verdict: d.Verdict}
	if d.MatchedRuleID != "" {
		// 规则命中：使用规则配置的完整动作（recover/switch_account/cooldown）。
		return d.Action
	}
	// AI 裁决：附加参数已在 Resolver 中填入 d.Action。
	if d.Action.Recover != "" {
		return d.Action
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
