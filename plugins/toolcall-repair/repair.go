package toolcallrepair

import (
	"bytes"
	"encoding/json"
	"strings"
)

// repairToolCallSequenceMessages 修复 OpenAI chat/completions 的 messages 工具调用序列，
// 使每条 assistant 声明的 tool_calls 与 role=tool 结果一一对应。返回 (新切片, 是否修改)。
//
// 背景：agent 客户端回传完整对话历史时，序列可能不完整——一次 assistant 声明的多个
// tool_calls 缺部分结果、role=tool 缺前置声明（孤立结果）、相邻两条 assistant。部分上游
// （火山方舟等）对这种不匹配直接 400（tool_call_sequence_broken / 400001）。
//
// 修复策略：
//   - assistant 的结果缺失 → 补一条占位 tool 结果（content 为空）。
//   - 孤立 tool 结果 → 补一条合成 assistant 声明，名字沿用最近一次真实工具名；
//     借不到名字时直接剥掉该结果。绝不写 name:""（上游会 400 code 11133）。
//   - 相邻 assistant → 合并（content 拼接、tool_calls 合并），不丢信息。
//   - 序列以未消化的 tool_calls 结尾 → 末尾补占位结果。
func repairToolCallSequenceMessages(messages []json.RawMessage) ([]json.RawMessage, bool) {
	if len(messages) == 0 {
		return messages, false
	}
	changed := false
	out := make([]json.RawMessage, 0, len(messages)+4)
	// pending：最近一条带 tool_calls 的 assistant 声明的、尚未被 tool 结果消化的 id 列表。
	var pending []string
	// lastName：最近一次能确认的工具名，给孤立结果补声明时避免写空名。
	lastName := ""

	for _, raw := range messages {
		role, toolCalls, toolCallID := peekToolMessage(raw)

		switch role {
		case "assistant":
			// 连续 assistant：上游（方舟 DeepSeek 等）严格禁止相邻两条 assistant，
			// 必须被 tool 结果或 user 间隔，否则 400 `400001`。合并成一条不丢信息。
			if len(out) > 0 {
				lastRole, _, _ := peekToolMessage(out[len(out)-1])
				if lastRole == "assistant" {
					merged, ok := mergeAssistantMessages(out[len(out)-1], raw)
					if ok {
						out[len(out)-1] = merged
						changed = true
						pending = append([]string{}, toolCalls...)
						continue
					}
				}
			}
			if len(toolCalls) > 0 {
				if names := toolNamesOf(raw); len(names) > 0 {
					lastName = names[len(names)-1]
				}
				// 新声明打断上一组未消化的工具调用 → 先补占位结果。
				for _, id := range pending {
					out = append(out, buildToolResultMessage(id))
					changed = true
				}
				pending = append([]string{}, toolCalls...)
			} else {
				// 普通回复（无 tool_calls）：上一组缺结果同样先补占位。
				for _, id := range pending {
					out = append(out, buildToolResultMessage(id))
					changed = true
				}
				pending = nil
			}
			out = append(out, raw)

		case "tool":
			if len(pending) == 0 {
				// 孤立 tool 结果：有 id 且有可借用工具名时补声明；否则无法归属，剥掉。
				if toolCallID != "" && lastName != "" {
					out = append(out, buildAssistantDeclMessage(toolCallID, lastName))
					changed = true
				} else {
					changed = true
					continue
				}
			} else {
				pending = removeID(pending, toolCallID)
			}
			out = append(out, raw)

		default:
			// system / user / developer：若上一组 tool_calls 未被消化就先补占位结果。
			for _, id := range pending {
				out = append(out, buildToolResultMessage(id))
				changed = true
			}
			pending = nil
			out = append(out, raw)
		}
	}

	// 末尾收尾：最后一段声明的 tool_calls 若仍未被结果消化，补占位结果。
	for _, id := range pending {
		out = append(out, buildToolResultMessage(id))
		changed = true
	}
	return out, changed
}

// peekToolMessage 宽松解析一条消息的 role / tool_calls id 列表 / tool_call_id。
func peekToolMessage(raw json.RawMessage) (role string, toolCallIDs []string, toolCallID string) {
	var m struct {
		Role      string `json:"role"`
		ToolCalls []struct {
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

// buildToolResultMessage 构造一条占位 tool 结果：tool_call_id 对应缺失结果，content 为空。
func buildToolResultMessage(toolCallID string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"role":         "tool",
		"tool_call_id": toolCallID,
		"content":      "",
	})
	return b
}

// buildAssistantDeclMessage 构造一条合成 assistant 消息，给孤立 tool 结果补出处。
// name 必须是真实工具名：上游对空 name 返回 400 code 11133。
func buildAssistantDeclMessage(toolCallID, name string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"role":    "assistant",
		"content": "",
		"tool_calls": []map[string]any{{
			"id":       toolCallID,
			"type":     "function",
			"function": map[string]any{"name": name, "arguments": "{}"},
		}},
	})
	return b
}

// toolNamesOf 提取一条 assistant 消息里所有非空 tool_call 的 function.name，保持原顺序。
func toolNamesOf(raw json.RawMessage) []string {
	var m struct {
		ToolCalls []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	out := make([]string, 0, len(m.ToolCalls))
	for _, tc := range m.ToolCalls {
		if name := strings.TrimSpace(tc.Function.Name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// mergeAssistantMessages 合并两条相邻 assistant：content 拼接、tool_calls 合并。
// content 支持字符串与 OpenAI 分段数组混合。
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
		merged["content"] = ma["content"]
	}

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
		// 一方为空字符串时不加换行，避免 "first\n" 这类尾随换行。
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
