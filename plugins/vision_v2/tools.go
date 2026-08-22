package visionv2

const lookAtImageToolName = "look_at_image"

const lookAtImageToolDesc = "图片以 <vision_img_xxx> 标记形式出现在对话中（xxx 为图片 id）。需要查看图片内容时调用本工具：把标记中的 id 传给 image_id，并用 prompt 说明你想从图片中获取的信息方向（如颜色、文字、布局、产品、实体关系等）。支持一次传多张图。不要输出标记本身。"

// ensureLookAtImageTool 按格式注入 look_at_image 工具；已存在同名工具返回原数组。
func ensureLookAtImageTool(tools any, format visionProxyFormat) []any {
	existing, _ := tools.([]any)
	for _, raw := range existing {
		if tool, ok := raw.(map[string]any); ok && toolHasName(tool, format) {
			return existing
		}
	}
	return append(existing, toolSchema(format))
}

// toolHasName 按格式判断工具元素是否为 look_at_image：
// chat: tool["function"]["name"]；claude/responses: tool["name"]。
func toolHasName(tool map[string]any, format visionProxyFormat) bool {
	switch format {
	case formatClaude, formatResponses:
		name, _ := tool["name"].(string)
		return name == lookAtImageToolName
	default: // formatChat
		if fn, ok := tool["function"].(map[string]any); ok {
			name, _ := fn["name"].(string)
			return name == lookAtImageToolName
		}
	}
	return false
}

func toolSchema(format visionProxyFormat) map[string]any {
	props := map[string]any{
		"image_id": map[string]any{"type": "string", "description": "图片 id，取自 <vision_img_xxx> 标记中的 xxx"},
		"prompt":   map[string]any{"type": "string", "description": "识别方向，说明要从图片中提取什么信息"},
		"image_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "可选，多张图时传多个 id"},
	}
	required := []any{"image_id"}
	switch format {
	case formatClaude:
		return map[string]any{"name": lookAtImageToolName, "description": lookAtImageToolDesc,
			"input_schema": map[string]any{"type": "object", "properties": props, "required": required}}
	case formatResponses:
		return map[string]any{"type": "function", "name": lookAtImageToolName, "description": lookAtImageToolDesc,
			"parameters": map[string]any{"type": "object", "properties": props, "required": required}}
	default: // formatChat
		return map[string]any{"type": "function", "function": map[string]any{
			"name": lookAtImageToolName, "description": lookAtImageToolDesc,
			"parameters": map[string]any{"type": "object", "properties": props, "required": required}}}
	}
}
