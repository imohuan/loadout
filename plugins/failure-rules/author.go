package failurerules

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// AuthorRound 一轮生成的结果（前端据此显示「第 k 轮 / 通过与否」）。
type AuthorRound struct {
	Round   int    `json:"round"`
	Verdict string `json:"verdict"`
	OK      bool   `json:"ok"`
	Note    string `json:"note,omitempty"`
}

// AuthorSession AI「多轮」生成规则的会话状态。
type AuthorSession struct {
	ID          string        `json:"id"`
	SampleID    string        `json:"sample_id"`
	Status      string        `json:"status"`
	Rounds      int           `json:"rounds"`
	MaxRounds   int           `json:"max_rounds"`
	RoundDetail []AuthorRound `json:"round_detail"`
	DraftRuleID string        `json:"draft_rule_id,omitempty"`
	AIModel     string        `json:"ai_model,omitempty"`
	Error       string        `json:"error,omitempty"`
	CreatedAt   string        `json:"created_at"`
	UpdatedAt   string        `json:"updated_at"`
}

// authorDraftSchema AI 返回的规则草稿结构。
type authorDraftSchema struct {
	Name   string `json:"name"`
	Match  Match  `json:"match"`
	Action Action `json:"action"`
	Reason string `json:"reason"`
}

const authorMaxRounds = 3

// AuthorStore rule_author_sessions 读写。
type AuthorStore struct{ db *sql.DB }

// NewAuthorStore 创建会话 Store。
func NewAuthorStore(database *sql.DB) *AuthorStore { return &AuthorStore{db: database} }

