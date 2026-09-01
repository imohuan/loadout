package multimodalmcp

import (
	"context"

	"loadout/core/mcpkit"
)

// tools 返回多模态 MCP 暴露的 4 个识别工具（schema + Handler 分发）。
// schema 参数严格按计划 2.1；Handler 把 args 分发到 Service 上的识别方法
// （识别函数由识别子代理实现，此处仅定义签名并留 TODO）。
func tools(s *Service) []mcpkit.ServerTool {
	return []mcpkit.ServerTool{
		{
			Name:        "understand_image",
			Description: "理解一张图片：识别图片内容、细节、文字、物体等。image 接受 url、data URI（data:{mime};base64,...）或 file://本地路径；detail 控制精细度（low/high/xhigh）；prompt 为可选的自由描述提示。",
			InputSchema: understandImageSchema(),
			Handler: func(ctx context.Context, args map[string]any) (*mcpkit.ToolResult, error) {
				return s.understandImage(ctx, args)
			},
		},
		{
			Name:        "understand_video",
			Description: "理解一段视频：抽帧并按时间顺序感知内容。video 接受 url、data URI 或 file://本地路径；fps 控制抽帧频率（0.2~5，默认 1）；prompt 为可选的自由描述提示。",
			InputSchema: understandVideoSchema(),
			Handler: func(ctx context.Context, args map[string]any) (*mcpkit.ToolResult, error) {
				return s.understandVideo(ctx, args)
			},
		},
		{
			Name:        "understand_audio",
			Description: "理解一段音频：按 task 执行转写/时间戳/说话人分离/翻译/分析等任务。audio 接受 url、data URI 或 file://本地路径；task 决定识别模式（asr/timed/diarize/translate/caption）；可配 language/source_lang/target_lang/prompt。",
			InputSchema: understandAudioSchema(),
			Handler: func(ctx context.Context, args map[string]any) (*mcpkit.ToolResult, error) {
				return s.understandAudio(ctx, args)
			},
		},
		{
			Name:        "understand_document",
			Description: "理解一份文档/PDF：模型把文档分页处理成多图后理解其中的文字、图片等信息（走视觉能力）。document 接受 url、data URI（data:application/pdf;base64,...）或 file://本地路径；prompt 为可选的自由描述提示。",
			InputSchema: understandDocumentSchema(),
			Handler: func(ctx context.Context, args map[string]any) (*mcpkit.ToolResult, error) {
				return s.understandDocument(ctx, args)
			},
		},
	}
}

// resourceRefSchema 资源引用参数（url / data URI / file:// 本地路径三态）。
func resourceRefSchema(desc string) map[string]any {
	return map[string]any{
		"type":        "string",
		"description": desc + "，支持 url、data URI（data:{mime};base64,...）或 file://本地路径",
	}
}

// understandImageSchema understand_image 工具参数：image/detail/prompt。
func understandImageSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"image": resourceRefSchema("图片资源"),
			"detail": map[string]any{
				"type":        "string",
				"enum":        []any{"low", "high", "xhigh"},
				"description": "图片精细度：low/high/xhigh（默认 high）",
			},
			"prompt": map[string]any{
				"type":        "string",
				"description": "可选的自由描述提示，告诉模型关注什么",
			},
		},
		"required": []any{"image"},
	}
}

// understandVideoSchema understand_video 工具参数：video/fps/prompt。
func understandVideoSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"video": resourceRefSchema("视频资源"),
			"fps": map[string]any{
				"type":        "number",
				"minimum":     0.2,
				"maximum":     5,
				"description": "抽帧频率（每秒帧数，0.2~5，默认 1）",
			},
			"prompt": map[string]any{
				"type":        "string",
				"description": "可选的自由描述提示",
			},
		},
		"required": []any{"video"},
	}
}

// understandAudioSchema understand_audio 工具参数：audio/task/language/source_lang/target_lang/prompt。
func understandAudioSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"audio": resourceRefSchema("音频资源"),
			"task": map[string]any{
				"type":        "string",
				"enum":        []any{"asr", "timed", "diarize", "translate", "caption"},
				"description": "音频任务：asr(普通转写)/timed(带时间戳)/diarize(多说话人)/translate(翻译)/caption(分析)",
			},
			"language": map[string]any{
				"type":        "string",
				"description": "音频语言（如 zh/en），缺省自动检测",
			},
			"source_lang": map[string]any{
				"type":        "string",
				"description": "源语言（translate 用）",
			},
			"target_lang": map[string]any{
				"type":        "string",
				"description": "目标语言（translate 用）",
			},
			"prompt": map[string]any{
				"type":        "string",
				"description": "可选的自由描述提示",
			},
		},
		"required": []any{"audio"},
	}
}

// understandDocumentSchema understand_document 工具参数：document/prompt。
func understandDocumentSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"document": resourceRefSchema("文档资源"),
			"prompt": map[string]any{
				"type":        "string",
				"description": "可选的自由描述提示，告诉模型关注文档的哪些内容",
			},
		},
		"required": []any{"document"},
	}
}
