// Package failurerules 实现失败规则引擎：可编辑的匹配规则 + AI 兜底判定，
// 统一聚合/非聚合两条失败路径的禁用/冷却裁决。
//
// 一条规则 = 作用域 + 匹配条件（any/all）+ 动作（verdict + 恢复策略）。
// 时间限定的禁用写 status='cooling' + disabled_until（复用 CheckNow 过期
// 恢复语义）；status='disabled' 仅永久禁用（recover=never）。
package failurerules

import "encoding/json"

// Condition 单条匹配条件。
type Condition struct {
	Field string `json:"field"` // status_code | body_code | message_text | message_regex
	Op    string `json:"op"`    // eq | contains | not_contains | regex
	Value any    `json:"value"` // number(status_code) | string(其它)
}

// Match 匹配表达式：any（任一命中）或 all（全部命中），二选一。
type Match struct {
	Any []Condition `json:"any,omitempty"`
	All []Condition `json:"all,omitempty"`
}

// Action 规则命中后的动作。
type Action struct {
	Verdict string `json:"verdict"` // disable_key|disable_model|disable_provider|cooldown|ignore|retry_same|switch_next
	// 恢复策略（disable_*/cooldown 时有效）：
	//   never  = 永久禁用（status='disabled'）
	//   daily  = 次日 daily_reset_hour 点恢复（status='cooling'）
	//   fixed  = now + cooldown_seconds 恢复（status='cooling'）
	Recover         string `json:"recover,omitempty"`
	CooldownSeconds int    `json:"cooldown_seconds,omitempty"`
	DailyResetHour  int    `json:"daily_reset_hour,omitempty"`
	// 连坐：disable_key 时是否同时禁用整个渠道组（auth 永久禁用场景）。
	SwitchAccount bool `json:"switch_account,omitempty"`
	// 限速升级：连续失败 FailUpgradeCount 次后升级为 FailUpgradeRecover 策略。
	FailUpgradeCount   int    `json:"fail_upgrade_count,omitempty"`
	FailUpgradeRecover string `json:"fail_upgrade_recover,omitempty"`
}

// Rule 一条失败规则（DB 行的内存表示）。
type Rule struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Enabled         bool   `json:"enabled"`
	Source          string `json:"source"`    // manual | ai
	Confirmed       bool   `json:"confirmed"` // AI 草稿为 false
	Priority        int    `json:"priority"`
	ProviderBaseURL string `json:"provider_base_url"` // 空 = 全平台
	Model           string `json:"model"`             // 空 = 全模型
	Match           Match  `json:"match"`
	Action          Action `json:"action"`
	HitCount        int64  `json:"hit_count"`
	LastHitAt       string `json:"last_hit_at,omitempty"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

// Evidence 一次失败的证据（求值输入）。
type Evidence struct {
	RequestID   string
	Model       string
	ChannelID   string
	ProviderURL string // 渠道 base_url（组身份）
	StatusCode  int
	BodyCode    string // 错误体中的业务码（如 14018）
	Message     string // error 文本 + error_body 合并（截断）
	FailCount   int    // 该 key 连续失败次数（RecordSuccess 清零）
}

// Verdict 常量。
const (
	VerdictDisableKey      = "disable_key"
	VerdictDisableModel    = "disable_model"
	VerdictDisableProvider = "disable_provider"
	VerdictCooldown        = "cooldown"
	VerdictIgnore          = "ignore"
	VerdictRetrySame       = "retry_same"
	VerdictSwitchNext      = "switch_next"
)

// Decision 引擎裁决结果。
type Decision struct {
	MatchedRuleID   string `json:"matched_rule_id,omitempty"`
	MatchedRuleName string `json:"matched_rule_name,omitempty"`
	Verdict         string `json:"verdict"`
	Reason          string `json:"reason,omitempty"`
	AIModel         string `json:"ai_model,omitempty"` // verdict 来自 AI 兜底时非空
	AIRaw           string `json:"ai_raw,omitempty"`
	Action          Action `json:"-"` // 命中规则的完整动作配置（内部传递）
}

// routeScope 作用域命中判断（引擎内部用）。
func (r *Rule) scopeMatches(ev Evidence) bool {
	if r.ProviderBaseURL != "" && r.ProviderBaseURL != ev.ProviderURL {
		return false
	}
	if r.Model != "" && r.Model != ev.Model {
		return false
	}
	return true
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