// Create 建会话。
func (s *AuthorStore) Create(ctx context.Context, sampleID, aiModel string, maxRounds int) (AuthorSession, error) {
	if maxRounds <= 0 {
		maxRounds = authorMaxRounds
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	id := fmt.Sprintf("au-%d", time.Now().UTC().UnixNano())
	_, err := s.db.ExecContext(ctx, "INSERT INTO rule_author_sessions(id, sample_id, status, rounds, max_rounds, rounds_json, ai_model, created_at, updated_at) VALUES (?, ?, 'running', 0, ?, '[]', ?, ?, ?)",
		id, sampleID, maxRounds, aiModel, now, now)
	if err != nil {
		return AuthorSession{}, fmt.Errorf("failure-rules: create author session: %w", err)
	}
	return s.Get(ctx, id)
}

// Update 更新会话进度。
func (s *AuthorStore) Update(ctx context.Context, id, status string, rounds int, detail []AuthorRound, draftRuleID, errMsg string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	b, _ := json.Marshal(detail)
	_, err := s.db.ExecContext(ctx, "UPDATE rule_author_sessions SET status=?, rounds=?, rounds_json=?, draft_rule_id=?, error=?, updated_at=? WHERE id=?",
		status, rounds, string(b), draftRuleID, errMsg, now, id)
	return err
}

// Get 读会话。
func (s *AuthorStore) Get(ctx context.Context, id string) (AuthorSession, error) {
	var out AuthorSession
	var roundsJSON string
	row := s.db.QueryRowContext(ctx, "SELECT id, sample_id, status, rounds, max_rounds, rounds_json, draft_rule_id, ai_model, error, created_at, updated_at FROM rule_author_sessions WHERE id=?", id)
	if err := row.Scan(&out.ID, &out.SampleID, &out.Status, &out.Rounds, &out.MaxRounds, &roundsJSON, &out.DraftRuleID, &out.AIModel, &out.Error, &out.CreatedAt, &out.UpdatedAt); err != nil {
		return AuthorSession{}, err
	}
	_ = json.Unmarshal([]byte(roundsJSON), &out.RoundDetail)
	return out, nil
}

// ListRecent 最近若干会话（前端进度列兜底轮询用）。
func (s *AuthorStore) ListRecent(ctx context.Context, limit int) ([]AuthorSession, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id FROM rule_author_sessions ORDER BY created_at DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	out := make([]AuthorSession, 0, len(ids))
	for _, id := range ids {
		sess, err := s.Get(ctx, id)
		if err != nil {
			continue
		}
		out = append(out, sess)
	}
	return out, nil
}

// Author 在**已创建**的会话上跑多轮生成。
//
// 一轮 = 让 AI 出草稿 → 用样本回放自检 → 不通过就把失败原因带回下一轮修订。
// 收敛（自检通过）或达到上限即停。产出永远是 confirmed=0 的草稿，必须人工确认。
//
// 注意：会话必须由调用方先建（AuthorRuleFromSample 已建），这里**不能**再 Create——
// 早期版本在这里又建了一次，导致一次点击产生两条会话记录。
// 同步执行：调用方应在后台 goroutine 里跑（每轮是一次完整推理，9~40s）。
func (e *Engine) AuthorRun(ctx context.Context, ai *AIResolver, drafts *Store, store *AuthorStore, sess AuthorSession, sm Sample) (AuthorSession, error) {
	if ai == nil || !ai.Enabled() {
		_ = store.Update(ctx, sess.ID, "failed", sess.Rounds, nil, "", "AI 未配置，无法生成规则")
		return store.Get(ctx, sess.ID)
	}
	return e.authorRun(ctx, ai, drafts, store, sess, sm)
}

// authorRun 跑多轮循环并落库。
func (e *Engine) authorRun(ctx context.Context, ai *AIResolver, drafts *Store, store *AuthorStore, sess AuthorSession, sm Sample) (AuthorSession, error) {
	var detail []AuthorRound
	var lastReason string
	var draftRuleID string
	for round := 1; round <= sess.MaxRounds; round++ {
		prompt := buildAuthorPrompt(sm, e.rulesBrief(ctx), round, lastReason)
		content, err := ai.chat(ctx, prompt)
		if err != nil {
			_ = store.Update(ctx, sess.ID, "failed", round-1, detail, draftRuleID, err.Error())
			return store.Get(ctx, sess.ID)
		}
		var draft authorDraftSchema
		if err := json.Unmarshal([]byte(content), &draft); err != nil || draft.Action.Verdict == "" {
			lastReason = "返回的不是合法规则 JSON"
			detail = append(detail, AuthorRound{Round: round, OK: false, Note: lastReason})
			_ = store.Update(ctx, sess.ID, "running", round, detail, draftRuleID, "")
			continue
		}
		// 自检：草稿规则能否正确命中本样本。
		rule := Rule{ID: "draft", Name: draft.Name, Enabled: true, Confirmed: true, Match: draft.Match, Action: draft.Action}
		hit := e.VerifyRule(rule, sm.Evidence())
		note := draft.Reason
		if !hit {
			note = "草稿规则未能命中该样本"
			lastReason = note
		}
		detail = append(detail, AuthorRound{Round: round, Verdict: draft.Action.Verdict, OK: hit, Note: note})

		// 命中即收敛：把草稿入库（confirmed=0 待人工确认）。
		if hit {
			in := RuleInput{
				Name:             draftAuthorName(draft, sm),
				Priority:         150,
				ProviderBaseURL:  sm.ProviderBaseURL,
				ScopeMode:        "urls",
				ProviderBaseURLs: []string{sm.ProviderBaseURL},
				Model:            sm.Model,
				Match:            draft.Match,
				Action:           draft.Action,
			}
			created, err := drafts.CreateDraft(ctx, in, sess.AIModel, content)
			if err != nil {
				_ = store.Update(ctx, sess.ID, "failed", round, detail, "", err.Error())
				return store.Get(ctx, sess.ID)
			}
			draftRuleID = created.ID
			_ = store.Update(ctx, sess.ID, "done", round, detail, draftRuleID, "")
			return store.Get(ctx, sess.ID)
		}
		_ = store.Update(ctx, sess.ID, "running", round, detail, draftRuleID, "")
	}
	// 轮次用尽仍不通过：保留最后一轮草稿供人工判断，标记未收敛。
	_ = store.Update(ctx, sess.ID, "exhausted", len(detail), detail, draftRuleID, "达到最大轮次仍未通过自检")
	return store.Get(ctx, sess.ID)
}

// rulesBrief 现有规则摘要，喂给 AI 避免重复造轮子。
func (e *Engine) rulesBrief(ctx context.Context) string {
	e.mu.RLock()
	cache := e.cache
	e.mu.RUnlock()
	var b strings.Builder
	for _, c := range cache {
		b.WriteString("- ")
		b.WriteString(c.rule.Name)
		b.WriteString(" → ")
		b.WriteString(c.rule.Action.Verdict)
		b.WriteString("; ")
	}
	return truncate(b.String(), 1500)
}

// buildAuthorPrompt 组装某轮的生成提示词。
func buildAuthorPrompt(sm Sample, rules string, round int, lastReason string) string {
	var b strings.Builder
	b.WriteString("你在为一个模型网关编写「失败规则」。根据下面这次上游失败，写一条能正确处置它的规则。")
	b.WriteString("\n只返回 JSON，不要 markdown 围栏：")
	b.WriteString("\n{\"name\":\"规则名\",\"match\":{\"all\":[{\"field\":\"status_code\",\"op\":\"eq\",\"value\":429}]},\"action\":{\"verdict\":\"disable_key|disable_model|disable_provider|cooldown|ignore|switch_next\",\"recover\":\"never|daily|fixed\",\"cooldown_seconds\":数字},\"reason\":\"为什么这么判\"}")
	b.WriteString("\nmatch.field 可选 status_code | body_code | message_text；op 可选 eq | contains | not_contains。")
	b.WriteString("\n判定原则：账号级问题（额度用尽/余额不足/密钥无效）→ disable_key；")
	b.WriteString("仅该模型不可用（平台没这个模型/该模型被限制）→ disable_model；整个平台故障 → disable_provider；")
	b.WriteString("瞬时限速/过载/网络抖动 → cooldown；请求本身有问题（内容审核/参数错）→ ignore。")
	b.WriteString("\n\n【本次失败】")
	b.WriteString("\nHTTP 状态码: ")
	b.WriteString(fmt.Sprintf("%d", sm.StatusCode))
	b.WriteString("\n业务码: ")
	b.WriteString(sm.BodyCode)
	b.WriteString("\n模型: ")
	b.WriteString(sm.Model)
	b.WriteString("\n平台: ")
	b.WriteString(sm.ProviderBaseURL)
	b.WriteString("\n错误信息: ")
	b.WriteString(truncate(sm.Message, 600))
	b.WriteString("\n\n【已有规则（避免重复）】\n")
	b.WriteString(rules)
	if lastReason != "" {
		b.WriteString("\n\n【上一轮不通过原因，请修正】\n")
		b.WriteString(lastReason)
	}
	b.WriteString("\n\n这是第 ")
	b.WriteString(fmt.Sprintf("%d", round))
	b.WriteString(" 轮。")
	return b.String()
}

// draftAuthorName 草稿名：AI 给的名字为空时用可读兜底。
func draftAuthorName(d authorDraftSchema, sm Sample) string {
	name := strings.TrimSpace(d.Name)
	if name == "" {
		name = "AI 规则"
	}
	return fmt.Sprintf("AI: %s (%s %s)", truncate(name, 40), sm.Model, errStatusTextLocal(sm.StatusCode))
}

// errStatusTextLocal 状态码可读化（与 model-health 的 errStatusText 同口径，本包自持）。
func errStatusTextLocal(code int) string {
	if code == 0 {
		return "net"
	}
	return "http" + fmt.Sprint(code)
}
