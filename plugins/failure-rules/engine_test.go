package failurerules

import (
	"context"
	"encoding/json"
	"testing"
)

func newTestEngine(t *testing.T) (*Engine, string) {
	t.Helper()
	database, err := openMemory(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return NewEngine(database, nil), ""
}

func TestEngineMatchesBodyCodeDailyQuota(t *testing.T) {
	e, _ := newTestEngine(t)
	d := e.Evaluate(context.Background(), Evidence{
		Model: "glm-5.3-flash", ChannelID: "ch1", ProviderURL: "https://api.codebuddy.cn",
		StatusCode: 429, BodyCode: "14018", Message: "额度已用尽，请购买加量包",
	})
	if d.MatchedRuleID != "seed-002" {
		t.Fatalf("expected seed-002, got %+v", d)
	}
	if d.Verdict != VerdictDisableKey {
		t.Fatalf("expected disable_key, got %s", d.Verdict)
	}
}

// TestStateClassEncodesVerdictAndRecover 固定写入 last_failure_class 的命名格式：
// 前端按 `rule_<verdict>_<recover>` 区分「永久禁用 / 次日恢复 / 定时冷却」，
// 一旦退化成裸 verdict，所有成因又会被显示成同一个「冷却中」。
func TestStateClassEncodesVerdictAndRecover(t *testing.T) {
	cases := []struct {
		verdict string
		action  Action
		want    string
	}{
		{"disable_key", Action{Recover: "daily", DailyResetHour: 12}, "rule_disable_key_daily"},
		{"disable_key", Action{Recover: "never"}, "rule_disable_key_never"},
		{"cooldown", Action{Recover: "fixed", CooldownSeconds: 120}, "rule_cooldown_fixed"},
		{"disable_model", Action{Recover: "never"}, "rule_disable_model_never"},
		// recover 缺省时按执行层默认 fixed 处理，不能留下裸 verdict。
		{"cooldown", Action{}, "rule_cooldown_fixed"},
	}
	for _, c := range cases {
		if got := stateClass(c.verdict, c.action); got != c.want {
			t.Errorf("stateClass(%q, %+v) = %q, want %q", c.verdict, c.action, got, c.want)
		}
	}
}

func TestEngineIgnoreClientCancel(t *testing.T) {
	e, _ := newTestEngine(t)
	d := e.Evaluate(context.Background(), Evidence{
		Model: "m", StatusCode: 0, Message: "Post \"https://x\": context canceled",
	})
	if d.Verdict != VerdictIgnore {
		t.Fatalf("expected ignore, got %+v", d)
	}
}

func TestEngineRateLimitCooldown(t *testing.T) {
	e, _ := newTestEngine(t)
	d := e.Evaluate(context.Background(), Evidence{
		Model: "m", StatusCode: 429, Message: "Too Many Requests",
	})
	if d.MatchedRuleID != "seed-005" || d.Verdict != VerdictCooldown {
		t.Fatalf("expected seed-005 cooldown, got %+v", d)
	}
}

func TestEngineDailyQuotaBeatsRateLimit(t *testing.T) {
	// 429 + 14018（额度）必须命中额度规则而不是限速规则（priority 20 < 50）。
	e, _ := newTestEngine(t)
	d := e.Evaluate(context.Background(), Evidence{
		Model: "m", StatusCode: 429, BodyCode: "14018",
		Message: "额度已用尽，请访问以下链接",
	})
	if d.MatchedRuleID != "seed-002" {
		t.Fatalf("expected seed-002, got %+v", d)
	}
}

func TestEngineScopeFilter(t *testing.T) {
	e, _ := newTestEngine(t)
	// seed-002 带平台过滤后不再命中通用证据。
	d := e.Evaluate(context.Background(), Evidence{
		Model: "m", StatusCode: 429, BodyCode: "14018", Message: "额度已用尽",
	})
	if d.MatchedRuleID != "seed-002" {
		t.Fatalf("scope filter should not break global rule, got %+v", d)
	}
}

func TestEngineVerifySamples(t *testing.T) {
	e, _ := newTestEngine(t)
	rule := Rule{
		ID: "r1", Name: "测试规则", Enabled: true, Confirmed: true, Priority: 1,
		Match:  Match{Any: []Condition{{Field: "status_code", Op: "eq", Value: float64(429)}}},
		Action: Action{Verdict: VerdictCooldown, CooldownSeconds: 60},
	}
	hit := e.VerifyRule(rule, Evidence{StatusCode: 429, Message: "rate limit"})
	if !hit {
		t.Fatal("expected hit")
	}
	miss := e.VerifyRule(rule, Evidence{StatusCode: 500, Message: "err"})
	if miss {
		t.Fatal("expected miss")
	}
}

func TestMatchJSONRoundTrip(t *testing.T) {
	m := Match{Any: []Condition{
		{Field: "body_code", Op: "eq", Value: "14018"},
		{Field: "message_text", Op: "contains", Value: "额度"},
	}}
	b, _ := json.Marshal(m)
	var back Match
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Any) != 2 || back.Any[0].Value != "14018" {
		t.Fatalf("roundtrip mismatch: %s", b)
	}
}
