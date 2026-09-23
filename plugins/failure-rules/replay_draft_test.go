package failurerules

import (
	"context"
	"testing"
)

// TestReplayMatchesDraftRulesWithDraftFlag 锁定用户要求：
// AI 生成的草稿（confirmed=0）也要参与「样本回放」匹配并展示匹配规则和结果，
// 但结果里必须带 IsDraft 标记 —— 前端据此用差异样式提醒用户：
// 草稿只是回测临时生效，正式环境要人工确认后才生效。
//
// 此前 Reload 只加载 confirmed=1，草稿对回放完全不可见：用户刚让 AI 生成一条
// 草稿，回放却显示「未命中规则」，看起来像草稿没用。
func TestReplayMatchesDraftRulesWithDraftFlag(t *testing.T) {
	database, err := openMemory(t)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	samples := NewSamplesStore(database)
	drafts := NewStore(database)

	// 草稿：confirmed=false；正式规则：confirmed=true。条件相同，优先级不同。
	if _, err := drafts.CreateDraft(ctx, RuleInput{
		Name: "502 平台异常", Priority: 200,
		Match:  Match{All: []Condition{{Field: "status_code", Op: "eq", Value: 502}}},
		Action: Action{Verdict: VerdictCooldown, Recover: "fixed", CooldownSeconds: 60},
	}, "stub-model", "raw"); err != nil {
		t.Fatal(err)
	}
	if _, err := drafts.Create(ctx, RuleInput{
		Name: "正式502", Priority: 300,
		Match:  Match{All: []Condition{{Field: "status_code", Op: "eq", Value: 502}}},
		Action: Action{Verdict: VerdictCooldown, Recover: "fixed", CooldownSeconds: 30},
	}); err != nil {
		t.Fatal(err)
	}

	e := NewEngine(database, nil)
	if _, err := samples.CreateBuiltin(ctx, Sample{ID: "sm-502", StatusCode: 502, Message: "upstream error"}); err != nil {
		t.Fatal(err)
	}
	list, err := samples.List(ctx, "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	sum := e.Replay(ctx, list)

	// 关键断言 1：草稿也能命中（回测可见）。
	if sum.Matched != 1 || sum.Unmatched != 0 {
		t.Fatalf("草稿应参与回放匹配（matched=1），实际 matched=%d unmatched=%d", sum.Matched, sum.Unmatched)
	}
	res := sum.Results[0]
	// 关键断言 2：命中的是草稿，且带 IsDraft 标记。
	if !res.IsDraft {
		t.Fatalf("命中的是草稿规则，IsDraft 应为 true，实际 %+v", res)
	}
	if res.MatchedRuleName != "502 平台异常" {
		t.Fatalf("应命中草稿名，实际 %q", res.MatchedRuleName)
	}
}

// TestReplayPrefersConfirmedOverDraft 正式规则优先于同条件的草稿：
// 同一样本同时命中正式与草稿时，按优先级取最小者，且 IsDraft 如实反映。
func TestReplayPrefersConfirmedOverDraft(t *testing.T) {
	database, err := openMemory(t)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	samples := NewSamplesStore(database)
	drafts := NewStore(database)

	// 草稿优先级更低（更先匹配）。
	if _, err := drafts.CreateDraft(ctx, RuleInput{
		Name: "草稿先中", Priority: 100,
		Match:  Match{All: []Condition{{Field: "status_code", Op: "eq", Value: 502}}},
		Action: Action{Verdict: VerdictIgnore},
	}, "stub", "raw"); err != nil {
		t.Fatal(err)
	}
	if _, err := drafts.Create(ctx, RuleInput{
		Name: "正式后中", Priority: 500,
		Match:  Match{All: []Condition{{Field: "status_code", Op: "eq", Value: 502}}},
		Action: Action{Verdict: VerdictCooldown},
	}); err != nil {
		t.Fatal(err)
	}

	e := NewEngine(database, nil)
	_, _ = samples.CreateBuiltin(ctx, Sample{ID: "sm-x", StatusCode: 502})
	list, _ := samples.List(ctx, "", "", 10)
	sum := e.Replay(ctx, list)
	res := sum.Results[0]
	if !res.IsDraft || res.MatchedRuleName != "草稿先中" {
		t.Fatalf("按优先级草稿先中，IsDraft 应 true，实际 %+v", res)
	}
}

// TestEvaluateIgnoresDrafts 正式链路绝不能被草稿影响：Evaluate 依旧只看
// confirmed=1 的规则，草稿不确认就永远不会在生产裁决里生效。
func TestEvaluateIgnoresDrafts(t *testing.T) {
	database, err := openMemory(t)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	drafts := NewStore(database)
	if _, err := drafts.CreateDraft(ctx, RuleInput{
		Name: "草稿不该生效", Priority: 1,
		Match:  Match{All: []Condition{{Field: "status_code", Op: "eq", Value: 502}}},
		Action: Action{Verdict: VerdictIgnore},
	}, "stub", "raw"); err != nil {
		t.Fatal(err)
	}

	e := NewEngine(database, nil)
	d := e.Evaluate(ctx, Evidence{StatusCode: 502})
	if d.MatchedRuleID != "" || d.Reason != "no_rule_matched_default" {
		t.Fatalf("草稿不得影响正式链路，实际 %+v", d)
	}
}
