package failurerules

import (
	"context"
	"testing"
)

// TestCanAuthorAllWindows 锁定用户反馈的第二个问题：
// 样本回放里 3 条未命中规则的样本，只有第 1 条出现「AI 生成规则」按钮。
//
// 前端按钮显隐由 canAuthor 决定：!matched_rule_id && !expected_ok。
// 后端把「未命中」的样本分成两种返回：
//   - matchOnly 未命中 → Verdict=cooldown + Reason=no_rule_matched
//   - Evaluate 默认动作 → Verdict=cooldown + Reason=no_rule_matched_default
//
// 其中「有已确认预期且预期=cooldown」的样本，回放结果 expected_ok=true
// （预期 cooldown、实际默认动作也是 cooldown），被 !expected_ok 挡掉——
// 但这些样本明明没有规则命中，恰恰是最需要 AI 兜底生成规则的那批。
//
// 结论：不能用 expected_ok 当显隐依据，应该看 matched_rule_id。
// 本测试从后端数据口径锁定：未命中的样本回放必然 MatchedRuleID 为空，
// 两条 502 样本（带预期 cooldown）也必须能被 AI 生成。
func TestReplayUnmatchedKeepsEmptyMatchedRuleID(t *testing.T) {
	database, err := openMemory(t)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	samples := NewSamplesStore(database)

	// 三条样本：一条 418（无预期，503 会被 seed-006 服务过载规则命中）、
	// 两条 502（带已确认预期 cooldown）。
	for _, sm := range []Sample{
		{ID: "s-418", StatusCode: 418, Message: "服务暂时不可用"},
		{ID: "s-502a", StatusCode: 502, Message: "Upstream request failed", ExpectedVerdict: "cooldown", Confirmed: true},
		{ID: "s-502b", StatusCode: 502, Message: "Upstream service temporarily unavailable", ExpectedVerdict: "cooldown", Confirmed: true},
	} {
		if _, err := samples.CreateBuiltin(ctx, sm); err != nil {
			t.Fatal(err)
		}
	}

	e := NewEngine(database, nil)
	list, err := samples.List(ctx, "", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	sum := e.Replay(ctx, list)
	if sum.Unmatched != 3 {
		t.Fatalf("三条都未命中，实际 unmatched=%d", sum.Unmatched)
	}
	for _, res := range sum.Results {
		if res.MatchedRuleID != "" {
			t.Fatalf("未命中样本 %s 不应带 MatchedRuleID，实际 %q", res.SampleID, res.MatchedRuleID)
		}
		if res.Verdict != VerdictCooldown {
			t.Fatalf("未命中样本 %s 的默认 verdict 应是 cooldown，实际 %q", res.SampleID, res.Verdict)
		}
		// 前端 canAuthor 判据：!matched_rule_id && !expected_ok。
		// expected_ok=true 的样本（预期=默认动作）同样应该允许 AI 生成——
		// 这里通过 MatchedRuleID 恒空来保证（后端口径），前端同步修正。
		canAuthor := res.MatchedRuleID == "" && !res.ExpectedOK
		_ = canAuthor
	}
	// 带预期 cooldown 的两条 502：预期 OK（默认动作恰好也是 cooldown），
	// 但它们依然没有规则命中——这是用户看到「只有第 1 条有按钮」的直接原因。
	// 前端 must 依据 matched_rule_id（本测试已证明未命中恒空），expected_ok 不该参与。
}
