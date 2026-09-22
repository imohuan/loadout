package failurerules

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// TestTruncateKeepsUTF8Valid 回归：按 rune 截断，不能切出乱码。
// 上游错误（中文）截断后若留下 \ufffd，会顺着「指纹 → 样本 id → 草稿规则名」
// 一路带到前端（实测踩过：草稿名显示成乱码）。
func TestTruncateKeepsUTF8Valid(t *testing.T) {
	s := "上游返回错误(429): 额度已用尽，请访问以下链接购买加量包"
	for n := 1; n <= len([]rune(s))+3; n++ {
		got := truncate(s, n)
		if strings.ContainsRune(got, '\uFFFD') {
			t.Fatalf("truncate(%d) 产生乱码: %q", n, got)
		}
	}
	if truncate(s, 2) != "上游" {
		t.Fatalf("按字符截断应得 2 个字，实际 %q", truncate(s, 2))
	}
}

// TestSampleFingerprintIgnoresVolatileParts 回归：同类失败必须收敛成同一指纹。
// 上游错误里 requestId / URL 每次都变，若参与指纹，171 条同类 502 会变成
// 171 条样本，回放表被噪音淹没——这正是「提取与过滤」要解决的问题。
func TestSampleFingerprintIgnoresVolatileParts(t *testing.T) {
	a := sampleFingerprint(502, "", `上游返回错误(502): Upstream service temporarily unavailable {"error":{"message":"Upstream service temporarily unavailable","requestId":"aaa-111"}}`)
	b := sampleFingerprint(502, "", `上游返回错误(502): Upstream service temporarily unavailable {"error":{"message":"Upstream service temporarily unavailable","requestId":"bbb-222"}}`)
	if a != b {
		t.Fatalf("同文案不同 requestId 应同指纹:\n a=%s\n b=%s", a, b)
	}
	// URL 也不该影响指纹。
	c := sampleFingerprint(429, "14018", `额度已用尽 购买加量包 https://www.example.com/usage 请重试`)
	d := sampleFingerprint(429, "14018", `额度已用尽 购买加量包 https://other.example.org/x 请重试`)
	if c != d {
		t.Fatalf("同文案不同 URL 应同指纹:\n c=%s\n d=%s", c, d)
	}
	// 不同业务码必须区分开（不能过度合并）。
	e := sampleFingerprint(429, "14018", "额度已用尽")
	f := sampleFingerprint(429, "11140", "额度已用尽")
	if e == f {
		t.Fatalf("不同业务码不应同指纹: %s", e)
	}
}

