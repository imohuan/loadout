package modelgateway

import (
	"encoding/json"
	"testing"
)

// msgJSON 便捷构造一条消息。
func msgJSON(role, content string, toolCalls []map[string]any, toolCallID string) json.RawMessage {
	m := map[string]any{"role": role}
	if content != "" {
		m["content"] = content
	}
	if len(toolCalls) > 0 {
		m["tool_calls"] = toolCalls
	}
	if toolCallID != "" {
		m["tool_call_id"] = toolCallID
	}
	b, _ := json.Marshal(m)
	return b
}

func tc(id string) map[string]any {
	return map[string]any{
		"id":       id,
		"type":     "function",
		"function": map[string]any{"name": "read_file", "arguments": `{"file_path":"a.txt"}`},
	}
}

// roles 提取修复后 messages 的 role 序列，便于断言。
func rolesOf(body []byte) []string {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil
	}
	var msgs []map[string]any
	if err := json.Unmarshal(root["messages"], &msgs); err != nil {
		return nil
	}
	var out []string
	for _, m := range msgs {
		r, _ := m["role"].(string)
		out = append(out, r)
	}
	return out
}

func TestRepairToolCallSequence_NoChange(t *testing.T) {
	cases := []struct {
		name string
		body []byte
	}{
		{"normal seq", []byte(`{"model":"m","messages":[
			{"role":"system","content":"sys"},
			{"role":"user","content":"hi"},
			{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}},{"id":"c2","type":"function","function":{"name":"f","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"c1","content":"r1"},
			{"role":"tool","tool_call_id":"c2","content":"r2"},
			{"role":"assistant","content":"done"}
		]}`)},
		{"no messages", []byte(`{"model":"m"}`)},
		{"not json object", []byte(`notjson`)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, changed := repairToolCallSequence(c.body)
			if changed {
				t.Fatalf("expected no change, got changed=true out=%s", out)
			}
		})
	}
}

func TestRepairToolCallSequence_NoChange_NonChatEndpoint(t *testing.T) {
	// 非 chat/completions 端点由调用方 gate 拦截，但函数本身也应对无 messages 的 body 无副作用。
	out, changed := repairToolCallSequence([]byte(`{"input":[]}`))
	if changed {
		t.Fatalf("expected no change, got %s", out)
	}
}

func TestRepairToolCallSequence_MissingResults(t *testing.T) {
	// assistant 声明了 c1、c2 两个 tool_call，但只有 c1 的结果，c2 缺失。
	body := []byte(`{"model":"m","messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}},{"id":"c2","type":"function","function":{"name":"f","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"r1"},
		{"role":"assistant","content":"ok"}
	]}`)
	out, changed := repairToolCallSequence(body)
	if !changed {
		t.Fatalf("expected change")
	}
	want := []string{"user", "assistant", "tool", "tool", "assistant"}
	got := rolesOf(out)
	if len(got) != len(want) {
		t.Fatalf("roles = %v, want %v (out=%s)", got, want, out)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("roles[%d] = %q, want %q (out=%s)", i, got[i], want[i], out)
		}
	}
}

func TestRepairToolCallSequence_OrphanResult(t *testing.T) {
	// role=tool 结果缺少前置 assistant 声明（孤立结果）。
	body := []byte(`{"model":"m","messages":[
		{"role":"user","content":"hi"},
		{"role":"tool","tool_call_id":"c1","content":"r1"},
		{"role":"assistant","content":"done"}
	]}`)
	out, changed := repairToolCallSequence(body)
	if !changed {
		t.Fatalf("expected change")
	}
	want := []string{"user", "assistant", "tool", "assistant"}
	got := rolesOf(out)
	if len(got) != len(want) {
		t.Fatalf("roles = %v, want %v (out=%s)", got, want, out)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("roles[%d] = %q, want %q (out=%s)", i, got[i], want[i], out)
		}
	}
}

func TestRepairToolCallSequence_TrailingDecl(t *testing.T) {
	// 序列以 assistant 的 tool_calls 结尾，无任何结果。
	body := []byte(`{"model":"m","messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]}
	]}`)
	out, changed := repairToolCallSequence(body)
	if !changed {
		t.Fatalf("expected change")
	}
	got := rolesOf(out)
	want := []string{"user", "assistant", "tool"}
	if len(got) != len(want) {
		t.Fatalf("roles = %v, want %v (out=%s)", got, want, out)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("roles[%d] = %q, want %q (out=%s)", i, got[i], want[i], out)
		}
	}
}

func TestRepairToolCallSequence_OrphanResultNoID(t *testing.T) {
	// 孤立结果连 id 都没有：无法归属，透传不新增数据，但仍按普通消息保留。
	body := []byte(`{"model":"m","messages":[
		{"role":"user","content":"hi"},
		{"role":"tool","content":"r1"},
		{"role":"assistant","content":"done"}
	]}`)
	out, changed := repairToolCallSequence(body)
	if changed {
		t.Fatalf("expected no structural change for id-less orphan, changed=true out=%s", out)
	}
}

