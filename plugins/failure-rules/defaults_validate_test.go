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
