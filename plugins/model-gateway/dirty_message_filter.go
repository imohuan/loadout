package modelgateway

import (
	"bytes"
	"encoding/json"
)

// stripBrokenAssistantMessages 剥除 OpenAI chat/completions 请求里残缺的 assistant 消息，
// 以及引用了被剥消息 tool_call_id 的 role=tool 结果（避免上游报 orphan tool result）。
//
// 背景：客户端 SDK 在流式响应被中断时，会把没流完的半截 assistant 消息（content 为空
// 且 tool_calls 里 function.name 也为空）写进会话历史，之后每轮请求都带着发。上游
// （step-5-preview 等）对这种残缺记录直接返回 400（code 11133 input_invalid），整个
// 请求作废。本函数在转发到上游之前自动剥掉，让请求能通过上游校验。
//
// 判定规则（同时满足才算残缺）：
//   - role == "assistant"
//   - content 为空字符串 / null / 纯空白（分段数组只要有元素就视为非空，保留消息）
//   - tool_calls 不存在，或存在 function.name 为空字符串的条目
//
// 只有实际发生剥除才重建 body；正常请求零改动（字节级透传）。
func stripBrokenAssistantMessages(body []byte) ([]byte, bool) {
	content := body
	if bytes.HasPrefix(content, []byte("\xEF\xBB\xBF")) {
		content = content[3:]
	}
	trimmed := bytes.TrimLeft(content, " \t\r\n")
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return body, false // 非 JSON 对象：原样透传
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &root); err != nil {
		return body, false
	}
	messagesRaw, ok := root["messages"]
	if !ok {
		return body, false
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(messagesRaw, &messages); err != nil {
		return body, false
	}

	filtered, changed := stripBrokenAssistantMessagesSlice(messages)
	if !changed {
		return body, false
	}
	filteredJSON, err := json.Marshal(filtered)
	if err != nil {
		return body, false
	}
	root["messages"] = filteredJSON
	out, err := json.Marshal(root)
	if err != nil {
		return body, false
	}
	return out, true
}

// stripBrokenAssistantMessagesSlice 对 messages 切片做残缺 assistant 剥除。
// 返回 (新切片, 是否修改)。被剥 assistant 消息声明的 tool_call_id 记入待剔除集合，
// 后续 role=tool 消息若 tool_call_id 命中该集合则一并剥掉。
func stripBrokenAssistantMessagesSlice(messages []json.RawMessage) ([]json.RawMessage, bool) {
	if len(messages) == 0 {
		return messages, false
	}
	changed := false
	out := make([]json.RawMessage, 0, len(messages))
	// droppedIDs：被剥 assistant 消息声明的 tool_call_id 集合，其后引用它们的
	// tool 结果一并剥除（上游对 orphan tool result 也会 400）。
	droppedIDs := map[string]bool{}

	for _, raw := range messages {
		if isBrokenAssistant(raw) {
			_, ids, _ := peekToolMessage(raw)
			for _, id := range ids {
				droppedIDs[id] = true
			}
			changed = true
			continue
		}
		if role, _, toolCallID := peekToolMessage(raw); role == "tool" && droppedIDs[toolCallID] {
			changed = true
			continue
		}
		out = append(out, raw)
	}
	return out, changed
}

// isBrokenAssistant 判断一条消息是否为「流式中断残缺」的 assistant 消息。
// 判定标准见 stripBrokenAssistantMessages 注释。
func isBrokenAssistant(raw json.RawMessage) bool {
	var m struct {
		Role      string          `json:"role"`
		Content   json.RawMessage `json:"content"`
		ToolCalls []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return false // 解析失败按正常消息透传，不误伤
	}
	if m.Role != "assistant" {
		return false
	}
	if !isEmptyContent(m.Content) {
		return false
	}
	if len(m.ToolCalls) == 0 {
		return true // 空 content 且无 tool_calls
	}
	for _, tc := range m.ToolCalls {
		if tc.Function.Name == "" {
			return true // 任一 tool_call 缺名字即视为残缺
		}
	}
	return false
}

// isEmptyContent 判断消息 content 是否为空：null / 空字符串 / 纯空白都算空。
// 分段数组只要有元素就视为非空（保留消息）。
func isEmptyContent(raw json.RawMessage) bool {
	s := string(bytes.TrimSpace(raw))
	if s == "" || s == "null" {
		return true
	}
	if s[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return false // 解析失败按非空处理
		}
		return text == "" || len(bytes.TrimSpace([]byte(text))) == 0
	}
	return false // 数组/对象一律视为非空
}
