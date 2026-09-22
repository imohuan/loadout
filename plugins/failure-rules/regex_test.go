package failurerules

import "testing"

// TestRegexMultipleConditionsPerRule 锁定一个真实踩过的坑：
// compileRegex 只保留规则里**第一个** message_regex 条件，
// 且 evalCondition 用同一个 *regexp.Regexp 去判所有正则条件。
//
// 后果（用户反馈「正则匹配存在问题」）：
//   - any 里写两条正则、第二条才命中 → 被算成不命中；
//   - all 里写两条正则、第二条本该不命中 → 被算成命中。
//
// 一条规则里出现两个正则就直接错乱。
func TestRegexMultipleConditionsPerRule(t *testing.T) {
	e := &Engine{}
	ev := Evidence{StatusCode: 429, Message: "quota exhausted 额度已用尽"}

	// any：第一条正则不命中、第二条命中 → any 语义下应命中。
	anyRule := Rule{ID: "r1", Enabled: true, Confirmed: true,
		Match: Match{Any: []Condition{
			{Field: "message_regex", Op: "regex", Value: "ZZZ_NEVER"},
			{Field: "message_regex", Op: "regex", Value: "额度.*用尽"},
		}},
		Action: Action{Verdict: VerdictIgnore},
	}
	if !e.VerifyRuleMatchOnly(anyRule, ev) {
		t.Fatal("any 里第二条正则命中，应判命中（compileRegex 只取首个正则导致误判）")
	}

	// all：第一条命中、第二条不命中 → any/all 语义下应不命中。
	allRule := Rule{ID: "r2", Enabled: true, Confirmed: true,
		Match: Match{All: []Condition{
			{Field: "message_regex", Op: "regex", Value: "额度.*用尽"},
			{Field: "message_regex", Op: "regex", Value: "ZZZ_NEVER"},
		}},
		Action: Action{Verdict: VerdictIgnore},
	}
	if e.VerifyRuleMatchOnly(allRule, ev) {
		t.Fatal("all 里第二条正则不命中，应判不命中（共用首个正则导致误判）")
	}
}

// TestRegexFieldRespectsOpAndField 锁定另外两个坑：
//   - field=message_regex + op=contains（前端 UI 允许这种组合）此前一律 false；
//   - field=message_text + op=regex 此前也一律 false。
//
// 用户在界面上按「正则」选了字段/操作，两种组合都该真的按正则去匹配。
func TestRegexFieldRespectsOpAndField(t *testing.T) {
	e := &Engine{}
	ev := Evidence{Message: "upstream error: 额度已用尽"}

	cases := []struct {
		name string
		cond Condition
	}{
		{"message_regex + contains", Condition{Field: "message_regex", Op: "contains", Value: "额度.*用尽"}},
		{"message_text + regex", Condition{Field: "message_text", Op: "regex", Value: "额度.*用尽"}},
		{"message_regex + regex", Condition{Field: "message_regex", Op: "regex", Value: "额度.*用尽"}},
	}
	for _, c := range cases {
		rule := Rule{ID: "r", Enabled: true, Confirmed: true,
			Match:  Match{Any: []Condition{c.cond}},
			Action: Action{Verdict: VerdictIgnore},
		}
		if !e.VerifyRuleMatchOnly(rule, ev) {
			t.Fatalf("%s：正则「额度.*用尽」应命中样本，实际未命中", c.name)
		}
	}

	// 不匹配的正则不能误报。
	miss := Rule{ID: "r3", Enabled: true, Confirmed: true,
		Match:  Match{Any: []Condition{{Field: "message_regex", Op: "regex", Value: "ZZZ_NEVER"}}},
		Action: Action{Verdict: VerdictIgnore},
	}
	if e.VerifyRuleMatchOnly(miss, ev) {
		t.Fatal("不匹配的正则不应命中")
	}
}

// TestRegexInvalidPatternDoesNotMatch 非法正则不应 panic，判定为不命中。
func TestRegexInvalidPatternDoesNotMatch(t *testing.T) {
	e := &Engine{}
	ev := Evidence{Message: "anything"}
	rule := Rule{ID: "bad", Enabled: true, Confirmed: true,
		Match:  Match{Any: []Condition{{Field: "message_regex", Op: "regex", Value: "([unclosed"}}},
		Action: Action{Verdict: VerdictIgnore},
	}
	if e.VerifyRuleMatchOnly(rule, ev) {
		t.Fatal("非法正则应判不命中（且不能 panic）")
	}
}

// TestRegexIsCaseInsensitiveLikeContains 正则要和「包含」保持一致的大小写口径。
//
// 「包含」是忽略大小写的（两侧都 ToLower），上游错误文案大小写又很随意
// （实测 12 条样本里 10 条是大小写混排）。如果正则严格区分大小写，
// 用户按「包含」的习惯写 regex 就会莫名不命中——两种操作对同样的字面量
// 给出不同结论，这种不一致最难排查。
func TestRegexIsCaseInsensitiveLikeContains(t *testing.T) {
	e := &Engine{}
	ev := Evidence{Message: "Upstream error: 404 Route Not Found"}

	// 小写正则应命中大写文案（与 contains("route not found") 结论一致）。
	rx := Rule{ID: "rx", Enabled: true, Confirmed: true,
		Match:  Match{Any: []Condition{{Field: "message_text", Op: "regex", Value: "route not found"}}},
		Action: Action{Verdict: VerdictIgnore},
	}
	if !e.VerifyRuleMatchOnly(rx, ev) {
		t.Fatal("正则应与「包含」同样忽略大小写：小写正则必须能命中大写文案")
	}
	// contains 同样输入必须给出同样结论（一致性回归）。
	ct := Rule{ID: "ct", Enabled: true, Confirmed: true,
		Match:  Match{Any: []Condition{{Field: "message_text", Op: "contains", Value: "route not found"}}},
		Action: Action{Verdict: VerdictIgnore},
	}
	if !e.VerifyRuleMatchOnly(ct, ev) {
		t.Fatal("contains 忽略大小写这一前提失效了")
	}
}
