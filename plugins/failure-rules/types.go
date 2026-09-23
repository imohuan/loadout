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
	// 捕获模板：正则条件里的捕获组 (…) 在命中后可用 $1/$2… 引用，
	// 动作字段里写模板即可引用捕获值（通用机制，不限于时间）。
	//
	// 例：正则 `retry after (\d+) seconds` 命中后，
	//   CooldownSecondsTemplate = "$1" → 冷却秒数取捕获值；
	//   RecoverAtTemplate       = "$1" → 冷却到捕获的「2026-09-23 15:48:27」。
	// 模板展开失败（无捕获/类型不符）时回退对应字段的静态值。
	// 模板展开结果为数字时是「秒数」；解析成时间成功时是「时刻」。
	CooldownSecondsTemplate string `json:"cooldown_seconds_template,omitempty"`
	RecoverAtTemplate       string `json:"recover_at_template,omitempty"`
}

// Rule 一条失败规则（DB 行的内存表示）。
type Rule struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Enabled         bool   `json:"enabled"`
	Source          string `json:"source"`    // manual | ai
	Confirmed       bool   `json:"confirmed"` // AI 草稿为 false
	Priority        int    `json:"priority"`
	ProviderBaseURL string `json:"provider_base_url"` // 单平台（scope_mode 空/urls 单值兼容旧数据）
	// 作用域模式（v36）："" / "all" = 全部平台；"urls" = 多选平台；"framework" = 按框架。
	ScopeMode         string   `json:"scope_mode,omitempty"`
	ProviderBaseURLs  []string `json:"provider_base_urls,omitempty"`
	ProviderFramework string   `json:"provider_framework,omitempty"`
	Model             string   `json:"model"` // 空 = 全模型
	Match             Match    `json:"match"`
	Action            Action   `json:"action"`
	HitCount          int64    `json:"hit_count"`
	LastHitAt         string   `json:"last_hit_at,omitempty"`
	CreatedAt         string   `json:"created_at"`
	UpdatedAt         string   `json:"updated_at"`
}

// Evidence 一次失败的证据（求值输入）。
//
// 必须带 json tag：这份结构会被 admin-api 直接从请求体反序列化
// （编辑弹窗的「样本校验」body 是 {status_code, body_code, message}）。
// 没有 tag 时 encoding/json 的大小写不敏感匹配只救得了 Message，
// status_code / body_code 这种带下划线的键会静默落到零值 —— 用户填了
// 完全正确的预测，dry-run 也一律显示「未命中」（实测踩过）。
type Evidence struct {
	RequestID         string `json:"request_id,omitempty"`
	Model             string `json:"model,omitempty"`
	ChannelID         string `json:"channel_id,omitempty"`
	ChannelName       string `json:"channel_name,omitempty"`       // Key 名（账号标识，如手机号；用于日志展示「平台+Key+模型」）
	ProviderURL       string `json:"provider_url,omitempty"`       // 渠道 base_url（组身份）
	ProviderFramework string `json:"provider_framework,omitempty"` // 渠道框架标签（newapi/one-api/…；空 = 自定义）
	StatusCode        int    `json:"status_code,omitempty"`
	BodyCode          string `json:"body_code,omitempty"`  // 错误体中的业务码（如 14018）
	Message           string `json:"message,omitempty"`    // error 文本 + error_body 合并（截断）
	FailCount         int    `json:"fail_count,omitempty"` // 该 key 连续失败次数（RecordSuccess 清零）
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
	// Captures 正则条件的捕获组（$0=整个匹配，$1..=子组）。
	// 供动作模板（cooldown_seconds_template / recover_at_template）展开用，
	// 不出 JSON——只在引擎 → 执行器的内存链路里传递。
	Captures []string `json:"-"`
}

// routeScope 作用域命中判断（引擎内部用）。
func (r *Rule) scopeMatches(ev Evidence) bool {
	// 作用域模式（v36）：urls = 多选平台；framework = 按框架；其余 = 全部或单平台。
	switch r.ScopeMode {
	case "urls":
		// 多选平台：ProviderBaseURLs 任一命中；空列表 = 无限制。
		if len(r.ProviderBaseURLs) > 0 {
			hit := false
			for _, u := range r.ProviderBaseURLs {
				if u != "" && u == ev.ProviderURL {
					hit = true
					break
				}
			}
			if !hit {
				return false
			}
		}
	case "framework":
		// 按框架：渠道 framework 标签匹配（同框架平台共用规则）。
		if r.ProviderFramework != "" && r.ProviderFramework != ev.ProviderFramework {
			return false
		}
	default:
		// 兼容旧数据：单平台 base_url 精确匹配。
		if r.ProviderBaseURL != "" && r.ProviderBaseURL != ev.ProviderURL {
			return false
		}
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
