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
			// 连续 assistant：上游（方舟 DeepSeek 等）严格禁止相邻两条 assistant 消息
			// （必须被 tool 结果或 user 间隔），否则返回 400 `400001` 参数拒绝。典型场景是
			// 客户端把"上轮工具调用的最终回复"和"新一轮的 assistant 工具调用"直接连发。
			// 安全修复：把当前 assistant 与输出里最后一条 assistant 合并成一条
			// （content 拼接、tool_calls 合并），不丢任何信息。
			if len(out) > 0 {
				lastRole, _, _ := peekToolMessage(out[len(out)-1])
				if lastRole == "assistant" {
					merged, ok := mergeAssistantMessages(out[len(out)-1], raw)
					if ok {
						out[len(out)-1] = merged
						changed = true
						// 合并后 pending 由 toolCalls 覆盖（新声明），直接进入下一轮。
						pending = append([]string{}, toolCalls...)
						continue
					}
				}
			}
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

// mergeAssistantMessages 将两条相邻的 assistant 消息合并为一条：content 拼接、tool_calls 合并。
// 返回 (合并后的消息 JSON, 是否成功)。仅当两条消息 role 都是 assistant 且都可解析时才成功；
// 否则返回原样（调用方应按其它策略处理，例如直接透传）。
//
// content 可能是字符串，也可能是 OpenAI 分段数组（[{type:"text",text:...}]）：
//   - 均为字符串 → 用换行拼接。
//   - 均为数组 → 元素直接拼接。
//   - 一为字符串一为数组 → 把字符串包成一段 text 元素再拼接。
//   - 为空/缺失 → 按无内容处理，取另一条的 content 原样。
func mergeAssistantMessages(a, b json.RawMessage) (json.RawMessage, bool) {
	var ma, mb map[string]json.RawMessage
	if err := json.Unmarshal(a, &ma); err != nil {
		return nil, false
	}
	if err := json.Unmarshal(b, &mb); err != nil {
		return nil, false
	}
	ra, _ := ma["role"].MarshalJSON()
	rb, _ := mb["role"].MarshalJSON()
	if string(ra) != `"assistant"` || string(rb) != `"assistant"` {
		return nil, false
	}

	merged := make(map[string]json.RawMessage, len(ma)+1)
	for k, v := range ma {
		merged[k] = v
	}
	// 除 content / tool_calls 外，b 上的字段（如 reasoning_content）也并入，取 b 的。
	for k, v := range mb {
		if k == "content" || k == "tool_calls" {
			continue
		}
		merged[k] = v
	}

	// content 拼接。
	if contentB, ok := mb["content"]; ok {
		contentA, hasA := ma["content"]
		if !hasA {
			merged["content"] = contentB
		} else {
			joined, ok := joinContents(contentA, contentB)
			if !ok {
				return nil, false
			}
			merged["content"] = joined
		}
	} else if _, ok := ma["content"]; ok {
		// b 无 content，保留 a 的。
		merged["content"] = ma["content"]
	}

	// tool_calls 合并。
	tca, _ := ma["tool_calls"].MarshalJSON()
	tcb, _ := mb["tool_calls"].MarshalJSON()
	if string(tcb) != "null" && string(tcb) != "" {
		if string(tca) == "null" || string(tca) == "" {
			merged["tool_calls"] = mb["tool_calls"]
		} else {
			combined, ok := mergeToolCalls(ma["tool_calls"], mb["tool_calls"])
			if !ok {
				return nil, false
			}
			merged["tool_calls"] = combined
		}
	}

	out, err := json.Marshal(merged)
	if err != nil {
		return nil, false
	}
	return out, true
}

// joinContents 拼接两条消息的 content，支持字符串与分段数组混合。
func joinContents(a, b json.RawMessage) (json.RawMessage, bool) {
	av := string(bytes.TrimSpace(a))
	bv := string(bytes.TrimSpace(b))
	isStrA := len(av) > 0 && av[0] == '"'
	isStrB := len(bv) > 0 && bv[0] == '"'
	isNullA := av == "null" || av == ""
	isNullB := bv == "null" || bv == ""

	switch {
	case isNullA && isNullB:
		return json.RawMessage(`""`), true
	case isNullA:
		return b, true
	case isNullB:
		return a, true
	case isStrA && isStrB:
		var sa, sb string
		if err := json.Unmarshal(a, &sa); err != nil {
			return nil, false
		}
		if err := json.Unmarshal(b, &sb); err != nil {
			return nil, false
		}
		// 一方为空字符串时不加换行，直接取非空的一方，避免 "first\n" 这类尾随换行。
		if sa == "" {
			return b, true
		}
		if sb == "" {
			return a, true
		}
		out, err := json.Marshal(sa + "\n" + sb)
		if err != nil {
			return nil, false
		}
		return out, true
	default:
		// 至少一个是数组：统一成数组再拼接。
		as, ok := asContentArray(a, isNullA)
		if !ok {
			return nil, false
		}
		bs, ok := asContentArray(b, isNullB)
		if !ok {
			return nil, false
		}
		out, err := json.Marshal(append(as, bs...))
		if err != nil {
			return nil, false
		}
		return out, true
	}
}

// asContentArray 把一条 content 转成 OpenAI 分段数组；字符串会包成 {type:"text",text:...}。
func asContentArray(raw json.RawMessage, isNull bool) ([]map[string]any, bool) {
	if isNull {
		return []map[string]any{}, true
	}
	s := string(bytes.TrimSpace(raw))
	if len(s) > 0 && s[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, false
		}
		return []map[string]any{{"type": "text", "text": text}}, true
	}
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, false
	}
	return arr, true
}

// mergeToolCalls 把两条 assistant 的 tool_calls 数组合并成一条。
func mergeToolCalls(a, b json.RawMessage) (json.RawMessage, bool) {
	var ta, tb []map[string]any
	if err := json.Unmarshal(a, &ta); err != nil {
		return nil, false
	}
	if err := json.Unmarshal(b, &tb); err != nil {
		return nil, false
	}
	out, err := json.Marshal(append(ta, tb...))
	if err != nil {
		return nil, false
	}
	return out, true
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
