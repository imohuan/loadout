package modelgateway

import (
	"bytes"
	"encoding/json"
)

// repairToolCallSequence 修复 OpenAI chat/completions 请求里 messages 的工具调用序列，
// 使每条 assistant 消息声明的 tool_calls 与 role=tool 的结果消息一一对应。
//
// 背景：agent 客户端（如本机 harness）把完整对话历史回传给网关时，历史序列可能不完整——
// 一次 assistant 声明的多个 tool_calls 缺了部分结果、某条 role=tool 结果缺少前置的
// assistant 声明（孤立结果）、或序列以孤立的 tool_calls/结果结尾。上游（火山方舟等）
// 对这种不匹配直接返回 400 `tool_call_sequence_broken`（"tool calls and tool results do not match"），
// 整个请求作废。本函数在转发到上游之前自动补全，让请求能通过上游校验。
//
// 修复策略（仅补全缺失、不删除任何已有消息，避免丢上下文）：
//   - assistant 声明了 tool_calls 但后续没有对应的 role=tool 结果 → 在下一段 assistant 正文前
//     补一条占位 tool 结果（content 为空）。
//   - role=tool 结果缺少前置 assistant 的 tool_calls 声明（孤立结果）→ 在其前补一条合成
//     assistant 消息声明该 tool_call（arguments 为空对象），让结果有出处。
//   - 序列以未消化的 tool_calls 结尾 → 在末尾补占位 tool 结果。
//
// 返回 (修复后 body, 是否发生修改)。正常序列或非 chat/completions 形态原样返回，零改动。
func repairToolCallSequence(body []byte) ([]byte, bool) {
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
		return body, false // 无 messages（如 /responses 等其他端点）：不适用本修复
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(messagesRaw, &messages); err != nil {
		return body, false
	}

	repaired, changed := repairToolCallSequenceMessages(messages)
	if !changed {
		return body, false
	}
	repairedJSON, err := json.Marshal(repaired)
	if err != nil {
		return body, false
	}
	root["messages"] = repairedJSON
	out, err := json.Marshal(root)
	if err != nil {
		return body, false
	}
	return out, true
}

// repairToolCallSequenceMessages 对 messages 切片做工具调用序列补全，返回 (新切片, 是否修改)。
// 输入的每条消息以 json.RawMessage 保留原样；仅当需要补全时才重建对象或插入新消息。
func repairToolCallSequenceMessages(messages []json.RawMessage) ([]json.RawMessage, bool) {
	if len(messages) == 0 {
		return messages, false
	}
	changed := false
	out := make([]json.RawMessage, 0, len(messages)+4)
	// pending：最近一条带 tool_calls 的 assistant 消息声明的、尚未被 tool 结果消化的 id 列表。
	var pending []string

	for _, raw := range messages {
		role, toolCalls, toolCallID := peekToolMessage(raw)

		switch role {
		case "assistant":
			if len(toolCalls) > 0 {
				// 新的工具调用声明：若上一组 pending 还没被消化完就被新声明打断，
				// 说明上一组工具结果缺失 → 先补占位结果再进入本轮。
				for _, id := range pending {
					out = append(out, buildToolResultMessage(id))
					changed = true
				}
				pending = append([]string{}, toolCalls...)
			} else {
				// 普通 assistant 回复（无 tool_calls）：若 pending 仍非空，
				// 说明上组工具调用缺结果 → 补占位结果，然后清空。
				for _, id := range pending {
					out = append(out, buildToolResultMessage(id))
					changed = true
				}
				pending = nil
			}
			out = append(out, raw)

		case "tool":
			if len(pending) == 0 {
				// 孤立 tool 结果：前一条 assistant 没有声明该 tool_call。
				// 有 id 时补一条合成 assistant 声明让结果有出处；连 id 都没有的
				// 结果无法归属，透传（不新增损坏数据）。
				if toolCallID != "" {
					out = append(out, buildAssistantDeclMessage(toolCallID))
					changed = true
				}
			} else {
				pending = removeID(pending, toolCallID)
			}
			out = append(out, raw)

		default:
			// system / user / developer 等：不影响工具调用序列，原样透传。
			// 但若此时上一条 assistant 的 tool_calls 还未被结果消化（pending 非空），
			// 说明工具结果缺失且被新消息打断 → 先补占位结果，再透传本条。
			for _, id := range pending {
				out = append(out, buildToolResultMessage(id))
				changed = true
			}
			pending = nil
			out = append(out, raw)
		}
	}

	// 序列末尾收尾：最后一段 assistant 声明的 tool_calls 若仍未被结果消化，补占位结果。
	for _, id := range pending {
		out = append(out, buildToolResultMessage(id))
		changed = true
	}
	return out, changed
}

// peekToolMessage 宽松解析一条消息的 role / tool_calls id 列表 / tool_call_id。
// 无法解析时返回空 role（按普通消息透传）。
func peekToolMessage(raw json.RawMessage) (role string, toolCallIDs []string, toolCallID string) {
	var m struct {
		Role       string `json:"role"`
		ToolCalls  []struct {
			ID string `json:"id"`
		} `json:"tool_calls"`
		ToolCallID string `json:"tool_call_id"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", nil, ""
	}
	for _, tc := range m.ToolCalls {
		if tc.ID != "" {
			toolCallIDs = append(toolCallIDs, tc.ID)
		}
	}
	return m.Role, toolCallIDs, m.ToolCallID
}

// buildToolResultMessage 构造一条占位 tool 结果：tool_call_id 对应缺失的结果，content 为空。
func buildToolResultMessage(toolCallID string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"role":         "tool",
		"tool_call_id": toolCallID,
		"content":      "",
	})
	return b
}

// buildAssistantDeclMessage 构造一条合成 assistant 消息，声明一个空参数的 tool_call，
// 用于给孤立的 role=tool 结果补上出处。
func buildAssistantDeclMessage(toolCallID string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"role":      "assistant",
		"content":   "",
		"tool_calls": []map[string]any{{
			"id":       toolCallID,
			"type":     "function",
			"function": map[string]any{"name": "", "arguments": "{}"},
		}},
	})
	return b
}

// removeID 从切片中移除第一个等于 id 的元素（返回新切片，不改原切片）。
func removeID(ids []string, id string) []string {
	for i, v := range ids {
		if v == id {
			out := make([]string, 0, len(ids)-1)
			out = append(out, ids[:i]...)
			out = append(out, ids[i+1:]...)
			return out
		}
	}
	return ids
}
