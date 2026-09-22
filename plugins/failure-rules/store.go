package failurerules

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// RuleInput 前端提交的规则载荷（创建/更新共用）。
type RuleInput struct {
	Name              string   `json:"name"`
	Enabled           *bool    `json:"enabled,omitempty"`
	Priority          int      `json:"priority"`
	ProviderBaseURL   string   `json:"provider_base_url"`
	ScopeMode         string   `json:"scope_mode,omitempty"`
	ProviderBaseURLs  []string `json:"provider_base_urls,omitempty"`
	ProviderFramework string   `json:"provider_framework,omitempty"`
	Model             string   `json:"model"`
	Match             Match    `json:"match"`
	Action            Action   `json:"action"`
	Confirm           bool     `json:"confirm,omitempty"` // AI 草稿转正
}

// Store failure_rules 表 CRUD。
type Store struct{ db *sql.DB }

// ErrInvalidRule 规则不合法（用户输入问题，不是服务端故障）。
//
// 单独成型的原因：HTTP 层要把这类错误回成 400 + 可读原因，让用户看到
// 「正则不合法：…」这种具体提示，而不是笼统的「服务器内部错误」。
// 早期校验失败一路走 writeServerError，用户只看到 500，根本不知道哪里填错了。
type ErrInvalidRule struct{ Reason string }

func (e ErrInvalidRule) Error() string { return e.Reason }

// NewStore 创建 Store。
func NewStore(database *sql.DB) *Store { return &Store{db: database} }