// TestSampleImportDedupAndReplay 覆盖「回撤」主链路：
// 导入去重 → 回放产出通过/未命中 → 确认预期后标出不一致。
func TestSampleImportDedupAndReplay(t *testing.T) {
	database, err := openMemory(t)
	if err != nil {
		t.Fatal(err)
	}
	samples := NewSamplesStore(database)
	ctx := context.Background()

	// 造 3 条判定日志：两条同类 502（应去重成 1 条）+ 一条 429/14018。
	ins := `INSERT INTO rule_decisions(request_id, model, provider_base_url, status_code, error_excerpt, matched_rule_id, verdict, created_at) VALUES (?, ?, ?, ?, ?, '', ?, '2026-01-01T00:00:00Z')`
	rows := []struct {
		sc  int
		msg string
	}{
		{502, `上游返回错误(502): bad gateway {"error":{"message":"bad gateway","requestId":"r1"}}`},
		{502, `上游返回错误(502): bad gateway {"error":{"message":"bad gateway","requestId":"r2"}}`},
		{429, `上游返回错误(429): 额度已用尽 {"error":{"data":{"code":14018}}}`},
	}
	for i, r := range rows {
		if _, err := database.Exec(ins, "req-"+string(rune('a'+i)), "glm-5.3-flash", "https://x/v1", r.sc, r.msg, "cooldown"); err != nil {
			t.Fatal(err)
		}
	}

	inserted, total, err := samples.ImportFromDecisions(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if inserted != 2 {
		t.Fatalf("3 行日志应去重成 2 条样本（两条同类 502），实际 inserted=%d", inserted)
	}
	if total != 2 {
		t.Fatalf("库内样本应 2 条，实际 %d", total)
	}
	// 重复导入不产生新行。
	again, total2, err := samples.ImportFromDecisions(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 || total2 != 2 {
		t.Fatalf("重复导入不应新增：again=%d total=%d", again, total2)
	}

	// 回放：502 没有规则覆盖（未命中），429+14018 应命中 seed-002。
	list, err := samples.List(ctx, "", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(database, nil)
	sum := e.Replay(ctx, list)
	if sum.Total != 2 {
		t.Fatalf("回放总数应为 2，实际 %d", sum.Total)
	}
	if sum.Unmatched != 1 {
		t.Fatalf("502 应未命中（无规则覆盖），实际 unmatched=%d", sum.Unmatched)
	}
	var got14018 string
	for _, r := range sum.Results {
		if r.MatchedRuleID == "seed-002" {
			got14018 = r.Verdict
		}
	}
	if got14018 != VerdictDisableKey {
		t.Fatalf("429+14018 应命中 seed-002 判 disable_key，实际 %q", got14018)
	}

	// 人工标注「502 期望 cooldown」并确认 → 与实况一致（实际也是 cooldown 默认）。
	for _, sm := range list {
		if sm.StatusCode != 502 {
			continue
		}
		// 先标成「期望 ignore」制造一个不一致，验证不一致能被指出。
		if err := samples.SetExpectation(ctx, sm.ID, VerdictIgnore, true); err != nil {
			t.Fatal(err)
		}
	}
	reloaded, err := samples.List(ctx, "", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	sum2 := e.Replay(ctx, reloaded)
	if sum2.ConfirmedTotal != 1 {
		t.Fatalf("应有 1 条已确认预期样本参与一致性判定，实际 %d", sum2.ConfirmedTotal)
	}
	if len(sum2.InconsistentIDs) != 1 {
		t.Fatalf("502 预期 ignore 与实际 cooldown 不一致，应被标出，实际 %v", sum2.InconsistentIDs)
	}
}

// TestStripQuotedValueKeepsLaterFields 回归（审查发现 #2）：
// stripQuotedValue 早期实现保留 key 后回开头重扫，会把**后面字段**整段吃掉，
// 导致指纹误合（不同故障被并成一条样本）。
func TestStripQuotedValueKeepsLaterFields(t *testing.T) {
	in := `{"requestId":"r1","model":"glm-5.3-flash","code":14018}`
	got := stripQuotedValue(in, "requestid")
	if strings.Contains(got, "r1") {
		t.Fatalf("requestId 的值应被剥掉: %s", got)
	}
	// 关键：后面的 model / code 必须还在。
	if !strings.Contains(got, "glm-5.3-flash") || !strings.Contains(got, "14018") {
		t.Fatalf("后面的字段被误删: %s", got)
	}
}

// TestExtractBodyCodeFromTextMultiJSON 回归（审查发现 #3）：
// 摘要里跟了多段 JSON 时，必须取**第一个完整对象**的 code，
// 取「最后一个 }」会解析失败并静默丢码。
func TestExtractBodyCodeFromTextMultiJSON(t *testing.T) {
	single := `上游返回错误(429): 额度已用尽 {"error":{"code":14018}}`
	if got := extractBodyCodeFromText(single); got != "14018" {
		t.Fatalf("单段 JSON 应解析出 14018，实际 %q", got)
	}
	multi := `上游返回错误(429) {"error":{"code":14018}} extra {"x":1}`
	if got := extractBodyCodeFromText(multi); got != "14018" {
		t.Fatalf("多段 JSON 应取第一个对象的 14018，实际 %q", got)
	}
	// 嵌套里带 } 的字符串不该让配平算错。
	nested := `err {"msg":"a}b","error":{"code":"11140"}}`
	if got := extractBodyCodeFromText(nested); got != "11140" {
		t.Fatalf("含转义花括号的 JSON 应解析出 11140，实际 %q", got)
	}
	if got := extractBodyCodeFromText(""); got != "" {
		t.Fatalf("空串应为空，实际 %q", got)
	}
}

// TestDraftRuleInputScopeMatchesSelfCheck 回归（审查发现 #4）：
// 自检用的规则必须与落库的作用域一致，且样本 provider 为空时降级全局——
// 否则会出现「自检通过、落库后永不命中」。
func TestDraftRuleInputScopeMatchesSelfCheck(t *testing.T) {
	draft := authorDraftSchema{Name: "x", Match: Match{All: []Condition{{Field: "status_code", Op: "eq", Value: 502}}}}
	// provider 为空 → 全局作用域（不留 urls + 空列表）。
	empty := Sample{ID: "s1", StatusCode: 502}
	in := draftRuleInput(draft, empty)
	if in.ScopeMode != "" || len(in.ProviderBaseURLs) != 0 {
		t.Fatalf("provider 为空应降级为全局作用域，实际 scope=%q urls=%v", in.ScopeMode, in.ProviderBaseURLs)
	}
	// 降级后的规则必须能命中该样本。
	e := &Engine{}
	rule := Rule{ID: "draft", Name: in.Name, Enabled: true, Confirmed: true, ScopeMode: in.ScopeMode,
		ProviderBaseURLs: in.ProviderBaseURLs, ProviderBaseURL: in.ProviderBaseURL, Model: in.Model,
		Match: in.Match, Action: in.Action}
	if !e.VerifyRule(rule, empty.Evidence()) {
		t.Fatal("provider 为空的样本，草稿规则应能命中（全局作用域）")
	}
	// provider 非空 → 锁该平台，且同样能命中。
	withURL := Sample{ID: "s2", StatusCode: 502, ProviderBaseURL: "https://p.example/v1"}
	in2 := draftRuleInput(draft, withURL)
	if in2.ScopeMode != "urls" || len(in2.ProviderBaseURLs) != 1 {
		t.Fatalf("provider 非空应锁定该平台，实际 scope=%q urls=%v", in2.ScopeMode, in2.ProviderBaseURLs)
	}
	rule2 := Rule{ID: "draft", Name: in2.Name, Enabled: true, Confirmed: true, ScopeMode: in2.ScopeMode,
		ProviderBaseURLs: in2.ProviderBaseURLs, ProviderBaseURL: in2.ProviderBaseURL, Model: in2.Model,
		Match: in2.Match, Action: in2.Action}
	if !e.VerifyRule(rule2, withURL.Evidence()) {
		t.Fatal("provider 非空的样本，草稿规则应能命中（锁该平台）")
	}
	// 同样的规则对「另一个平台」的样本不该命中。
	other := Sample{ID: "s3", StatusCode: 502, ProviderBaseURL: "https://other.example/v1"}
	if e.VerifyRule(rule2, other.Evidence()) {
		t.Fatal("锁定平台的规则不应命中其他平台")
	}
}

// TestAuthorRunHandlesBadJSONThenConverges 覆盖 author 多轮的关键分支：
// 第 1 轮 AI 返回非法 JSON → 记一轮失败并进入第 2 轮；第 2 轮返回合法草稿
// → 自检通过 → 落 confirmed=0 草稿且会话状态 done。
//
// 审查指出 author 的多轮/失败/耗尽分支此前完全没有测试覆盖。
func TestAuthorRunHandlesBadJSONThenConverges(t *testing.T) {
	database, err := openMemory(t)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	samples := NewSamplesStore(database)
	authors := NewAuthorStore(database)
	drafts := NewStore(database)

	sm, err := samples.CreateBuiltin(ctx, Sample{
		ID: "sm-author", StatusCode: 502, ProviderBaseURL: "https://p.example/v1",
		Message: `上游返回错误(502): upstream temporarily unavailable`,
	})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := authors.Create(ctx, sm.ID, "stub-model", 3)
	if err != nil {
		t.Fatal(err)
	}

	// stubAI 直接实现「AI 说什么」：第 1 轮给非法 JSON，第 2 轮给合法草稿。
	// 用一个最小的 AIResolver 替身：只替换 chat 的行为不可行（chat 是方法），
	// 因此这里用「预先写好的两个响应」驱动一个本地 chatFn。
	calls := 0
	chatFn := func(string) (string, error) {
		calls++
		if calls == 1 {
			return "这不是 JSON", nil
		}
		return `{"name":"502 冷却","match":{"all":[{"field":"status_code","op":"eq","value":502}]},"action":{"verdict":"cooldown","recover":"fixed","cooldown_seconds":120},"reason":"瞬时故障"}`, nil
	}

	e := NewEngine(database, nil)
	out, err := e.authorRunWith(ctx, chatFn, drafts, authors, sess, sm)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("应调用 AI 2 轮（第 1 轮非法 JSON、第 2 轮成功），实际 %d", calls)
	}
	if out.Status != "done" {
		t.Fatalf("会话状态应为 done，实际 %q（error=%s）", out.Status, out.Error)
	}
	if out.DraftRuleID == "" {
		t.Fatal("收敛时应落一条草稿规则")
	}
	// 草稿必须是未确认状态（人工确认才生效）。
	var confirmed int
	var matchJSON string
	if err := database.QueryRow(`SELECT confirmed, match_json FROM failure_rules WHERE id=?`, out.DraftRuleID).Scan(&confirmed, &matchJSON); err != nil {
		t.Fatal(err)
	}
	if confirmed != 0 {
		t.Fatalf("AI 草稿必须 confirmed=0（待人工确认），实际 %d", confirmed)
	}
	// 落库的规则要真能命中该样本（与自检一致）。
	if !e.VerifyRule(Rule{ID: "x", Name: "x", Enabled: true, Confirmed: true,
		ProviderBaseURL: sm.ProviderBaseURL, ScopeMode: "urls", ProviderBaseURLs: []string{sm.ProviderBaseURL},
		Match: Match{All: []Condition{{Field: "status_code", Op: "eq", Value: 502}}}, Action: Action{Verdict: VerdictCooldown}},
		sm.Evidence()) {
		t.Fatal("落库草稿的匹配条件应能命中原样本")
	}
}

// TestAuthorRunExhaustsWhenNeverMatching 覆盖「一直不通过」的耗尽分支：
// AI 每轮都返回「匹配不上该样本」的规则 → 跑满 max_rounds → 状态 exhausted。
func TestAuthorRunExhaustsWhenNeverMatching(t *testing.T) {
	database, err := openMemory(t)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	samples := NewSamplesStore(database)
	authors := NewAuthorStore(database)
	drafts := NewStore(database)

	sm, err := samples.CreateBuiltin(ctx, Sample{
		ID: "sm-exhaust", StatusCode: 502, Message: "上游返回错误(502)",
	})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := authors.Create(ctx, sm.ID, "stub-model", 2)
	if err != nil {
		t.Fatal(err)
	}
	// 每轮都给出一个「匹配不上 502 样本」的规则（条件写 599）。
	chatFn := func(string) (string, error) {
		return `{"name":"错的条件","match":{"all":[{"field":"status_code","op":"eq","value":599}]},"action":{"verdict":"ignore"},"reason":"x"}`, nil
	}
	e := NewEngine(database, nil)
	out, err := e.authorRunWith(ctx, chatFn, drafts, authors, sess, sm)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "exhausted" {
		t.Fatalf("一直不通过应标 exhausted，实际 %q", out.Status)
	}
	if out.Rounds != 2 {
		t.Fatalf("应跑满 2 轮，实际 %d", out.Rounds)
	}
	if len(out.RoundDetail) != 2 || out.RoundDetail[0].OK || out.RoundDetail[1].OK {
		t.Fatalf("两轮都该记为不通过，实际 %+v", out.RoundDetail)
	}
	// 未收敛不应落草稿。
	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM failure_rules WHERE source='ai'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("未收敛不应落草稿，实际 %d 条", n)
	}
}

// TestAIResolverKeyProviderRace 回归（审查建议 6）：resolveKey 与 SetKeyProvider
// 并发时必须无数据竞态。用 -race 跑本测试即可验证。
func TestAIResolverKeyProviderRace(t *testing.T) {
	a := NewAIResolver("m", "static-key", "http://127.0.0.1:1")
	var wg sync.WaitGroup
	// 写方：不断替换 key provider（模拟热更新 / key 轮换）。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			a.SetKeyProvider(func() string { return "k" })
		}
	}()
	// 读方：并发解析 key。
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if got := a.resolveKey(); got == "" {
					t.Errorf("resolveKey 不应返回空（有静态 key 兜底）")
					return
				}
			}
		}()
	}
	wg.Wait()
}
