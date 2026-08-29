package translate

import (
	"strings"
	"testing"
)

func TestSplitChunks(t *testing.T) {
	input := "First sentence. Second sentence!\n\nAnother paragraph? It has multiple sentences. And more.\n\nThird para done."
	chunks := splitChunks(input)
	if len(chunks) == 0 {
		t.Fatal("expected chunks")
	}
	// 验证切分结果：每块非空
	for i, c := range chunks {
		if strings.TrimSpace(c.Text) == "" {
			t.Fatalf("chunk %d empty", i)
		}
	}
	// 空行分隔应产生独立大段
	joined := ""
	for _, c := range chunks {
		joined += c.Text + "|"
	}
	if !strings.Contains(joined, "Another paragraph?") {
		t.Fatalf("expected paragraph 2 preserved, got %q", joined)
	}
}

func TestHashTextStable(t *testing.T) {
	a := hashText("Hello world")
	b := hashText("Hello world")
	if a != b {
		t.Fatalf("hash not stable: %s != %s", a, b)
	}
	// 改一个字符 hash 必变
	c := hashText("Hello world!")
	if a == c {
		t.Fatalf("hash should change on content change")
	}
}

func TestSkipTranslation(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"", true},
		{"https://example.com", true},
		{"12345", true},
		{"3.14%", true},
		{"Hello, this is English text. Please translate.", false},
	}
	for _, c := range cases {
		if got := skipTranslation(c.text, "zh-CN"); got != c.want {
			t.Errorf("skipTranslation(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestParseNumberedTranslations(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"[1] 你好\n[2] 世界\n[3] 你好，世界"}}]}`)
	got, err := parseNumberedTranslations(body, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"你好", "世界", "你好，世界"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("item %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseIndexPrefix(t *testing.T) {
	if got := parseIndexPrefix("[1] hello"); got != 0 {
		t.Errorf("parseIndexPrefix([1]) = %d, want 0", got)
	}
	if got := parseIndexPrefix("2. world"); got != 1 {
		t.Errorf("parseIndexPrefix(2.) = %d, want 1", got)
	}
	if got := parseIndexPrefix("no prefix"); got != -1 {
		t.Errorf("parseIndexPrefix(no prefix) = %d, want -1", got)
	}
}

func TestTrimPrefix(t *testing.T) {
	if got := trimPrefix("[1] hello"); got != "hello" {
		t.Errorf("trimPrefix = %q, want hello", got)
	}
	if got := trimPrefix("3. hi"); got != "hi" {
		t.Errorf("trimPrefix = %q, want hi", got)
	}
	if got := trimPrefix("plain"); got != "plain" {
		t.Errorf("trimPrefix = %q, want plain", got)
	}
}

// TestIsPlaceholder 回归测试：占位符判断必须精确，不能因缺少句末标点把英文短语误判为占位符。
// 此前 isPlaceholder 用「不含 。！？.!? 即视为占位符」，导致 "Repository owner" 这类
// 英文描述/标题被跳过、不发翻译请求（用户截图里 owner/repo 的 title 没翻译、也没请求日志）。
func TestIsPlaceholder(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		// 真正的占位符/代码 → true（应跳过翻译）
		{"{{name}}", true},
		{"{user_id}", true},
		{"${var}", true},
		{"$owner", true},
		{"<br/>", true},
		{"%s", true},
		{"%.2f", true},
		{"", true},
		{"user_id", true},
		{"foo.bar.baz", true},
		{"foo-bar", true},
		{"path/to/file", true},

		// 英文自然语言短语（带空格）→ false（必须翻译）
		{"Repository owner", false},
		{"Repository name", false},
		{"Add a comment", false},
		{"The numeric ID of the issue", false},
	}
	for _, c := range cases {
		if got := isPlaceholder(c.text); got != c.want {
			t.Errorf("isPlaceholder(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}
