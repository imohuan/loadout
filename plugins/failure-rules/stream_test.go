package failurerules

import (
	"strings"
	"testing"
)

// sseJSON 把若干 JSON 片段拼成 OpenAI 风格的 SSE 响应体。
func sseJSON(chunks ...string) string {
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString("data: ")
		b.WriteString(c)
		b.WriteString("\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

// TestStreamCapturesReasoningAndContent 锁定用户反馈的「实时进度不显示」问题。
//
// 实测上游（glm-5.3-flash 等推理模型）把「思考」放在 delta.reasoning_content、
// 把正文放在 delta.content，两者是**分开的两路流**（实测 866 个思考增量 vs
// 217 个正文增量）。旧解析只看 delta.content，于是整段思考期间预览一直是空的，
// 用户以为卡死了。
//
// 这里要求：思考与正文的增量都要实时回调；返回值（用于解析规则 JSON）
// 只包含正文——把思考混进去会让 json.Unmarshal 解析规则失败。
func TestStreamCapturesReasoningAndContent(t *testing.T) {
	body := sseJSON(
		`{"choices":[{"delta":{"role":"assistant","content":""},"index":0}]}`,
		`{"choices":[{"delta":{"reasoning_content":"用户"}}]}`,
		`{"choices":[{"delta":{"reasoning_content":"在问"}}]}`,
		`{"choices":[{"delta":{"content":"name"}}]}`,
		`{"choices":[{"delta":{"content":"-x"}}]}`,
	)
	var seen []string
	kinds := map[string]int{}
	content, reasoning, err := collectStreamDeltas(strings.NewReader(body), func(kind, s string) {
		seen = append(seen, s)
		kinds[kind]++
	})
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	// 思考与正文要带上各自的类型标签，前端才能显示「思考 / 文本」。
	if kinds[StreamKindReasoning] == 0 {
		t.Fatalf("未收到 reasoning 类型增量，实际 %v", kinds)
	}
	if kinds[StreamKindContent] == 0 {
		t.Fatalf("未收到 content 类型增量，实际 %v", kinds)
	}
	// 思考增量必须被回调出来（这是「实时看到思考」的关键）。
	joined := strings.Join(seen, "")
	if !strings.Contains(joined, "用户") || !strings.Contains(joined, "在问") {
		t.Fatalf("思考增量未回调，实际收到 %q", joined)
	}
	if !strings.Contains(joined, "name") {
		t.Fatalf("正文增量未回调，实际收到 %q", joined)
	}
	if reasoning != "用户在问" {
		t.Fatalf("思考应单独拼接，实际 %q", reasoning)
	}
	// 返回值只含正文：混入思考会让上层 json.Unmarshal 解析规则失败。
	if content != "name-x" {
		t.Fatalf("返回值应只含正文，实际 %q", content)
	}
}

// TestStreamFallsBackToReasoningWhenContentEmpty 覆盖「模型把答案全写在思考里」
// 的退化情况：正文为空时必须回退用思考内容，否则这一轮等于白跑。
func TestStreamFallsBackToReasoningWhenContentEmpty(t *testing.T) {
	body := sseJSON(
		`{"choices":[{"delta":{"reasoning_content":"only-reasoning-answer"}}]}`,
	)
	raw, reasoning, err := collectStreamDeltas(strings.NewReader(body), nil)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	content := finalizeStreamText(raw, reasoning)
	if content != "only-reasoning-answer" {
		t.Fatalf("正文为空时应回退用思考内容，实际 %q", content)
	}
}

// TestStreamToleratesNullAndNoise 心跳、空行、非 JSON 行、delta=null 都不能让解析崩。
func TestStreamToleratesNullAndNoise(t *testing.T) {
	body := ": keep-alive\n\ndata: not-json\n\n" +
		"data: {\"a\":1}\n\n" +
		"data: {\"choices\":[]}\n\n" +
		"data: {\"choices\":[{\"delta\":null}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"
	content, _, err := collectStreamDeltas(strings.NewReader(body), nil)
	if err != nil {
		t.Fatalf("噪音行不应导致错误：%v", err)
	}
	if content != "ok" {
		t.Fatalf("应只取到 ok，实际 %q", content)
	}
}