// List 返回全部规则（含未确认草稿与禁用规则，UI 展示用）。
func (s *Store) List(ctx context.Context) ([]Rule, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, enabled, source, confirmed, priority, provider_base_url, scope_mode, COALESCE(provider_base_urls_json, '[]'), COALESCE(provider_framework, ''), model,
		       match_json, action_json, hit_count, COALESCE(last_hit_at,''), created_at, updated_at
		FROM failure_rules ORDER BY priority ASC, rowid ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rule
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Create 新建规则（source=manual；AI 生成走 CreateDraft）。
func (s *Store) Create(ctx context.Context, in RuleInput) (Rule, error) {
	if err := validateInput(in); err != nil {
		return Rule{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	id := fmt.Sprintf("rule-%d", time.Now().UTC().UnixNano())
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO failure_rules(id, name, enabled, source, confirmed, priority, provider_base_url, scope_mode, provider_base_urls_json, provider_framework, model,
		       match_json, action_json, created_at, updated_at)
		VALUES (?, ?, ?, 'manual', 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, in.Name, b2i(enabled), in.Priority, in.ProviderBaseURL, in.ScopeMode, mustJSON(in.ProviderBaseURLs), in.ProviderFramework, in.Model,
		mustJSON(in.Match), mustJSON(in.Action), now, now)
	if err != nil {
		return Rule{}, err
	}
	return s.Get(ctx, id)
}

// CreateDraft AI 草稿入库（confirmed=0，确认后才参与匹配）。
func (s *Store) CreateDraft(ctx context.Context, in RuleInput, aiModel, aiRaw string) (Rule, error) {
	if err := validateInput(in); err != nil {
		return Rule{}, err
	}
	// 去重：同一条 AI 判定反复触发（并发请求 / 缓存过期）时，不重复堆草稿。
	// 判据 = 同样的匹配条件 + 同样的动作 + 同一个模型，命中已有草稿就直接复用。
	matchJSON, actionJSON := mustJSON(in.Match), mustJSON(in.Action)
	if existingID := s.findDuplicateDraft(ctx, in.Model, matchJSON, actionJSON); existingID != "" {
		return s.Get(ctx, existingID)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	id := fmt.Sprintf("ai-%d", time.Now().UTC().UnixNano())
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO failure_rules(id, name, enabled, source, confirmed, priority, provider_base_url, scope_mode,
		       provider_base_urls_json, provider_framework, model, match_json, action_json, created_at, updated_at)
		VALUES (?, ?, 1, 'ai', 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, in.Name, in.Priority, in.ProviderBaseURL, in.ScopeMode, mustJSON(in.ProviderBaseURLs), in.ProviderFramework, in.Model,
		matchJSON, actionJSON, now, now)
	if err != nil {
		// 并发下两个请求可能同时通过上面的查重、同时插入：此时唯一索引
		// idx_ai_draft_dedup 会拒绝第二条。这不是错误——退回复用已存在的那条。
		if again := s.findDuplicateDraft(ctx, in.Model, matchJSON, actionJSON); again != "" {
			return s.Get(ctx, again)
		}
		return Rule{}, err
	}
	// AI 原始输出入 rule_decisions（审计）。
	_, _ = s.db.ExecContext(ctx, `
		INSERT INTO rule_decisions(request_id, model, provider_base_url, ai_model, ai_raw, verdict, created_at)
		VALUES ('', ?, ?, ?, ?, ?, ?)`,
		in.Model, in.ProviderBaseURL, aiModel, aiRaw, in.Action.Verdict, now)
	return s.Get(ctx, id)
}

// findDuplicateDraft 查「同模型 + 同 match + 同 action 且未确认」的 AI 草稿 id（无则空串）。
func (s *Store) findDuplicateDraft(ctx context.Context, model, matchJSON, actionJSON string) string {
	var id string
	if err := s.db.QueryRowContext(ctx, `
		SELECT id FROM failure_rules
		WHERE source='ai' AND confirmed=0 AND model=? AND match_json=? AND action_json=?
		LIMIT 1`, model, matchJSON, actionJSON).Scan(&id); err != nil {
		return ""
	}
	return id
}

// Update 更新规则（字段全量替换；Enabled nil = 不变）。
func (s *Store) Update(ctx context.Context, id string, in RuleInput) (Rule, error) {
	if err := validateInput(in); err != nil {
		return Rule{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	enabled := 1
	if in.Enabled != nil && !*in.Enabled {
		enabled = 0
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE failure_rules SET name=?, enabled=?, priority=?, provider_base_url=?, scope_mode=?, provider_base_urls_json=?, provider_framework=?, model=?,
		       match_json=?, action_json=?, updated_at=? WHERE id=?`,
		in.Name, enabled, in.Priority, in.ProviderBaseURL, in.ScopeMode, mustJSON(in.ProviderBaseURLs), in.ProviderFramework, in.Model,
		mustJSON(in.Match), mustJSON(in.Action), now, id)
	if err != nil {
		return Rule{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Rule{}, fmt.Errorf("rule not found: %s", id)
	}
	return s.Get(ctx, id)
}

// Confirm AI 草稿转正。
func (s *Store) Confirm(ctx context.Context, id string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx,
		`UPDATE failure_rules SET confirmed=1, updated_at=? WHERE id=?`, now, id)
	return err
}

// SetEnabled 启停。
func (s *Store) SetEnabled(ctx context.Context, id string, enabled bool) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx,
		`UPDATE failure_rules SET enabled=?, updated_at=? WHERE id=?`, b2i(enabled), now, id)
	return err
}

// RestoreDefaults 把内置默认规则恢复成出厂状态：按固定 ID upsert（覆盖
// name/enabled/priority/match/action/scope），已存在的行原地更新、被删掉的
// 行重新插入。用户自建的 rule-*/ai-* 规则不受影响。返回写入条数。
func (s *Store) RestoreDefaults(ctx context.Context) (int, error) {
	defaults := DefaultRules()
	if len(defaults) != len(DefaultRuleIDs) {
		return 0, fmt.Errorf("failure-rules: default rules/ids length mismatch: %d vs %d", len(defaults), len(DefaultRuleIDs))
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	n := 0
	for i, in := range defaults {
		if err := validateInput(in); err != nil {
			return n, fmt.Errorf("failure-rules: default rule %s invalid: %w", DefaultRuleIDs[i], err)
		}
		enabled := 1
		if in.Enabled != nil && !*in.Enabled {
			enabled = 0
		}
		_, err := s.db.ExecContext(ctx, `
			INSERT INTO failure_rules(id, name, enabled, source, confirmed, priority, provider_base_url,
			       scope_mode, provider_base_urls_json, provider_framework, model, match_json, action_json, created_at, updated_at)
			VALUES (?, ?, ?, 'manual', 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
			       name=excluded.name, enabled=excluded.enabled, priority=excluded.priority,
			       provider_base_url=excluded.provider_base_url, scope_mode=excluded.scope_mode,
			       provider_base_urls_json=excluded.provider_base_urls_json,
			       provider_framework=excluded.provider_framework, model=excluded.model,
			       match_json=excluded.match_json, action_json=excluded.action_json,
			       updated_at=excluded.updated_at`,
			DefaultRuleIDs[i], in.Name, enabled, in.Priority, in.ProviderBaseURL, in.ScopeMode,
			mustJSON(in.ProviderBaseURLs), in.ProviderFramework, in.Model,
			mustJSON(in.Match), mustJSON(in.Action), now, now)
		if err != nil {
			return n, fmt.Errorf("failure-rules: restore default %s: %w", DefaultRuleIDs[i], err)
		}
		n++
	}
	return n, nil
}

// Delete 删除。
func (s *Store) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM failure_rules WHERE id=?`, id)
	return err
}

// Get 单条。
func (s *Store) Get(ctx context.Context, id string) (Rule, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, enabled, source, confirmed, priority, provider_base_url, scope_mode, COALESCE(provider_base_urls_json, '[]'), COALESCE(provider_framework, ''), model,
		       match_json, action_json, hit_count, COALESCE(last_hit_at,''), created_at, updated_at
		FROM failure_rules WHERE id=?`, id)
	return scanRule(row)
}

// RecordDecision 记录一次裁决（引擎入口调用，含规则命中与 AI 判定）。

// RuleDecision 一条判定日志（前端展示用）。
type RuleDecision struct {
	ID              int64  `json:"id"`
	RequestID       string `json:"request_id"`
	Model           string `json:"model"`
	ChannelID       string `json:"channel_id"`
	ChannelName     string `json:"channel_name"`
	ProviderBaseURL string `json:"provider_base_url"`
	StatusCode      int    `json:"status_code"`
	ErrorExcerpt    string `json:"error_excerpt"`
	MatchedRuleID   string `json:"matched_rule_id"`
	MatchedRuleName string `json:"matched_rule_name"`
	AIModel         string `json:"ai_model"`
	AIRaw           string `json:"ai_raw"`
	Verdict         string `json:"verdict"`
	CreatedAt       string `json:"created_at"`
}

// ListDecisions 最近的判定日志（AI 路由 + 规则路由）。
func (s *Store) ListDecisions(ctx context.Context, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, COALESCE(request_id,''), model, COALESCE(channel_id,''), COALESCE(channel_name,''),
		       COALESCE(provider_base_url,''), status_code,
		       error_excerpt, matched_rule_id, matched_rule_name, COALESCE(ai_model,''),
		       COALESCE(ai_raw,''), verdict, created_at
		FROM rule_decisions ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var d RuleDecision
		if err := rows.Scan(&d.ID, &d.RequestID, &d.Model, &d.ChannelID, &d.ChannelName,
			&d.ProviderBaseURL, &d.StatusCode,
			&d.ErrorExcerpt, &d.MatchedRuleID, &d.MatchedRuleName, &d.AIModel,
			&d.AIRaw, &d.Verdict, &d.CreatedAt); err != nil {
			return nil, err
		}
		b, _ := json.Marshal(d)
		m := map[string]any{}
		_ = json.Unmarshal(b, &m)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) RecordDecision(ctx context.Context, ev Evidence, d Decision) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = s.db.ExecContext(ctx, `
		INSERT INTO rule_decisions(request_id, model, provider_base_url, status_code, error_excerpt,
		       matched_rule_id, matched_rule_name, ai_model, ai_raw, verdict, next_action, created_at,
		       channel_id, channel_name)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.RequestID, ev.Model, ev.ProviderURL, ev.StatusCode, truncate(ev.Message, 400),
		d.MatchedRuleID, d.MatchedRuleName, d.AIModel, truncate(d.AIRaw, 4000),
		d.Verdict, "", now, ev.ChannelID, ev.ChannelName)
}

func scanRule(row interface{ Scan(...any) error }) (Rule, error) {
	var r Rule
	var enabled, confirmed int
	var matchJSON, actionJSON, urlsJSON string
	if err := row.Scan(&r.ID, &r.Name, &enabled, &r.Source, &confirmed, &r.Priority,
		&r.ProviderBaseURL, &r.ScopeMode, &urlsJSON, &r.ProviderFramework, &r.Model,
		&matchJSON, &actionJSON, &r.HitCount, &r.LastHitAt,
		&r.CreatedAt, &r.UpdatedAt); err != nil {
		return Rule{}, err
	}
	_ = json.Unmarshal([]byte(urlsJSON), &r.ProviderBaseURLs)
	r.Enabled = enabled == 1
	r.Confirmed = confirmed == 1
	if err := json.Unmarshal([]byte(matchJSON), &r.Match); err != nil {
		return Rule{}, fmt.Errorf("match_json: %w", err)
	}
	if err := json.Unmarshal([]byte(actionJSON), &r.Action); err != nil {
		return Rule{}, fmt.Errorf("action_json: %w", err)
	}
	return r, nil
}

func validateInput(in RuleInput) error {
	if strings.TrimSpace(in.Name) == "" {
		return ErrInvalidRule{"规则名不能为空"}
	}
	if len(in.Match.Any) == 0 && len(in.Match.All) == 0 {
		return ErrInvalidRule{"匹配条件不能为空（any/all 至少一组）"}
	}
	if err := validateConditions(in.Match); err != nil {
		return err
	}
	switch in.Action.Verdict {
	case VerdictDisableKey, VerdictDisableModel, VerdictDisableProvider, VerdictCooldown,
		VerdictIgnore, VerdictRetrySame, VerdictSwitchNext:
	default:
		return ErrInvalidRule{fmt.Sprintf("无效的 verdict: %q", in.Action.Verdict)}
	}
	switch in.Action.Recover {
	case "", "never", "daily", "fixed":
	default:
		return ErrInvalidRule{fmt.Sprintf("无效的 recover: %q", in.Action.Recover)}
	}
	return nil
}

// validateConditions 逐条校验匹配条件。
//
// 目的是把「配了但永远不会命中」的条件挡在保存这一步，而不是等用户线上发现
// 规则没生效：
//   - 值不能为空（空值条件恒不匹配，等于白配）；
//   - 正则必须能编译（写错正则静默失效极具迷惑性）；
//   - 「正则」只对错误文案有意义（状态码/业务码是数字）。
func validateConditions(m Match) error {
	for _, group := range []struct {
		name  string
		conds []Condition
	}{{"any", m.Any}, {"all", m.All}} {
		for i, cond := range group.conds {
			label := fmt.Sprintf("%s 第 %d 条条件", group.name, i+1)
			if err := validateCondition(cond); err != nil {
				return ErrInvalidRule{fmt.Sprintf("%s：%s", label, err.Error())}
			}
		}
	}
	return nil
}

func validateCondition(cond Condition) error {
	switch cond.Field {
	case "status_code", "body_code", "message_text", "message_regex":
	default:
		return fmt.Errorf("未知的字段 %q", cond.Field)
	}
	if _, ok := regexPattern(cond); ok {
		// 需要正则的条件：模式必须能编译。
		if _, err := compileMessageRegex(toString(cond.Value)); err != nil {
			return fmt.Errorf("正则不合法：%v", err)
		}
		return nil
	}
	if cond.Op == "regex" {
		// field=status_code/body_code + op=regex：配了也不会生效。
		return fmt.Errorf("「正则」只能用于错误文案（当前字段是 %s）", cond.Field)
	}
	switch cond.Op {
	case "eq", "contains", "not_contains":
	default:
		return fmt.Errorf("未知的操作 %q", cond.Op)
	}
	if toString(cond.Value) == "" {
		return fmt.Errorf("匹配值不能为空")
	}
	if cond.Field == "status_code" {
		if _, ok := toInt(cond.Value); !ok {
			return fmt.Errorf("状态码必须是数字")
		}
	}
	return nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
