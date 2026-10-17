package modelgateway

import (
	"bytes"
	"encoding/json"
	"strings"
)

// stripBrokenAssistantMessages 剥除 OpenAI chat/completions 请求里残缺的 assistant 消息，
// 以及引用了被剥消息 tool_call_id 的 role=tool 结果（避免上游报 orphan tool result）。
//
// 背景：客户端 SDK 在流式响应被中断时，会把没流完的半截 assistant 消息（content 为空
// 且 tool_calls 里 function.name 也为空）写进会话历史，之后每轮请求都带着发。上游
// （step-5-preview 等）对这种残缺记录直接返回 400（code 11133 input_invalid），整个
// 请求作废。本函数在转发到上游之前自动清理，让请求能通过上游校验。
//
// 判定规则：
//   - 空工具名（function.name 为空/纯空白）是硬伤：无论 content 是否有正文，该条
//     tool_calls 都必须移除（实测只要带 name:"" 上游就 400）。有正文时保留 assistant
//     的 content，并剥掉引用这些 tool_call_id 的 role=tool 结果。
//   - assistant 的 content 为空字符串 / null / 纯空白（分段数组只要有元素就视为非空）
//     且没有可保留的 tool_calls 时，整条消息剥除。
//
// 注意：本函数不能只看 content 是否为空就放行——网关自己的 toolcall repair 在给孤立
// tool 结果补声明时曾写出 name:""，content 是空串的坏声明能命中旧规则，但带正文的
// 空名声明会漏掉，最终仍被上游 400。
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
		if cleaned, ids, drop := cleanBrokenAssistant(raw); drop {
			for _, id := range ids {
				droppedIDs[id] = true
			}
			changed = true
			continue
		} else if cleaned != nil {
			for _, id := range ids {
				droppedIDs[id] = true
			}
			out = append(out, cleaned)
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

// cleanBrokenAssistant 清理一条 assistant 消息里的残缺工具调用。
//
// 返回 (保留的消息, 被移除的 tool_call_id 列表, 是否整条丢弃)：
//   - 没有任何残缺：返回 (nil, nil, false)，调用方原样透传。
//   - 只有部分 tool_calls 的 name 为空：移除这些条目后返回重建的消息（保留正文与
//     其余正常 tool_calls），第二个返回值是需要一并剥除结果的 id。
//   - 清理后既没有正文也没有 tool_calls：返回 (nil, ids, true)，整条剥除。
func cleanBrokenAssistant(raw json.RawMessage) (json.RawMessage, []string, bool) {
	var m struct {
		Role      string            `json:"role"`
		Content   json.RawMessage   `json:"content"`
		ToolCalls []json.RawMessage `json:"tool_calls"`
	}
	if err := json.Unmarshal(raw, &m); err != nil || m.Role != "assistant" {
		return nil, nil, false
	}

	// 逐条检查 tool_calls，收集 name 为空的条目。
	var badIDs []string
	kept := make([]json.RawMessage, 0, len(m.ToolCalls))
	for _, tcRaw := range m.ToolCalls {
		var tc struct {
			ID       string `json:"id"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		if err := json.Unmarshal(tcRaw, &tc); err != nil {
			kept = append(kept, tcRaw) // 解析不了就原样保留，避免误删
			continue
		}
		if strings.TrimSpace(tc.Function.Name) == "" {
			if tc.ID != "" {
				badIDs = append(badIDs, tc.ID)
			}
			continue
		}
		kept = append(kept, tcRaw)
	}
	if len(badIDs) == 0 && len(kept) == len(m.ToolCalls) {
		// 没有空名工具调用。若正文为空且本来就没有 tool_calls，才算整条残缺
		// （流式中断写进历史的空 assistant）。
		if len(m.ToolCalls) == 0 && isEmptyContent(m.Content) {
			return nil, nil, true
		}
		return nil, nil, false
	}

	if len(kept) == 0 && isEmptyContent(m.Content) {
		return nil, badIDs, true
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, nil, false
	}
	if len(kept) == 0 {
		delete(obj, "tool_calls")
	} else {
		b, err := json.Marshal(kept)
		if err != nil {
			return nil, nil, false
		}
		obj["tool_calls"] = b
	}
	// 正文为空但有可保留的工具调用：content 补成空串，保持字段形态合法。
	if isEmptyContent(obj["content"]) {
		obj["content"] = json.RawMessage(`""`)
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return nil, nil, false
	}
	return out, badIDs, false
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
