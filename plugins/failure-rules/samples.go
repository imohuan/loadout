package failurerules

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Sample 一条可复用的失败样本（「回撤」的输入单元）。
// 来源两类：real = 从 rule_decisions 导入的线上真实失败；builtin = 人工构造样本。
// expected_verdict + confirmed 是人工标注的期望判定，确认后才作为「不一致」判据。
type Sample struct {
	ID                string `json:"id"`
	ProviderBaseURL   string `json:"provider_base_url"`
	Model             string `json:"model"`
	StatusCode        int    `json:"status_code"`
	BodyCode          string `json:"body_code"`
	Message           string `json:"message"`
	ChannelID         string `json:"channel_id,omitempty"`
	ChannelName       string `json:"channel_name,omitempty"`
	ProviderFramework string `json:"provider_framework,omitempty"`
	Fingerprint       string `json:"fingerprint"`
	Source            string `json:"source"`
	ExpectedVerdict   string `json:"expected_verdict,omitempty"`
	Confirmed         bool   `json:"confirmed"`
	Note              string `json:"note,omitempty"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

// Evidence 把样本还原成规则求值用的证据（回放输入）。
func (s Sample) Evidence() Evidence {
	return Evidence{
		Model:             s.Model,
		ChannelID:         s.ChannelID,
		ChannelName:       s.ChannelName,
		ProviderURL:       s.ProviderBaseURL,
		ProviderFramework: s.ProviderFramework,
		StatusCode:        s.StatusCode,
		BodyCode:          s.BodyCode,
		Message:           s.Message,
	}
}

// ReplayResult 单条样本的回放结果（表格一行）。
type ReplayResult struct {
	SampleID        string `json:"sample_id"`
	MatchedRuleID   string `json:"matched_rule_id,omitempty"`
	MatchedRuleName string `json:"matched_rule_name,omitempty"`
	Verdict         string `json:"verdict"`
	Expected        string `json:"expected,omitempty"`
	ExpectedOK      bool   `json:"expected_ok"`
	Reason          string `json:"reason,omitempty"`
}

// ReplaySummary 批量回放汇总。
type ReplaySummary struct {
	Results         []ReplayResult `json:"results"`
	Total           int            `json:"total"`
	Matched         int            `json:"matched"`
	Unmatched       int            `json:"unmatched"`
	ConfirmedOk     int            `json:"confirmed_ok"`
	ConfirmedTotal  int            `json:"confirmed_total"`
	InconsistentIDs []string       `json:"inconsistent_ids"`
}

// isSampleDelim URL 扫描终止字符（用码值写，避免引号嵌套书写歧义）。
func isSampleDelim(c byte) bool {
	switch c {
	case 0x20, 0x09, 0x0a, 0x0d, 0x22, 0x27, 0x2c, 0x29, 0x7d, 0x5d:
		return true
	}
	return false
}

// sampleFingerprint 样本去重键：同一「状态码 + 业务码 + 错误摘要」只留一条。
// 必须先剥掉每次都变的片段（requestId、URL），否则同类故障永远去不了重
// （实测 171 条同类 502 会变成 171 条样本，回放表直接被噪音淹没）。
func sampleFingerprint(statusCode int, bodyCode, message string) string {
	msg := message
	msg = stripQuotedValue(msg, "requestid")
	msg = stripQuotedValue(msg, "request_id")
	msg = stripURLs(msg)
	msg = strings.Join(strings.Fields(strings.ToLower(msg)), " ")
	msg = truncate(msg, 160)
	return fmt.Sprintf("%d|%s|%s", statusCode, strings.TrimSpace(bodyCode), msg)
}

// stripQuotedValue 删掉 "key":"..." 里的值（保留 key 便于人眼识别）。
// 引号用 0x22 码值比较，不写字面量，避免多层转义。
func stripQuotedValue(s, key string) string {
	const quote = 0x22
	var b strings.Builder
	searchFrom := 0
	for {
		lower := strings.ToLower(s[searchFrom:])
		i := strings.Index(lower, key)
		if i < 0 {
			b.WriteString(s[searchFrom:])
			return b.String()
		}
		i += searchFrom
		// 先把 key 之前的原文原样保留。
		b.WriteString(s[searchFrom:i])
		rest := s[i+len(key):]
		colon := strings.IndexByte(rest, 0x3a)
		if colon < 0 {
			b.WriteString(s[i:])
			return b.String()
		}
		q1 := strings.IndexByte(rest[colon:], quote)
		if q1 < 0 {
			b.WriteString(s[i:])
			return b.String()
		}
		q1 += colon
		q2 := strings.IndexByte(rest[q1+1:], quote)
		if q2 < 0 {
			b.WriteString(s[i:])
			return b.String()
		}
		q2 += q1 + 1
		// 保留 key 本身（便于人眼识别是哪一段被剥掉了），删掉它的值，
		// 然后从「值之后」继续往后扫——不能回到 s 开头重扫，否则会把
		// 后面字段的值也一并吃掉（requestId 出现在 JSON 中间时尤甚）。
		b.WriteString(s[i : i+len(key)])
		searchFrom = i + len(key) + q2 + 1
	}
}

// stripURLs 把 http(s):// 起的连续片段替换成占位符。
func stripURLs(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], "http://") || strings.HasPrefix(s[i:], "https://") {
			j := i
			for j < len(s) && !isSampleDelim(s[j]) {
				j++
			}
			b.WriteString("<url>")
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// SamplesStore rule_samples 表读写。
type SamplesStore struct{ db *sql.DB }

// maxImportLimit 单次导入的扫描上限（防止一次把几年日志全拉进内存）。
// limit<=0（「导入全部」）时用它兜底，并在结果里回带 Truncated 让 UI 提示。
const maxImportLimit = 5000

// NewSamplesStore 创建样本 Store。
func NewSamplesStore(database *sql.DB) *SamplesStore { return &SamplesStore{db: database} }

const sampleCols = "id, provider_base_url, model, status_code, body_code, message, channel_id, channel_name, " +
	"provider_framework, fingerprint, source, expected_verdict, confirmed, note, created_at, updated_at"

// ImportResult 一次导入的结果，用于向用户解释「为什么只多了几条」。
//
// 为什么需要它：历史失败里绝大多数是同一类错误（实测 605 条 → 8 条样本，
// 457 条 403 合并成 1 条）。只回「新增 8」用户会以为漏导了；把「扫描了多少、
// 其中多少并进了已有样本、本次上限是多少」一并说清，才不会误解。
type ImportResult struct {
	Scanned  int `json:"scanned"`  // 实际扫描的判定日志条数
	Inserted int `json:"inserted"` // 本次新增的样本数
	// Merged 扫描到但已存在同样本（指纹命中）而被并入的条数。
	// 注意：这不是「本次合并了多少种」，而是「有多少条日志落到了已有样本上」——
	// 重复点导入时它等于 Scanned（全部并进已有样本），UI 要按这个语义措辞。
	Merged    int  `json:"merged"`
	Total     int  `json:"total"`     // 库内样本总数
	Limit     int  `json:"limit"`     // 本次扫描上限（触顶时 UI 要提示）
	Truncated bool `json:"truncated"` // 是否因为上限而没扫全
}

// ImportFromDecisions 把历史判定日志导入样本库（按 fingerprint 去重）。
// limit <= 0 表示「尽可能多」（用 maxImportLimit 兜底）。
func (s *SamplesStore) ImportFromDecisions(ctx context.Context, limit int) (ImportResult, error) {
	if limit <= 0 || limit > 5000 {
		limit = maxImportLimit
	}
	// 多取 1 条用来判断「是否还有更多」（触顶提示）。limit+1 不参与插入。
	q := "SELECT provider_base_url, model, status_code, COALESCE(error_excerpt,''), COALESCE(channel_id,''), COALESCE(channel_name,'') FROM rule_decisions WHERE error_excerpt <> '' ORDER BY id DESC LIMIT ?"
	rows, err := s.db.QueryContext(ctx, q, limit+1)
	if err != nil {
		return ImportResult{}, fmt.Errorf("failure-rules: read decisions for import: %w", err)
	}
	type cand struct {
		url, model, msg, channelID, channelName string
		status                                  int
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.url, &c.model, &c.status, &c.msg, &c.channelID, &c.channelName); err != nil {
			rows.Close()
			return ImportResult{}, err
		}
		cands = append(cands, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return ImportResult{}, err
	}
	truncated := false
	if len(cands) > limit {
		truncated = true
		cands = cands[:limit]
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	inserted := 0
	ins := "INSERT OR IGNORE INTO rule_samples(id, provider_base_url, model, status_code, body_code, message, channel_id, channel_name, fingerprint, source, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'real', ?, ?)"
	for _, c := range cands {
		bodyCode := extractBodyCodeFromText(c.msg)
		fp := sampleFingerprint(c.status, bodyCode, c.msg)
		res, err := s.db.ExecContext(ctx, ins,
			"sm-"+fp, c.url, c.model, c.status, bodyCode, truncate(c.msg, 2000), c.channelID, c.channelName, fp, now, now)
		if err != nil {
			return ImportResult{Scanned: len(cands), Inserted: inserted, Limit: limit, Truncated: truncated},
				fmt.Errorf("failure-rules: insert sample: %w", err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted++
		}
	}
	total, err := s.Count(ctx)
	if err != nil {
		return ImportResult{}, err
	}
	return ImportResult{
		Scanned:   len(cands),
		Inserted:  inserted,
		Merged:    len(cands) - inserted,
		Total:     total,
		Limit:     limit,
		Truncated: truncated,
	}, nil
}

// extractBodyCodeFromText 从错误摘要文本里抓上游业务码。
//
// 导入样本时手上只有 rule_decisions.error_excerpt 这段**已拼接的文本**
// （形如 `上游返回错误(429) {"error":{"data":{"code":14018,...}}}`），
// 不是结构化 error_body，所以这里自带一个轻量解析：截出**第一个完整的**
// JSON 对象后依次看 error.data.code → error.code → code。与 model-health 的
// extractBodyCode 同一口径（那边拿的是原始 body，这里拿的是摘要文本）。
//
// 必须按括号配平找对象结尾，不能取「最后一个 }」：摘要里可能再跟一段 JSON
// （或多组花括号），取最后一个会让整段解析失败并静默丢码，进而把同类故障
// 拆成不同指纹、让 body_code 规则在回放里误判未命中。
func extractBodyCodeFromText(text string) string {
	i := strings.IndexByte(text, 0x7b)
	if i < 0 {
		return ""
	}
	j, ok := firstJSONObjectEnd(text, i)
	if !ok {
		return ""
	}
	// 用 map 逐层取，避免为各种上游格式各写一个 struct。
	var raw map[string]any
	if err := json.Unmarshal([]byte(text[i:j+1]), &raw); err != nil {
		return ""
	}
	for _, v := range []any{digCode(raw, "error", "data", "code"), digCode(raw, "error", "code"), raw["code"]} {
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

// firstJSONObjectEnd 从 start（指向 '{'）起找配平的 '}' 下标（含引号内的转义处理）。
func firstJSONObjectEnd(s string, start int) (int, bool) {
	depth := 0
	inStr := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case escaped:
				escaped = false
			case c == 0x5c: // \
				escaped = true
			case c == 0x22: // "
				inStr = false
			}
			continue
		}
		switch c {
		case 0x22:
			inStr = true
		case 0x7b, 0x5b: // { [
			depth++
		case 0x7d, 0x5d: // } ]
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

// digCode 按 key 路径在嵌套 map 里取值。
func digCode(m map[string]any, keys ...string) any {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}

// List 返回样本（倒序）；source / model 为空则不过滤。
func (s *SamplesStore) List(ctx context.Context, source, model string, limit int) ([]Sample, error) {
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	q := "SELECT " + sampleCols + " FROM rule_samples WHERE 1=1"
	var args []any
	if source != "" {
		q += " AND source = ?"
		args = append(args, source)
	}
	if model != "" {
		q += " AND model = ?"
		args = append(args, model)
	}
	q += " ORDER BY created_at DESC, rowid DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("failure-rules: list samples: %w", err)
	}
	defer rows.Close()
	var out []Sample
	for rows.Next() {
		sm, err := scanSample(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sm)
	}
	return out, rows.Err()
}

// ListByIDs 按 id 取样本；ids 为空取全部。
func (s *SamplesStore) ListByIDs(ctx context.Context, ids []string) ([]Sample, error) {
	if len(ids) == 0 {
		// 「回放全部」的上限与导入上限保持一致（都是 2000）：早期这里写 500，
		// 样本超过 500 条后「批量回放」会静默只回放最新 500 条，UI 却显示成全量。
		return s.List(ctx, "", "", 2000)
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+sampleCols+" FROM rule_samples WHERE id IN ("+ph+")", args...)
	if err != nil {
		return nil, fmt.Errorf("failure-rules: list samples by ids: %w", err)
	}
	defer rows.Close()
	var out []Sample
	for rows.Next() {
		sm, err := scanSample(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sm)
	}
	return out, rows.Err()
}

// Get 单条样本。
func (s *SamplesStore) Get(ctx context.Context, id string) (Sample, error) {
	row := s.db.QueryRowContext(ctx, "SELECT "+sampleCols+" FROM rule_samples WHERE id = ?", id)
	return scanSample(row)
}

// CreateBuiltin 新增人工构造样本（source=builtin）。
func (s *SamplesStore) CreateBuiltin(ctx context.Context, sm Sample) (Sample, error) {
	if sm.Message == "" && sm.StatusCode == 0 {
		return Sample{}, fmt.Errorf("failure-rules: 样本需要状态码或错误文本")
	}
	if sm.BodyCode == "" {
		sm.BodyCode = extractBodyCodeFromText(sm.Message)
	}
	fp := sampleFingerprint(sm.StatusCode, sm.BodyCode, sm.Message)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// 同指纹重复新增时**保留已有的人工标注**（expected_verdict/confirmed/note）。
	// 早期用 INSERT OR REPLACE 会整体覆盖，把用户标过的预期静默洗掉，
	// 「不一致」统计随之失真。这里改成 upsert：存在则只更新内容字段。
	ins := "INSERT INTO rule_samples(id, provider_base_url, model, status_code, body_code, message, channel_id, channel_name, provider_framework, fingerprint, source, expected_verdict, confirmed, note, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'builtin', ?, ?, ?, ?, ?) " +
		"ON CONFLICT(id) DO UPDATE SET provider_base_url=excluded.provider_base_url, model=excluded.model, " +
		"status_code=excluded.status_code, body_code=excluded.body_code, message=excluded.message, " +
		"channel_id=excluded.channel_id, channel_name=excluded.channel_name, provider_framework=excluded.provider_framework, " +
		"updated_at=excluded.updated_at"
	_, err := s.db.ExecContext(ctx, ins,
		"sm-"+fp, sm.ProviderBaseURL, sm.Model, sm.StatusCode, sm.BodyCode, truncate(sm.Message, 2000),
		sm.ChannelID, sm.ChannelName, sm.ProviderFramework, fp, sm.ExpectedVerdict, b2i(sm.Confirmed), sm.Note, now, now)
	if err != nil {
		return Sample{}, fmt.Errorf("failure-rules: create sample: %w", err)
	}
	return s.Get(ctx, "sm-"+fp)
}

// SetExpectation 标注样本预期判定（confirmed=true 才计入「不一致」统计）。
func (s *SamplesStore) SetExpectation(ctx context.Context, id, expectedVerdict string, confirmed bool) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx, "UPDATE rule_samples SET expected_verdict=?, confirmed=?, updated_at=? WHERE id=?",
		expectedVerdict, b2i(confirmed), now, id)
	if err != nil {
		return fmt.Errorf("failure-rules: set expectation: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("failure-rules: sample not found: %s", id)
	}
	return nil
}

// Delete 删除样本。
func (s *SamplesStore) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM rule_samples WHERE id=?", id)
	return err
}

// Count 样本总数。
func (s *SamplesStore) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM rule_samples").Scan(&n)
	return n, err
}

func scanSample(row interface{ Scan(...any) error }) (Sample, error) {
	var sm Sample
	var confirmed int
	if err := row.Scan(&sm.ID, &sm.ProviderBaseURL, &sm.Model, &sm.StatusCode, &sm.BodyCode, &sm.Message,
		&sm.ChannelID, &sm.ChannelName, &sm.ProviderFramework, &sm.Fingerprint, &sm.Source,
		&sm.ExpectedVerdict, &confirmed, &sm.Note, &sm.CreatedAt, &sm.UpdatedAt); err != nil {
		return Sample{}, err
	}
	sm.Confirmed = confirmed == 1
	return sm, nil
}

// Replay 对给定样本跑规则求值（纯匹配：不写状态、不调上游、不调 AI）。
// 「回撤」核心：把历史失败当输入，验证现有规则能否正确处理。
func (e *Engine) Replay(ctx context.Context, samples []Sample) ReplaySummary {
	summary := ReplaySummary{Total: len(samples)}
	for _, sm := range samples {
		d := e.matchOnly(sm.Evidence())
		res := ReplayResult{
			SampleID:        sm.ID,
			MatchedRuleID:   d.MatchedRuleID,
			MatchedRuleName: d.MatchedRuleName,
			Verdict:         d.Verdict,
			Expected:        sm.ExpectedVerdict,
			Reason:          d.Reason,
		}
		if d.MatchedRuleID == "" {
			summary.Unmatched++
		} else {
			summary.Matched++
		}
		// 只有「已确认预期」的样本才参与一致性判定，避免随手填的预期当标准答案。
		if sm.Confirmed && sm.ExpectedVerdict != "" {
			summary.ConfirmedTotal++
			res.ExpectedOK = d.Verdict == sm.ExpectedVerdict
			if res.ExpectedOK {
				summary.ConfirmedOk++
			} else {
				if res.Reason == "" {
					res.Reason = fmt.Sprintf("预期 %s，实际 %s", sm.ExpectedVerdict, d.Verdict)
				}
				summary.InconsistentIDs = append(summary.InconsistentIDs, sm.ID)
			}
		}
		summary.Results = append(summary.Results, res)
	}
	return summary
}

// matchOnly 只做规则匹配：不调 AI、不计命中数（回放必须无副作用）。
func (e *Engine) matchOnly(ev Evidence) Decision {
	e.mu.RLock()
	cache := e.cache
	e.mu.RUnlock()
	for _, c := range cache {
		if !c.rule.scopeMatches(ev) {
			continue
		}
		if matchConditions(c, ev) {
			return Decision{
				MatchedRuleID:   c.rule.ID,
				MatchedRuleName: c.rule.Name,
				Verdict:         c.rule.Action.Verdict,
				Action:          c.rule.Action,
			}
		}
	}
	return Decision{Verdict: VerdictCooldown, Reason: "no_rule_matched"}
}
