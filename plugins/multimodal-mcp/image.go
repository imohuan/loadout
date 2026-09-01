package multimodalmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"loadout/core/mcpkit"
)

// understandImage 实现 understand_image 工具：图片理解。
//   - args.image  图片资源（url / data URI / file:// 本地路径，三态）。
//   - args.detail 精细度（low/high/xhigh）；缺省用配置默认（默认 high）。
//   - args.prompt 可选自由描述提示；缺省用内置默认提示词。
//
// 走 chat/completions 协议：image_url 块（含 detail）+ text 块，经 s.gw 子请求通道识别。
// 资源三态由 resolveResource 处理：http URL / data URI 直传，file:// 小文件转 base64
// data URI、大文件走上传拿 file_id（首版图片若上传未实现则报错）。
func (s *Service) understandImage(ctx context.Context, args map[string]any) (*mcpkit.ToolResult, error) {
	imageRef := strArg(args, "image")
	if strings.TrimSpace(imageRef) == "" {
		return nil, errors.New("multimodal-mcp: understand_image 缺少 image 参数")
	}
	model, err := s.modelFor(ToolImage)
	if err != nil {
		return nil, err
	}
	detail := strArg(args, "detail")
	if strings.TrimSpace(detail) == "" {
		if d := s.defaultFor(ToolImage, "detail"); d != "" {
			detail = d
		} else {
			detail = "high"
		}
	}
	prompt := strArg(args, "prompt")
	if strings.TrimSpace(prompt) == "" {
		prompt = defaultImagePrompt
	}

	// 资源三态解析：url 或 data URI 直传；file:// 大文件走上传（图片走 file_id 的协议
	// 后续再定，首版若上传失败则直接报错）。
	url, fileID, err := s.resolveResource(ctx, imageRef, mimeByExt(strings.TrimPrefix(imageRef, "file://")), imageSizeLimit)
	if err != nil {
		return nil, err
	}
	_ = fileID // 图片走 chat/completions 的 image_url，不用 responses 的 input_image file_id（后续协议再定）
	blocks := []map[string]any{
		imageBlock(url, detail),
		textBlock("识别方向: " + prompt),
	}
	text, err := s.callChat(ctx, model, blocks, callOpts{})
	if err != nil {
		return nil, err
	}
	return textResult(text), nil
}

// modelFor 取指定工具的内置模型名（配置里按 Kind 匹配；未配置则报错）。
func (s *Service) modelFor(kind ToolKind) (string, error) {
	cfg, err := s.loadConfig()
	if err != nil {
		return "", fmt.Errorf("multimodal-mcp: 读取配置失败: %w", err)
	}
	for _, t := range cfg.Tools {
		if t.Kind == kind && t.Enabled && strings.TrimSpace(t.Model) != "" {
			return t.Model, nil
		}
	}
	return "", fmt.Errorf("multimodal-mcp: 工具 %s 未配置可用模型或已禁用", kind)
}

// defaultFor 取指定工具配置里的默认参数值（Defaults map）。
func (s *Service) defaultFor(kind ToolKind, key string) string {
	cfg, err := s.loadConfig()
	if err != nil {
		return ""
	}
	for _, t := range cfg.Tools {
		if t.Kind == kind {
			if v, ok := t.Defaults[key]; ok {
				return fmt.Sprintf("%v", v)
			}
		}
	}
	return ""
}

// strArg 从 args 里取字符串参数（nil 安全，空值返回 ""）。
func strArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	switch v := args[key].(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", v)
	}
}

// floatArg 从 args 里取 float 参数（number / 数字字符串）。
func floatArg(args map[string]any, key string) (float64, bool) {
	if args == nil {
		return 0, false
	}
	switch v := args[key].(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case string:
		var f float64
		if _, err := fmt.Sscanf(v, "%g", &f); err == nil {
			return f, true
		}
	}
	return 0, false
}

// textResult 构造纯文本成功的工具结果。
func textResult(text string) *mcpkit.ToolResult {
	return &mcpkit.ToolResult{
		Content: []mcpkit.ContentPart{{Type: "text", Text: text}},
	}
}
