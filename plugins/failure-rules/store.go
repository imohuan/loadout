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
	now := time.Now().UTC().Format(time.RFC3339Nano)
	id := fmt.Sprintf("ai-%d", time.Now().UTC().UnixNano())
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO failure_rules(id, name, enabled, source, confirmed, priority, provider_base_url, model,
		       match_json, action_json, created_at, updated_at)
		VALUES (?, ?, 1, 'ai', 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, in.Name, in.Priority, in.ProviderBaseURL, in.ScopeMode, mustJSON(in.ProviderBaseURLs), in.ProviderFramework, in.Model,
		mustJSON(in.Match), mustJSON(in.Action), now, now)
	if err != nil {
		return Rule{}, err
	}
	// AI 原始输出入 rule_decisions（审计）。
	_, _ = s.db.ExecContext(ctx, `
		INSERT INTO rule_decisions(request_id, model, provider_base_url, ai_model, ai_raw, verdict, created_at)
		VALUES ('', ?, ?, ?, ?, ?, ?)`,
		in.Model, in.ProviderBaseURL, aiModel, aiRaw, in.Action.Verdict, now)
	return s.Get(ctx, id)
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
		return fmt.Errorf("规则名不能为空")
	}
	if len(in.Match.Any) == 0 && len(in.Match.All) == 0 {
		return fmt.Errorf("匹配条件不能为空（any/all 至少一组）")
	}
	switch in.Action.Verdict {
	case VerdictDisableKey, VerdictDisableModel, VerdictDisableProvider, VerdictCooldown,
		VerdictIgnore, VerdictRetrySame, VerdictSwitchNext:
	default:
		return fmt.Errorf("无效的 verdict: %q", in.Action.Verdict)
	}
	switch in.Action.Recover {
	case "", "never", "daily", "fixed":
	default:
		return fmt.Errorf("无效的 recover: %q", in.Action.Recover)
	}
	return nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
