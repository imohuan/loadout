package failurerules

import (
	"context"
	"strings"
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
