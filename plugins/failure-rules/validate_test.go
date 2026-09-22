package failurerules

import (
	"errors"
	"strings"
	"testing"
)

// TestValidateInputRejectsDeadConditions 锁定「配了却永远不命中」的条件要在保存时
// 就被挡住。用户在界面上很容易配出这类死条件，线上却表现为「规则没生效」，
// 排查起来非常费劲。
func TestValidateInputRejectsDeadConditions(t *testing.T) {
	base := func(conds ...Condition) RuleInput {
		return RuleInput{
			Name:   "t",
			Match:  Match{Any: conds},
			Action: Action{Verdict: VerdictIgnore},
		}
	}
	cases := []struct {
		name    string
		in      RuleInput
		wantErr string
	}{
		{"空匹配值", base(Condition{Field: "message_text", Op: "contains", Value: ""}), "匹配值不能为空"},
		{"非法正则", base(Condition{Field: "message_regex", Op: "regex", Value: "([unclosed"}), "正则不合法"},
		{"状态码配正则", base(Condition{Field: "status_code", Op: "regex", Value: "429"}), "「正则」只能用于错误文案"},
		{"业务码配正则", base(Condition{Field: "body_code", Op: "regex", Value: "14018"}), "「正则」只能用于错误文案"},
		{"状态码非数字", base(Condition{Field: "status_code", Op: "eq", Value: "abc"}), "状态码必须是数字"},
		{"未知字段", base(Condition{Field: "nope", Op: "eq", Value: "x"}), "未知的字段"},
		{"未知操作", base(Condition{Field: "message_text", Op: "weird", Value: "x"}), "未知的操作"},
		{"合法：文案包含", base(Condition{Field: "message_text", Op: "contains", Value: "额度"}), ""},
		{"合法：文案正则", base(Condition{Field: "message_text", Op: "regex", Value: "额度.*用尽"}), ""},
		{"合法：旧正则字段", base(Condition{Field: "message_regex", Op: "regex", Value: "额度.*用尽"}), ""},
		{"合法：状态码等于", base(Condition{Field: "status_code", Op: "eq", Value: 429}), ""},
	}
	for _, c := range cases {
		err := validateInput(c.in)
		if c.wantErr == "" {
			if err != nil {
				t.Fatalf("%s：应通过校验，实际报错 %v", c.name, err)
			}
			continue
		}
		if err == nil {
			t.Fatalf("%s：应报错（%s），实际通过了校验", c.name, c.wantErr)
		}
		// 必须是「用户输入问题」型错误：HTTP 层据此回 400 而不是 500。
		var invalid ErrInvalidRule
		if !errors.As(err, &invalid) {
			t.Fatalf("%s：应返回 ErrInvalidRule（以便 HTTP 层回 400），实际 %T", c.name, err)
		}
		if !strings.Contains(err.Error(), c.wantErr) {
			t.Fatalf("%s：错误信息应包含 %q，实际 %q", c.name, c.wantErr, err.Error())
		}
	}
}
