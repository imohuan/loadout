package visionv2

import "testing"

// toolMapAt 从 tools 数组中查找 name 匹配（按格式）的工具元素。
func findToolByName(t *testing.T, tools []any, name string) map[string]any {
	t.Helper()
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		// 直接比对三格式下的名称字段
		if fn, ok := tool["function"].(map[string]any); ok {
			if n, _ := fn["name"].(string); n == name {
				return tool
			}
		}
		if n, _ := tool["name"].(string); n == name {
			return tool
		}
	}
	t.Fatalf("tools 中未找到工具 %q，全部元素: %#v", name, tools)
	return nil
}

func TestEnsureToolChat(t *testing.T) {
	// 空 tools → 注入（结果含 look_at_image 且 function 嵌套）
	tools, injected := ensureLookAtImageTool(nil, formatChat)
	if !injected {
		t.Fatalf("空 tools 应标记 injected=true")
	}
	if len(tools) != 1 {
		t.Fatalf("期望注入 1 个工具，得到 %d", len(tools))
	}
	tool := findToolByName(t, tools, lookAtImageToolName)
	fn, ok := tool["function"].(map[string]any)
	if !ok {
		t.Fatalf("chat 格式工具缺少 function 嵌套，tool=%#v", tool)
	}
	if n, _ := fn["name"].(string); n != lookAtImageToolName {
		t.Fatalf("function.name 错误: %q", n)
	}
	if _, ok := fn["parameters"].(map[string]any); !ok {
		t.Fatalf("function 缺少 parameters")
	}

	// 已有同名 function 嵌套工具 → 不重复（长度不变）
	existing := []any{map[string]any{
		"type": "function",
		"function": map[string]any{"name": lookAtImageToolName},
	}}
	again, injected := ensureLookAtImageTool(existing, formatChat)
	if injected {
		t.Fatalf("已存在同名工具时不应标记 injected=true")
	}
	if len(again) != 1 {
		t.Fatalf("已存在同名工具时不应追加，长度=%d", len(again))
	}
}

func TestEnsureToolClaude(t *testing.T) {
	tools, injected := ensureLookAtImageTool(nil, formatClaude)
	if !injected {
		t.Fatalf("空 tools 应标记 injected=true")
	}
	if len(tools) != 1 {
		t.Fatalf("期望注入 1 个工具，得到 %d", len(tools))
	}
	tool := findToolByName(t, tools, lookAtImageToolName)
	if n, _ := tool["name"].(string); n != lookAtImageToolName {
		t.Fatalf("claude 格式 name 错误: %q", n)
	}
	if _, ok := tool["input_schema"].(map[string]any); !ok {
		t.Fatalf("claude 格式缺少 input_schema")
	}
	if _, ok := tool["function"]; ok {
		t.Fatalf("claude 格式不应有 function 嵌套字段")
	}

	// 已有 {"name":"look_at_image"} → 不重复
	existing := []any{map[string]any{"name": lookAtImageToolName}}
	again, injected := ensureLookAtImageTool(existing, formatClaude)
	if injected {
		t.Fatalf("已存在同名工具时不应标记 injected=true")
	}
	if len(again) != 1 {
		t.Fatalf("已存在同名工具时不应追加，长度=%d", len(again))
	}
}

func TestEnsureToolResponses(t *testing.T) {
	tools, injected := ensureLookAtImageTool(nil, formatResponses)
	if !injected {
		t.Fatalf("空 tools 应标记 injected=true")
	}
	if len(tools) != 1 {
		t.Fatalf("期望注入 1 个工具，得到 %d", len(tools))
	}
	tool := findToolByName(t, tools, lookAtImageToolName)
	if typ, _ := tool["type"].(string); typ != "function" {
		t.Fatalf("responses 格式 type 应为 function，得到 %q", typ)
	}
	if n, _ := tool["name"].(string); n != lookAtImageToolName {
		t.Fatalf("responses 格式 name 错误: %q", n)
	}
	if _, ok := tool["parameters"].(map[string]any); !ok {
		t.Fatalf("responses 格式缺少平铺 parameters")
	}
	if _, ok := tool["function"]; ok {
		t.Fatalf("responses 格式不应有 function 嵌套字段")
	}

	// 已有同名 → 不重复
	existing := []any{map[string]any{"type": "function", "name": lookAtImageToolName}}
	again, injected := ensureLookAtImageTool(existing, formatResponses)
	if injected {
		t.Fatalf("已存在同名工具时不应标记 injected=true")
	}
	if len(again) != 1 {
		t.Fatalf("已存在同名工具时不应追加，长度=%d", len(again))
	}
}

func TestEnsureToolPreservesExisting(t *testing.T) {
	existing := []any{
		map[string]any{"type": "function", "function": map[string]any{"name": "web_search"}},
	}
	for _, format := range []visionProxyFormat{formatChat, formatClaude, formatResponses} {
		tools, injected := ensureLookAtImageTool(existing, format)
		if !injected {
			t.Fatalf("format=%d 应标记 injected=true（look_at_image 不存在）", format)
		}
		if len(tools) != 2 {
			t.Fatalf("format=%d 期望原工具保留并追加 1 个，得到长度 %d", format, len(tools))
		}
		web := findToolByName(t, tools, "web_search")
		_ = web
		_ = findToolByName(t, tools, lookAtImageToolName)
	}
}

func TestEnsureToolNonArray(t *testing.T) {
	for _, format := range []visionProxyFormat{formatChat, formatClaude, formatResponses} {
		// nil
		tools, injected := ensureLookAtImageTool(nil, format)
		if !injected {
			t.Fatalf("format=%d nil 输入应标记 injected=true", format)
		}
		if len(tools) != 1 {
			t.Fatalf("format=%d nil 输入应返回 1 个工具，得到 %d", format, len(tools))
		}
		_ = findToolByName(t, tools, lookAtImageToolName)
		// 字符串（非数组类型断言失败）
		tools, injected = ensureLookAtImageTool("not-array", format)
		if !injected {
			t.Fatalf("format=%d 字符串输入应标记 injected=true", format)
		}
		if len(tools) != 1 {
			t.Fatalf("format=%d 字符串输入应返回 1 个工具，得到 %d", format, len(tools))
		}
		_ = findToolByName(t, tools, lookAtImageToolName)
	}
}
