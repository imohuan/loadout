package failurerules

import (
	"strings"
	"testing"
)

// TestDraftAuthorNameIsConcise 锁定用户对 AI 规则名的三项要求：
//  1. 简洁明了，一句话概括问题原因；
//  2. 不携带模型名 / 平台名；
//  3. 不带「AI:」前缀。
//
// 旧格式是「AI: <名> (glm-5.2 http404)」——前缀和括号里的模型/协议信息
// 都是冗余的：作用域已经记录了平台，优先级和来源字段已经记录了这是 AI 规则，
// 名字本身应该只回答「这条规则在处理什么问题」。
func TestDraftAuthorNameIsConcise(t *testing.T) {
	sm := Sample{ID: "s1", StatusCode: 502, Model: "gpt-5.6-terra", ProviderBaseURL: "https://pixelstarrysky.xyz/v1"}

	// AI 给了名字：原样使用（AI 自己概括原因），不加前缀不加后缀。
	if got := draftAuthorName(authorDraftSchema{Name: "上游服务异常"}, sm); got != "上游服务异常" {
		t.Fatalf("AI 给名时应原样使用，实际 %q", got)
	}

	// AI 没给名字：兜底名也只概括问题，不带模型/平台/前缀。
	got := draftAuthorName(authorDraftSchema{}, sm)
	if !strings.Contains(got, "502") {
		t.Fatalf("兜底名应包含状态码，实际 %q", got)
	}
	for _, bad := range []string{"AI:", "gpt-5.6-terra", "pixelstarrysky"} {
		if strings.Contains(got, bad) {
			t.Fatalf("兜底名不应包含 %q，实际 %q", bad, got)
		}
	}
}

// TestDraftAuthorNameTruncatesLongName AI 名字过长时截断（防表格被撑爆）。
func TestDraftAuthorNameTruncatesLongName(t *testing.T) {
	sm := Sample{StatusCode: 429}
	long := strings.Repeat("很长的名字", 30)
	got := draftAuthorName(authorDraftSchema{Name: long}, sm)
	if len([]rune(got)) > 40 {
		t.Fatalf("名字应截断到 40 字以内，实际 %d 字：%q", len([]rune(got)), got)
	}
}