func TestRepairToolCallSequence_EndsWithToolResult(t *testing.T) {
	// 序列以 role=tool 结尾（正常 OpenAI 历史也允许，末尾结果后无 assistant 回复）。
	// 这种不应被误判为"孤立"，因为其前置有 assistant 声明。
	body := []byte(`{"model":"m","messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"r1"}
	]}`)
	out, changed := repairToolCallSequence(body)
	if changed {
		t.Fatalf("expected no change for valid trailing result, changed=true out=%s", out)
	}
}

func TestRepairToolCallSequence_ConsecutiveAssistant(t *testing.T) {
	// 连续两条 assistant：前一条是普通回复，后一条带 tool_calls。上游（方舟）严格禁止
	// 相邻 assistant，返回 400001 参数拒绝。应合并为一条（content 拼接、tool_calls 保留）。
	body := []byte(`{"model":"m","messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"first"},
		{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"r1"}
	]}`)
	out, changed := repairToolCallSequence(body)
	if !changed {
		t.Fatalf("expected change")
	}
	want := []string{"user", "assistant", "tool"}
	got := rolesOf(out)
	if len(got) != len(want) {
		t.Fatalf("roles = %v, want %v (out=%s)", got, want, out)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("roles[%d] = %q, want %q (out=%s)", i, got[i], want[i], out)
		}
	}
	// 合并后的 assistant 应同时包含 content 和 tool_calls。
	var root map[string]json.RawMessage
	_ = json.Unmarshal(out, &root)
	var msgs []map[string]any
	_ = json.Unmarshal(root["messages"], &msgs)
	asst := msgs[1]
	if c, _ := asst["content"].(string); c != "first" {
		t.Fatalf("merged assistant content = %q, want %q (out=%s)", c, "first", out)
	}
	tcs, _ := asst["tool_calls"].([]any)
	if len(tcs) != 1 {
		t.Fatalf("merged assistant tool_calls = %v, want 1 (out=%s)", tcs, out)
	}
}

func TestRepairToolCallSequence_ConsecutiveAssistantArrayContent(t *testing.T) {
	// 连续两条 assistant，且 content 是 OpenAI 分段数组形式：应能正确拼接并保留 tool_calls。
	body := []byte(`{"model":"m","messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":[{"type":"text","text":"part-a"}]},
		{"role":"assistant","content":[{"type":"text","text":"part-b"}],"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"r1"}
	]}`)
	out, changed := repairToolCallSequence(body)
	if !changed {
		t.Fatalf("expected change")
	}
	want := []string{"user", "assistant", "tool"}
	got := rolesOf(out)
	if len(got) != len(want) {
		t.Fatalf("roles = %v, want %v (out=%s)", got, want, out)
	}
	var root map[string]json.RawMessage
	_ = json.Unmarshal(out, &root)
	var msgs []map[string]any
	_ = json.Unmarshal(root["messages"], &msgs)
	asst := msgs[1]
	arr, _ := asst["content"].([]any)
	if len(arr) != 2 {
		t.Fatalf("merged assistant content = %v, want 2 segments (out=%s)", arr, out)
	}
}

func TestRepairToolCallSequence_ConsecutiveAssistantToolCalls(t *testing.T) {
	// 连续两条 assistant 各带 tool_calls：合并后应同时保留两条声明的 tool_call id。
	body := []byte(`{"model":"m","messages":[
		{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},
		{"role":"assistant","content":"","tool_calls":[{"id":"c2","type":"function","function":{"name":"f","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"r1"},
		{"role":"tool","tool_call_id":"c2","content":"r2"}
	]}`)
	out, changed := repairToolCallSequence(body)
	if !changed {
		t.Fatalf("expected change")
	}
	want := []string{"assistant", "tool", "tool"}
	got := rolesOf(out)
	if len(got) != len(want) {
		t.Fatalf("roles = %v, want %v (out=%s)", got, want, out)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("roles[%d] = %q, want %q (out=%s)", i, got[i], want[i], out)
		}
	}
	var root map[string]json.RawMessage
	_ = json.Unmarshal(out, &root)
	var msgs []map[string]any
	_ = json.Unmarshal(root["messages"], &msgs)
	tcs, _ := msgs[0]["tool_calls"].([]any)
	if len(tcs) != 2 {
		t.Fatalf("merged assistant tool_calls = %v, want 2 (out=%s)", tcs, out)
	}
}

func TestRepairToolCallSequence_MissingResultThenUser(t *testing.T) {
	// assistant 声明 tool_calls 后没有结果，直接接一条 user 新提问。
	// 占位结果应补在 assistant 与 user 之间（而非末尾），保证顺序正确。
	body := []byte(`{"model":"m","messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},
		{"role":"user","content":"继续"}
	]}`)
	out, changed := repairToolCallSequence(body)
	if !changed {
		t.Fatalf("expected change")
	}
	want := []string{"user", "assistant", "tool", "user"}
	got := rolesOf(out)
	if len(got) != len(want) {
		t.Fatalf("roles = %v, want %v (out=%s)", got, want, out)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("roles[%d] = %q, want %q (out=%s)", i, got[i], want[i], out)
		}
	}
}
