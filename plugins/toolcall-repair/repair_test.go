package toolcallrepair

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func rolesOf(body []byte) []string {
	var root struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &root); err != nil {
		return nil
	}
	out := make([]string, 0, len(root.Messages))
	for _, m := range root.Messages {
		out = append(out, m.Role)
	}
	return out
}

func TestApply_NothingToRepair(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"done"}]}`)
	got, changed := apply(body)
	if changed || !bytes.Equal(got, body) {
		t.Fatalf("正常请求应字节级透传，changed=%v out=%s", changed, got)
	}
}

func TestApply_NonChatBody(t *testing.T) {
	if _, changed := apply([]byte(`{"model":"m"}`)); changed {
		t.Fatal("无 messages 不应改动")
	}
	if _, changed := apply([]byte(`notjson`)); changed {
		t.Fatal("非 JSON 不应改动")
	}
}

// 空工具名是上游 400 的直接原因（code 11133）：即便正文非空也必须清掉该 tool_call。
func TestApply_EmptyNameKeepsContent(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"我先查一下","tool_calls":[{"id":"bad","type":"function","function":{"name":"","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"bad","content":"结果"},
		{"role":"assistant","content":"完成"}
	]}`)
	got, changed := apply(body)
	if !changed {
		t.Fatal("空工具名应被清理")
	}
	var root struct {
		Messages []struct {
			Role      string          `json:"role"`
			Content   string          `json:"content"`
			ToolCalls json.RawMessage `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(got, &root); err != nil {
		t.Fatal(err)
	}
	// 清理后剩下 user、带正文的 assistant、"完成"；两条相邻 assistant 会合并成一条。
	if len(root.Messages) != 2 {
		t.Fatalf("应剩 2 条消息（相邻 assistant 合并），实际 %d", len(root.Messages))
	}
	if !strings.HasPrefix(root.Messages[1].Content, "我先查一下") {
		t.Fatalf("正文应保留: %q", root.Messages[1].Content)
	}
	if len(root.Messages[1].ToolCalls) > 0 {
		t.Fatalf("空名 tool_calls 应被移除: %s", root.Messages[1].ToolCalls)
	}
	if bytes.Contains(got, []byte(`"tool_call_id":"bad"`)) {
		t.Fatal("引用被清调用的 tool 结果应一并剥除")
	}
	if !strings.Contains(root.Messages[1].Content, "完成") {
		t.Fatalf("合并后的正文应包含两条 assistant 的内容: %q", root.Messages[1].Content)
	}
}

// 原始失败请求：最后一条是孤立 tool 结果。补全时不能写出 name:""。
func TestApply_OrphanResultNeverEmptyName(t *testing.T) {
	body := []byte(`{"model":"m","messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"r1"},
		{"role":"tool","tool_call_id":"c2","content":"r2"}
	]}`)
	got, changed := apply(body)
	if !changed {
		t.Fatal("孤立结果应触发补全")
	}
	if bytes.Contains(got, []byte(`"name":""`)) {
		t.Fatalf("补全后不得出现空工具名: %s", got)
	}
	var root struct {
		Messages []struct {
			Role      string `json:"role"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(got, &root); err != nil {
		t.Fatal(err)
	}
	var synthFound bool
	for _, m := range root.Messages {
		for _, tc := range m.ToolCalls {
			if tc.ID == "c2" {
				synthFound = true
				if tc.Function.Name != "read_file" {
					t.Fatalf("合成声明应沿用最近的真实工具名，实际 %q", tc.Function.Name)
				}
			}
		}
	}
	if !synthFound {
		t.Fatal("应给 c2 补一条合成声明")
	}
}

// 一个真实工具名都借不到时，孤立结果无法归属，直接剥掉而不是造空名声明。
func TestApply_OrphanWithoutKnownNameDropped(t *testing.T) {
	body := []byte(`{"model":"m","messages":[
		{"role":"user","content":"hi"},
		{"role":"tool","tool_call_id":"c1","content":"r1"}
	]}`)
	got, changed := apply(body)
	if !changed {
		t.Fatal("expected change")
	}
	if bytes.Contains(got, []byte(`"name":""`)) || bytes.Contains(got, []byte(`"role":"tool"`)) {
		t.Fatalf("无法归属的孤立结果应被剥除: %s", got)
	}
	if roles := rolesOf(got); len(roles) != 1 || roles[0] != "user" {
		t.Fatalf("roles = %v, want [user]", roles)
	}
}

// 连续 assistant：上游严格禁止，应合并为一条且保留 content 与 tool_calls。
func TestApply_ConsecutiveAssistantMerged(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"first"},
		{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"r1"}
	]}`)
	got, changed := apply(body)
	if !changed {
		t.Fatal("expected change")
	}
	if roles := rolesOf(got); len(roles) != 3 {
		t.Fatalf("roles = %v, want [user assistant tool]", roles)
	}
	if !strings.Contains(string(got), `"first"`) || !strings.Contains(string(got), `"c1"`) {
		t.Fatalf("合并后应同时保留正文和调用: %s", got)
	}
}

// 缺失 tool 结果时应补一条空内容占位结果，而不是删除声明。
func TestApply_MissingResultFilled(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},
		{"role":"user","content":"继续"}
	]}`)
	got, changed := apply(body)
	if !changed {
		t.Fatal("expected change")
	}
	if want := []string{"user", "assistant", "tool", "user"}; !equalStrings(rolesOf(got), want) {
		t.Fatalf("roles = %v, want %v", rolesOf(got), want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
