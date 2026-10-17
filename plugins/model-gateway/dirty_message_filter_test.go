package modelgateway

import (
	"encoding/json"
	"testing"
)

func mustParseMessages(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var root struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatalf("解析响应 body 失败: %v", err)
	}
	return root.Messages
}

// 脏 assistant（空 content + 空 tool name）及其 tool 结果被剥除，其余消息完整保留。
func TestStripBrokenAssistantMessages(t *testing.T) {
	body := []byte(`{
		"model": "step-5-preview",
		"messages": [
			{"role": "system", "content": "你是助手"},
			{"role": "user", "content": "帮我画个图"},
			{"role": "assistant", "content": "", "tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "canvas_ui_read", "arguments": "{}"}}]},
			{"role": "tool", "tool_call_id": "call_1", "content": "ok"},
			{"role": "assistant", "content": "", "tool_calls": [{"id": "call_bad", "type": "function", "function": {"name": "", "arguments": ""}}]},
			{"role": "tool", "tool_call_id": "call_bad", "content": "result"},
			{"role": "user", "content": "你好"}
		]
	}`)
	got, changed := stripBrokenAssistantMessages(body)
	if !changed {
		t.Fatal("包含残缺 assistant 消息时应报告 changed=true")
	}
	msgs := mustParseMessages(t, got)
	if len(msgs) != 5 {
		t.Fatalf("剥除后应剩 5 条消息，实际 %d 条: %v", len(msgs), msgs)
	}
	// 顺序与内容完整：system、user、正常 assistant+tool、user。
	roles := []string{}
	for _, m := range msgs {
		r, _ := m["role"].(string)
		roles = append(roles, r)
	}
	want := []string{"system", "user", "assistant", "tool", "user"}
	for i := range want {
		if roles[i] != want[i] {
			t.Fatalf("消息顺序不符: got %v want %v", roles, want)
		}
	}
	// 被剥的 call_bad 不得出现。
	for _, m := range msgs {
		if s, ok := m["tool_call_id"].(string); ok && s == "call_bad" {
			t.Fatal("引用被剥 tool_call_id 的 tool 结果应一并剥除")
		}
	}
}

// 干净请求：零改动，body 保持原样。
func TestStripBrokenAssistantMessagesCleanRequest(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"ok"}]}`)
	got, changed := stripBrokenAssistantMessages(body)
	if changed {
		t.Fatal("干净请求不应改动")
	}
	if string(got) != string(body) {
		t.Fatalf("干净请求 body 应原样返回，实际: %s", got)
	}
}

// 空 content 且无 tool_calls 的 assistant 也应剥除。
func TestStripBrokenAssistantNoToolCalls(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"a"},{"role":"assistant","content":""},{"role":"user","content":"b"}]}`)
	got, changed := stripBrokenAssistantMessages(body)
	if !changed {
		t.Fatal("空 assistant 应被剥除")
	}
	msgs := mustParseMessages(t, got)
	if len(msgs) != 2 {
		t.Fatalf("应剩 2 条消息，实际 %d", len(msgs))
	}
}

// content 有实质文本的正常 assistant 不剥；带分段数组的 content 视为非空。
func TestStripBrokenAssistantKeepsNormalContent(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","content":"正常回复"},{"role":"assistant","content":[{"type":"text","text":"分段"}]}]}`)
	if _, changed := stripBrokenAssistantMessages(body); changed {
		t.Fatal("正常 assistant 消息不应剥除")
	}
}
