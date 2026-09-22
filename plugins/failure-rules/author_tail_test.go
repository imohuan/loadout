package failurerules

import "testing"

// TestStreamTailKeepsLastRunes 锁定用户明确要求的一项展示行为：
// 「我只需要最后的 10 个字符串就行了」。
//
// 之前这里误用了 truncate（保留**头部**），于是打字机效果在满 10 个字符后
// 就冻住了 —— 前端永远显示 AI 输出的第一句话，用户看不到生成在推进。
func TestStreamTailKeepsLastRunes(t *testing.T) {
	if got := streamTail("abcdefghijklmn", 10); got != "efghijklmn" {
		t.Fatalf("应保留最后 10 个字符，实际 %q", got)
	}
	if got := streamTail("短", 10); got != "短" {
		t.Fatalf("不足长度应原样返回，实际 %q", got)
	}
	// 按 rune 截断，不能切出乱码（中文 JSON 输出很常见）。
	if got := streamTail("规则名中文一二三四五六七八九十", 4); got != "七八九十" {
		t.Fatalf("中文应按字符取尾部，实际 %q", got)
	}
	if got := streamTail("abc", 0); got != "" {
		t.Fatalf("n<=0 应返回空，实际 %q", got)
	}
}
