package failurerules

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// AuthorRound 一轮生成的结果（前端据此显示「第 k 轮 / 通过与否」）。
type AuthorRound struct {
	Round int `json:"round"`
	// Attempt 本轮 AI 交出的规则摘要（如「status_code eq 599 → ignore」）。
	// 既给前端看「它到底试了什么」，也是下一轮提示词里「历史记录」的原料。
	Attempt string `json:"attempt,omitempty"`
	Verdict string `json:"verdict,omitempty"`
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
	// StreamTail 当前轮流式输出的尾部预览（打字机效果展示用；非 running 时为空）。
	StreamTail  string `json:"stream_tail,omitempty"`
	DraftRuleID string `json:"draft_rule_id,omitempty"`
	AIModel     string `json:"ai_model,omitempty"`
	Error       string `json:"error,omitempty"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// authorDraftSchema AI 返回的规则草稿结构。
type authorDraftSchema struct {
	Name   string `json:"name"`
	Match  Match  `json:"match"`
	Action Action `json:"action"`
	Reason string `json:"reason"`
}

// authorMaxRounds AI 生成规则的最大轮次。
// 用户要求 20 轮：复杂故障（多平台/多模型混合语义）确实需要更多修订空间。
// 每轮都落库，中途失败也能看到已完成的轮次。
const authorMaxRounds = 20

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

// UpdateStream 只更新当前轮的流式尾部预览（高频调用，专列专改）。
// 独立成方法的原因：authorLoop 的 Update 会整体覆盖 rounds_json 等字段，
// 流式预览是每几个字符就要刷一次的高频小写入，混进去会互相覆盖。
func (s *AuthorStore) UpdateStream(ctx context.Context, id, tail string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE rule_author_sessions SET stream_tail=?, updated_at=? WHERE id=?",
		tail, time.Now().UTC().Format(time.RFC3339Nano), id)
	return err
}

// Get 读会话。
func (s *AuthorStore) Get(ctx context.Context, id string) (AuthorSession, error) {
	var out AuthorSession
	var roundsJSON string
	row := s.db.QueryRowContext(ctx, "SELECT id, sample_id, status, rounds, max_rounds, rounds_json, stream_tail, draft_rule_id, ai_model, error, created_at, updated_at FROM rule_author_sessions WHERE id=?", id)
	if err := row.Scan(&out.ID, &out.SampleID, &out.Status, &out.Rounds, &out.MaxRounds, &roundsJSON, &out.StreamTail, &out.DraftRuleID, &out.AIModel, &out.Error, &out.CreatedAt, &out.UpdatedAt); err != nil {
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
	// 每轮独立 3 分钟：实测推理型模型 9~40s，3 分钟足够且不会被第一轮吃光总预算。
	return e.authorStream(ctx, ai, drafts, store, sess, sm)
}

// authorStreamTailLen 实时预览保留的流输出尾部字符数（用户只要看最后 10 个字符）。
const authorStreamTailLen = 10

// authorStream 带流式输出的多轮生成：每轮调 AI 时把增量实时写进会话
// （streams_json 只留尾部 N 个字符，供前端「打字机」式展示），
// 轮次结构照旧走 authorLoop 的注入版。
func (e *Engine) authorStream(ctx context.Context, ai *AIResolver, drafts *Store, store *AuthorStore, sess AuthorSession, sm Sample) (AuthorSession, error) {
	// currentStream 每轮的实时尾部（并发安全由闭包内自持，只有本轮在写）。
	var tailMu sync.Mutex
	tail := ""
	onChunk := func(delta string) {
		tailMu.Lock()
		tail = streamTail(tail+delta, authorStreamTailLen)
		snapshot := tail
		tailMu.Unlock()
		// 每个增量都落库（轮询接口读 streams_json 实时展示）。
		// 只更新流尾部字段，不动 rounds/status，失败静默（预览非关键路径）。
		_ = store.UpdateStream(ctx, sess.ID, snapshot)
	}
	return e.authorRunWith(ctx, func(prompt string) (string, error) {
		tailMu.Lock()
		tail = "" // 每轮开始清空预览
		tailMu.Unlock()
		return ai.chatStream(ctx, prompt, authorRoundTimeout, onChunk)
	}, drafts, store, sess, sm)
}

// authorRunWith 用可注入的对话函数跑多轮（测试用：无需真实 AI 即可覆盖
// 「非法 JSON → 修订 → 收敛」「一直不通过 → 耗尽」等分支）。
func (e *Engine) authorRunWith(ctx context.Context, chatFn func(prompt string) (string, error), drafts *Store, store *AuthorStore, sess AuthorSession, sm Sample) (AuthorSession, error) {
	return e.authorLoop(ctx, chatFn, drafts, store, sess, sm)
}

// authorLoop 多轮生成主循环（与具体 AI 通道解耦，便于测试）。
func (e *Engine) authorLoop(ctx context.Context, chatFn func(prompt string) (string, error), drafts *Store, store *AuthorStore, sess AuthorSession, sm Sample) (AuthorSession, error) {
	var detail []AuthorRound
	var draftRuleID string
	for round := 1; round <= sess.MaxRounds; round++ {
		// 历史累积：把**已完成每一轮**试过什么、为什么没通过都带给 AI。
		// 用户明确要求「继续提交给 AI 进行对话（历史记录给他）」——
		// 只传上一轮的失败原因时，AI 看不到自己前面错在哪，容易在同一类
		// 错解上反复打转（实测前几轮会重复给同样的条件）。
		prompt := buildAuthorPrompt(sm, e.rulesBrief(ctx), round, detail)
		content, err := chatFn(prompt)
		if err != nil {
			_ = store.Update(ctx, sess.ID, "failed", round-1, detail, draftRuleID, err.Error())
			return store.Get(ctx, sess.ID)
		}
		var draft authorDraftSchema
		if err := json.Unmarshal([]byte(content), &draft); err != nil || draft.Action.Verdict == "" {
			detail = append(detail, AuthorRound{
				Round: round, OK: false,
				Attempt: truncate(strings.TrimSpace(content), 200),
				Note:    "返回的不是合法规则 JSON",
			})
			_ = store.Update(ctx, sess.ID, "running", round, detail, draftRuleID, "")
			continue
		}
		// 自检：草稿规则能否正确命中本样本。
		//
		// 自检必须用「落库时那份同样的作用域」，否则会出现「自检通过、落库后
		// 永不命中」：样本 provider 为空时，落库写成 scope_mode=urls +
		// provider_base_urls=[""]，而 scopeMatches 对「列表非空但没有非空项命中」
		// 恒返回 false——用户确认后拿到一条死规则。
		in := draftRuleInput(draft, sm)
		rule := Rule{
			ID: "draft", Name: in.Name, Enabled: true, Confirmed: true,
			ScopeMode: in.ScopeMode, ProviderBaseURLs: in.ProviderBaseURLs,
			ProviderBaseURL: in.ProviderBaseURL, ProviderFramework: in.ProviderFramework,
			Model: in.Model, Match: in.Match, Action: in.Action,
		}
		hit := e.VerifyRule(rule, sm.Evidence())
		note := draft.Reason
		if !hit {
			note = "草稿规则未能命中该样本"
		}
		detail = append(detail, AuthorRound{
			Round:   round,
			Attempt: describeAttempt(draft),
			Verdict: draft.Action.Verdict,
			OK:      hit,
			Note:    note,
		})

		// 命中即收敛：把草稿入库（confirmed=0 待人工确认）。
		if hit {
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
//
// history 是前面各轮的完整记录（含每轮交出的规则与不通过原因）。用户要求
// 「继续提交给 ai 进行对话（历史记录给他）」——把历史整段带上，AI 才知道
// 自己已经试过哪些写法，从而换方向，而不是重复同一份错解。
func buildAuthorPrompt(sm Sample, rules string, round int, history []AuthorRound) string {
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
	b.WriteString("\n\n【作用域要求（重要）】")
	b.WriteString("\n规则按「平台」生效：同一个平台（同 base_url 的所有账号 Key）对外错误格式统一，")
	b.WriteString("同一类上游故障在任何模型上都会以同样方式出现。因此规则不要限定 model（model 字段留空），")
	b.WriteString("让规则对该平台所有模型生效；当前失败只是恰好发生在 " + sm.Model + " 上。")
	b.WriteString("\n只有当错误文案明确指向某个特定模型名（如 model \"xxx\" not found）时，")
	b.WriteString("才考虑在 match 里用 message_text 锚定那个模型名，而不是用 model 字段限定作用域。")
	b.WriteString("\n错误信息: ")
	b.WriteString(truncate(sm.Message, 600))
	b.WriteString("\n\n【已有规则（避免重复）】\n")
	b.WriteString(rules)
	if hist := formatHistory(history); hist != "" {
		b.WriteString("\n\n【你的历次尝试（全都没通过自检，请针对原因换一种写法，不要重复同一份规则）】\n")
		b.WriteString(hist)
	}
	b.WriteString("\n\n这是第 ")
	b.WriteString(fmt.Sprintf("%d", round))
	b.WriteString(" 轮。")
	return b.String()
}

// formatHistory 把已完成轮次拼成给 AI 看的「历史记录」；无历史时返回空串。
func formatHistory(history []AuthorRound) string {
	var b strings.Builder
	for _, h := range history {
		b.WriteString(fmt.Sprintf("第 %d 轮：你交出的规则是 %s → 未通过（%s）\n", h.Round, h.Attempt, h.Note))
	}
	// 历史太长会挤掉样本本身；只保留最近若干轮（2000 字符足够 AI 看出模式）。
	return truncate(b.String(), 2000)
}

// describeAttempt 把 AI 交出的草稿压成一行可读摘要（历史记录与前端展示共用）。
func describeAttempt(d authorDraftSchema) string {
	var parts []string
	if name := strings.TrimSpace(d.Name); name != "" {
		parts = append(parts, "「"+truncate(name, 40)+"」")
	}
	if len(d.Match.All) > 0 {
		parts = append(parts, "all("+describeConditions(d.Match.All)+")")
	}
	if len(d.Match.Any) > 0 {
		parts = append(parts, "any("+describeConditions(d.Match.Any)+")")
	}
	if d.Action.Verdict != "" {
		parts = append(parts, "动作 "+d.Action.Verdict)
	}
	if len(parts) == 0 {
		return ""
	}
	return truncate(strings.Join(parts, " "), 200)
}

// describeConditions 条件列表可读化（field op value，逗号分隔）。
func describeConditions(conds []Condition) string {
	parts := make([]string, 0, len(conds))
	for _, c := range conds {
		parts = append(parts, fmt.Sprintf("%s %s %v", c.Field, c.Op, c.Value))
	}
	return strings.Join(parts, ", ")
}

// draftRuleInput 由 AI 草稿 + 样本构造落库用的规则输入（自检与落库共用同一份）。
//
// 作用域规则：样本有 provider_base_url → 锁该平台；没有 → 退化成全局
// （不留 scope_mode=urls + 空列表，否则规则永远匹配不上，见 authorRun 注释）。
func draftRuleInput(draft authorDraftSchema, sm Sample) RuleInput {
	in := RuleInput{
		Name:     draftAuthorName(draft, sm),
		Priority: 150,
		Match:    draft.Match,
		Action:   draft.Action,
	}
	if sm.ProviderBaseURL != "" {
		in.ProviderBaseURL = sm.ProviderBaseURL
		in.ScopeMode = "urls"
		in.ProviderBaseURLs = []string{sm.ProviderBaseURL}
	}
	return in
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
