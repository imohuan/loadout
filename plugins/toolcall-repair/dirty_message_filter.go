package toolcallrepair

import (
	"encoding/json"
	"strings"
)

// stripBrokenAssistantMessagesSlice 清理 messages 里残缺的 assistant 工具调用声明，
// 以及引用了被删声明的 role=tool 结果。返回 (新切片, 是否修改)。
//
// 背景：客户端 SDK 在流式响应被中断时，会把没流完的半截 assistant 消息写进历史——
// content 为空、tool_calls 的 function.name 为空；也有些网关的历史里会出现带正文但
// 工具名为空的声明。上游（step-5-preview、火山方舟等）见到空工具名直接 400
// （code 11133 input_invalid），整个请求作废。
//
// 判定规则：
//   - 空工具名（function.name 为空/纯空白）是硬伤：无论 content 是否有正文，该条
//     tool_calls 都必须移除；有正文时保留 assistant 的 content，并剥掉引用这些
//     tool_call_id 的 role=tool 结果。
//   - content 为空且没有可保留的 tool_calls 时，整条 assistant 剥除。
func stripBrokenAssistantMessagesSlice(messages []json.RawMessage) ([]json.RawMessage, bool) {
	if len(messages) == 0 {
		return messages, false
	}
	changed := false
	out := make([]json.RawMessage, 0, len(messages))
	// droppedIDs：被删 assistant 声明过的 tool_call_id，其后引用它们的 tool 结果一并剥除。
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
		// 没有空名工具调用。正文为空且本来就没有 tool_calls 才算整条残缺。
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
