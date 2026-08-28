package mcphub

import (
	"strings"
	"testing"
)

// TestParseSkillDescription 回归测试：mcp-hub 从 SKILL.md frontmatter 提取 description，
// 必须正确处理 YAML 多行 folded (`>`) / literal (`|`) scalar。
// 旧实现只匹配单行 `description:`，遇到 `description: >` 只返回 `>` 一个字符。
func TestParseSkillDescription(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			"folded multi-line",
			"---\nname: notify\ndescription: >\n  Use when the user wants to send a push notification\n  to their phone via WxPusher.\n---\n\n# notify\n",
			"Use when the user wants to send a push notification to their phone via WxPusher.",
		},
		{
			"single line quoted",
			"---\nname: foo\ndescription: \"A simple skill.\"\n---\n",
			"A simple skill.",
		},
		{
			"single line unquoted",
			"---\nname: foo\ndescription: Plain text description.\n---\n",
			"Plain text description.",
		},
		{
			"literal block",
			"---\nname: bar\ndescription: |\n  Line one.\n  Line two.\n---\n",
			"Line one.\nLine two.",
		},
		{
			"no description",
			"---\nname: baz\n---\n",
			"",
		},
		{
			"no frontmatter",
			"# Just a title\n\nNo frontmatter here.\n",
			"",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseSkillDescription(c.body)
			// folded scalar 的换行被折叠为空格，结果可能含空格差异，比较 trim 后
			gotNorm := strings.Join(strings.Fields(got), " ")
			wantNorm := strings.Join(strings.Fields(c.want), " ")
			if gotNorm != wantNorm {
				t.Errorf("parseSkillDescription()\n got: %q\nwant: %q", got, c.want)
			}
		})
	}
}