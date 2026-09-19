package failurerules

import (
	"testing"
)

// TestScopeModeUrls 多选平台：命中列表内平台，不命中其它平台。
func TestScopeModeUrls(t *testing.T) {
	e, _ := newTestEngine(t)
	rule := Rule{
		ID:               "r-urls",
		Name:             "多平台规则",
		Enabled:          true,
		Confirmed:        true,
		Priority:         1,
		ScopeMode:        "urls",
		ProviderBaseURLs: []string{"https://a.com/v1", "https://b.com/v1"},
		Match:            Match{Any: []Condition{{Field: "status_code", Op: "eq", Value: float64(429)}}},
		Action:           Action{Verdict: VerdictCooldown},
	}
	hit := e.VerifyRule(rule, Evidence{ProviderURL: "https://b.com/v1", StatusCode: 429})
	if !hit {
		t.Fatal("expected hit for listed provider")
	}
	miss := e.VerifyRule(rule, Evidence{ProviderURL: "https://c.com/v1", StatusCode: 429})
	if miss {
		t.Fatal("expected miss for unlisted provider")
	}
}

// TestScopeModeFramework 按框架：渠道框架标签匹配。
func TestScopeModeFramework(t *testing.T) {
	e, _ := newTestEngine(t)
	rule := Rule{
		ID:                "r-fw",
		Name:              "NewAPI 通用规则",
		Enabled:           true,
		Confirmed:         true,
		Priority:          1,
		ScopeMode:         "framework",
		ProviderFramework: "newapi",
		Match:             Match{Any: []Condition{{Field: "message_text", Op: "contains", Value: "额度已用尽"}}},
		Action:            Action{Verdict: VerdictDisableKey, Recover: "daily"},
	}
	hit := e.VerifyRule(rule, Evidence{ProviderURL: "https://x.com/v1", ProviderFramework: "newapi", Message: "额度已用尽"})
	if !hit {
		t.Fatal("expected hit for newapi channel")
	}
	miss := e.VerifyRule(rule, Evidence{ProviderURL: "https://y.com/v1", ProviderFramework: "one-api", Message: "额度已用尽"})
	if miss {
		t.Fatal("expected miss for one-api channel")
	}
	miss2 := e.VerifyRule(rule, Evidence{ProviderURL: "https://z.com/v1", Message: "额度已用尽"})
	if miss2 {
		t.Fatal("expected miss for unlabeled channel")
	}
}

// TestScopeLegacyCompat 旧数据兼容：scope_mode 空 + provider_base_url 非空 = 单平台。
func TestScopeLegacyCompat(t *testing.T) {
	e, _ := newTestEngine(t)
	rule := Rule{
		ID:              "r-legacy",
		Name:            "旧单平台规则",
		Enabled:         true,
		Confirmed:       true,
		Priority:        1,
		ProviderBaseURL: "https://old.com/v1",
		Match:           Match{Any: []Condition{{Field: "status_code", Op: "eq", Value: float64(429)}}},
		Action:          Action{Verdict: VerdictCooldown},
	}
	if !e.VerifyRule(rule, Evidence{ProviderURL: "https://old.com/v1", StatusCode: 429}) {
		t.Fatal("expected hit for same provider")
	}
	if e.VerifyRule(rule, Evidence{ProviderURL: "https://other.com/v1", StatusCode: 429}) {
		t.Fatal("expected miss for other provider")
	}
}
