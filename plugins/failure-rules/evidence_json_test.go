package failurerules

import (
	"encoding/json"
	"testing"
)

// TestEvidenceUnmarshalFromFrontendPayload 锁定一个真实踩过的坑：
// 编辑弹窗的「样本校验」把 {status_code, body_code, message} 发到后端，
// 而 Evidence 过去没有任何 json tag —— encoding/json 的大小写不敏感匹配
// 只救得了 message，救不了 status_code / body_code（下划线分隔的名字）。
// 结果：dry-run 里状态码永远是 0，用户填了完全正确的预测也一律显示「未命中」。
func TestEvidenceUnmarshalFromFrontendPayload(t *testing.T) {
	raw := `{"status_code":404,"body_code":"14018","message":"Route Not Found sdsd","model":"glm-5.2","channel_id":"c1","provider_url":"https://p.example/v2"}`
	var ev Evidence
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ev.StatusCode != 404 {
		t.Fatalf("status_code 应绑定到 StatusCode，实际 %d（json tag 缺失）", ev.StatusCode)
	}
	if ev.BodyCode != "14018" {
		t.Fatalf("body_code 应绑定到 BodyCode，实际 %q", ev.BodyCode)
	}
	if ev.Message != "Route Not Found sdsd" {
		t.Fatalf("message 绑定失败，实际 %q", ev.Message)
	}
	if ev.Model != "glm-5.2" || ev.ChannelID != "c1" {
		t.Fatalf("model/channel_id 绑定失败：%+v", ev)
	}
	if ev.ProviderURL != "https://p.example/v2" {
		t.Fatalf("provider_url 绑定失败，实际 %q", ev.ProviderURL)
	}
}

// TestVerifyRuleMatchOnlyHitsProviderScopedRule 复现用户截图里的「样本校验」：
// 规则锁了平台 + 状态码 404 + 文案包含 Route Not Found，
// 样本填「404 + Route Not Found sdsd」必须命中（用户说「我提供的明明都是对的」）。
func TestVerifyRuleMatchOnlyHitsProviderScopedRule(t *testing.T) {
	e := &Engine{}
	rule := Rule{
		ID: "ai-1", Enabled: true, Confirmed: true,
		ScopeMode: "urls", ProviderBaseURLs: []string{"https://copilot.tencent.com/v2"},
		Match: Match{All: []Condition{
			{Field: "status_code", Op: "eq", Value: 404},
			{Field: "message_text", Op: "contains", Value: "Route Not Found"},
		}},
		Action: Action{Verdict: VerdictDisableProvider, Recover: "never"},
	}
	// 走一遍与 HTTP 层完全相同的路径（decodeJSON 就是 json.Unmarshal）。
	var ev Evidence
	if err := json.Unmarshal([]byte(`{"status_code":404,"message":"Route Not Found sdsd"}`), &ev); err != nil {
		t.Fatalf("unmarshal sample: %v", err)
	}
	if !e.VerifyRuleMatchOnly(rule, ev) {
		t.Fatal("条件全中却判未命中：检查 Evidence 的 json tag 是否缺失")
	}
}
