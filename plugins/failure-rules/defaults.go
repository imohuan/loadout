package failurerules

// DefaultRules 内置默认规则（唯一来源）。迁移 v35/v39 用 SQL 写入同样的内容；
// 这里供「恢复默认规则」接口在运行时重放——用户删掉或改坏某条默认规则后，
// 可以一键回到出厂状态（用户自建的 rule-*/ai-* 规则不受影响）。
//
// 设计约束：ID 固定为 seed-001..seed-014，顺序即优先级；
// 恢复时按 ID upsert（覆盖 name/enabled/priority/match/action），不新增重复行。
func DefaultRules() []RuleInput {
	return []RuleInput{
		{
			Name: "客户端取消请求（不记失败）", Priority: 10,
			Match:  Match{Any: []Condition{{Field: "message_text", Op: "contains", Value: "context canceled"}}},
			Action: Action{Verdict: VerdictIgnore},
		},
		{
			Name: "每日额度用尽（次日恢复）", Priority: 20,
			Match: Match{Any: []Condition{
				{Field: "body_code", Op: "eq", Value: "14018"},
				{Field: "message_text", Op: "contains", Value: "额度已用尽"},
				{Field: "message_text", Op: "contains", Value: "quota exhausted"},
			}},
			Action: Action{Verdict: VerdictDisableKey, Recover: "daily", DailyResetHour: 12},
		},
		{
			Name: "账户余额不足（禁用key）", Priority: 30,
			Match:  Match{Any: []Condition{{Field: "status_code", Op: "eq", Value: 402}}},
			Action: Action{Verdict: VerdictDisableKey, Recover: "never"},
		},
		{
			Name: "无效API密钥（禁用key并连坐渠道）", Priority: 40,
			Match: Match{Any: []Condition{
				{Field: "status_code", Op: "eq", Value: 401},
				{Field: "message_text", Op: "contains", Value: "invalid api key"},
			}},
			Action: Action{Verdict: VerdictDisableKey, Recover: "never", SwitchAccount: true},
		},
		{
			// 403 + 11140「request illegal / 内容未通过安全审核」：
			//
			// 语义修正（用户实测）：这不是「请求内容有问题、换个模型就好」，
			// 而是**该 Key 已被平台风控标记**——继续用它只会一直 403。
			// 所以禁用当前 Key（recover=never：风控要人工申诉/换号，不会自愈）。
			// 原来判 ignore 会导致这条 Key 持续被选中反复失败。
			Name: "内容未过安全审核（禁用当前Key）", Priority: 45,
			Match: Match{All: []Condition{
				{Field: "status_code", Op: "eq", Value: 403},
				{Field: "body_code", Op: "eq", Value: "11140"},
			}},
			Action: Action{Verdict: VerdictDisableKey, Recover: "never"},
		},
		{
			Name: "限速（冷却2分钟，连续5次升级为次日恢复）", Priority: 50,
			Match: Match{All: []Condition{
				{Field: "status_code", Op: "eq", Value: 429},
				{Field: "message_text", Op: "not_contains", Value: "额度"},
			}},
			Action: Action{Verdict: VerdictCooldown, CooldownSeconds: 120, Recover: "fixed", FailUpgradeCount: 5, FailUpgradeRecover: "daily"},
		},
		{
			// 限速且文案带「重置时刻」（如腾讯 copilot code 6004：
			// 「您的使用量已超出频率限制，将在 2026-09-23 15:48:27 UTC+8 重置」）：
			// 正则捕获组抓出时间文本，动作里用 $1 引用（recover_at_template），
			// 冷却到上游自己说的那个点，而不是拍脑袋 2 分钟——用户实测反馈：
			// 文案里明明给了时间，旧规则却只冷却 2 分钟、5 次后又升级成 1 天。
			// 优先级比通用限速更高（49 < 50）。这是**通用捕获模板**的一个实例，
			// 任何数值/时间/文本都能用同样的方式提取。
			Name: "限速带重置时间（按文案时刻恢复）", Priority: 49,
			Match: Match{All: []Condition{
				{Field: "status_code", Op: "eq", Value: 429},
				{Field: "message_text", Op: "regex", Value: "20\\d{2}[-/]\\d{2}[-/]\\d{2}[ T]\\d{1,2}:\\d{2}:\\d{2}"},
			}},
			Action: Action{Verdict: VerdictCooldown, Recover: "fixed", RecoverAtTemplate: "$0"},
		},
		{
			// 平台没有这个模型：只禁该模型（其余模型照用），不要连坐整个 Key。
			Name: "平台不支持该模型（禁用该模型）", Priority: 55,
			Match:  Match{All: []Condition{{Field: "body_code", Op: "eq", Value: "11102"}}},
			Action: Action{Verdict: VerdictDisableModel, Recover: "never"},
		},
		{
			Name: "服务过载（冷却1分钟）", Priority: 60,
			Match: Match{Any: []Condition{
				{Field: "status_code", Op: "eq", Value: 503},
				{Field: "message_text", Op: "contains", Value: "overloaded"},
			}},
			Action: Action{Verdict: VerdictCooldown, CooldownSeconds: 60, Recover: "fixed"},
		},
		{
			Name: "上下文超长（跳过该模型）", Priority: 70,
			Match: Match{Any: []Condition{
				{Field: "message_text", Op: "contains", Value: "context length"},
				{Field: "message_text", Op: "contains", Value: "maximum context"},
			}},
			Action: Action{Verdict: VerdictSwitchNext},
		},
		{
			Name: "网络超时（冷却30秒）", Priority: 80,
			Match: Match{Any: []Condition{
				{Field: "message_text", Op: "contains", Value: "timeout"},
				{Field: "message_text", Op: "contains", Value: "connection reset"},
			}},
			Action: Action{Verdict: VerdictCooldown, CooldownSeconds: 30, Recover: "fixed"},
		},
		{
			Name: "模型不存在（禁用该模型）", Priority: 90,
			Match: Match{All: []Condition{
				{Field: "status_code", Op: "eq", Value: 404},
				{Field: "message_text", Op: "contains", Value: "model"},
			}},
			Action: Action{Verdict: VerdictDisableModel, Recover: "never"},
		},
		{
			// 403/404 已有专属规则；这里只兜 400/405 这类纯客户端参数错误。
			Name: "客户端参数错误（忽略）", Priority: 100,
			Match: Match{Any: []Condition{
				{Field: "status_code", Op: "eq", Value: 400},
				{Field: "status_code", Op: "eq", Value: 405},
			}},
			Action: Action{Verdict: VerdictIgnore},
		},
		{
			Name: "连接失败（忽略）", Priority: 110,
			Match: Match{Any: []Condition{
				{Field: "message_text", Op: "contains", Value: "no such host"},
				{Field: "message_text", Op: "contains", Value: "connection refused"},
				{Field: "message_text", Op: "contains", Value: "no route to host"},
				{Field: "message_text", Op: "contains", Value: "dial tcp"},
				{Field: "message_text", Op: "contains", Value: "lookup"},
			}},
			Action: Action{Verdict: VerdictIgnore},
		},
		{
			Name: "EOF连接中断（忽略）", Priority: 120,
			Match:  Match{Any: []Condition{{Field: "message_text", Op: "contains", Value: "eof"}}},
			Action: Action{Verdict: VerdictIgnore},
		},
	}
}

// DefaultRuleIDs 默认规则的固定 ID，**必须与 DefaultRules() 返回顺序一一对应**
// （seed-013/014 是按优先级 45/55 插进中间的两条实测新增规则）。
var DefaultRuleIDs = []string{
	"seed-001", "seed-002", "seed-003", "seed-004", "seed-013", "seed-015", "seed-005", "seed-014",
	"seed-006", "seed-007", "seed-008", "seed-009", "seed-010", "seed-011", "seed-012",
}
