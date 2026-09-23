package failurerules

import "testing"

// TestDefaultRulesPassValidation 回归：内置默认规则必须全部通过保存校验。
//
// 「恢复默认规则」走的是 Store.CreateBuiltin → validateInput。如果哪天给默认规则
// 加了校验不允许的字段/操作，用户一点「恢复默认规则」就会整体报错，
// 这个测试把这种联动挡住。
func TestDefaultRulesPassValidation(t *testing.T) {
	for _, in := range DefaultRules() {
		if err := validateInput(in); err != nil {
			t.Fatalf("内置默认规则「%s」未通过校验：%v", in.Name, err)
		}
	}
}

// TestDefaultRuleIDsMatchOrder 锁定一个真实踩过的坑：
// RestoreDefaults 按**下标**把 DefaultRules()[i] 与 DefaultRuleIDs[i] 对位 upsert。
// 我在中间插入 seed-015 时只改了 IDs 列表，没动 DefaultRules() 的条目顺序——
// 结果 ID 与内容整体错位一格：「限速（2分钟）」被写进 seed-015、
// 「限速带重置时间」被写进 seed-005，恢复默认后所有相关规则全部张冠李戴。
//
// 正确约定：DefaultRules() 的第 i 项必须对应 DefaultRuleIDs[i]，
// 且名字里的编号（若有）应与 ID 一致。这里按「内容指纹」反查每个 seed ID
// 应有的名字，逐一对位验证。
func TestDefaultRuleIDsMatchOrder(t *testing.T) {
	wantNames := map[string]string{
		"seed-001": "客户端取消请求（不记失败）",
		"seed-002": "每日额度用尽（次日恢复）",
		"seed-003": "账户余额不足（禁用key）",
		"seed-004": "无效API密钥（禁用key并连坐渠道）",
		"seed-013": "内容未过安全审核（禁用当前Key）",
		"seed-015": "限速带重置时间（按文案时刻恢复）",
		"seed-005": "限速（冷却2分钟，连续5次升级为次日恢复）",
		"seed-014": "平台不支持该模型（禁用该模型）",
		"seed-006": "服务过载（冷却1分钟）",
		"seed-007": "上下文超长（跳过该模型）",
		"seed-008": "网络超时（冷却30秒）",
		"seed-009": "模型不存在（禁用该模型）",
		"seed-010": "客户端参数错误（忽略）",
		"seed-011": "连接失败（忽略）",
		"seed-012": "EOF连接中断（忽略）",
	}
	if len(wantNames) != len(DefaultRules()) {
		t.Fatalf("wantNames %d 条 vs DefaultRules %d 条：新增默认规则时必须同步两边",
			len(wantNames), len(DefaultRules()))
	}
	for i, in := range DefaultRules() {
		id := DefaultRuleIDs[i]
		want, ok := wantNames[id]
		if !ok {
			t.Fatalf("DefaultRuleIDs[%d]=%s 不在已知清单里", i, id)
		}
		if in.Name != want {
			t.Fatalf("错位：DefaultRules()[%d] 是「%s」，但 ID %s 应为「%s」",
				i, in.Name, id, want)
		}
	}
}
