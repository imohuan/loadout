package multimodalmcp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"loadout/core/mcpkit"
)

// understandVideo 实现 understand_video 工具：视频理解（抽帧 + 时序感知）。
//   - args.video 视频资源（url / data URI / file:// 本地路径，三态）。
//   - args.fps   抽帧频率（0.2~5）；缺省用配置默认（默认 1）。
//   - args.prompt 可选自由描述提示；缺省用内置默认提示词。
//
// 走 chat/completions 协议：video_url 块（含 fps）+ text 块。
// 视频 >50MB 走 file_id（上传 TODO），否则 base64/URL。
func (s *Service) understandVideo(ctx context.Context, args map[string]any) (*mcpkit.ToolResult, error) {
	videoRef := strArg(args, "video")
	if strings.TrimSpace(videoRef) == "" {
		return nil, errors.New("multimodal-mcp: understand_video 缺少 video 参数")
	}
	model, err := s.modelFor(ToolVideo)
	if err != nil {
		return nil, err
	}
	fps := 1.0
	if v, ok := floatArg(args, "fps"); ok {
		fps = v
	} else if d := s.defaultFor(ToolVideo, "fps"); d != "" {
		if f, ok := parseFloat(d); ok {
			fps = f
		}
	}
	if fps < 0.2 || fps > 5 {
		fps = 1 // schema 已约束 0.2~5，越界兜底用默认
	}
	prompt := strArg(args, "prompt")
	if strings.TrimSpace(prompt) == "" {
		prompt = defaultVideoPrompt
	}

	url, fileID, err := s.resolveResource(ctx, videoRef, mimeByExt(strings.TrimPrefix(videoRef, "file://")), videoSizeLimit)
	if err != nil {
		return nil, err
	}
	_ = fileID // 视频走 chat/completions 的 video_url，file_id 协议（responses input_video）后续再定
	blocks := []map[string]any{
		videoBlock(url, fps),
		textBlock("识别方向: " + prompt),
	}
	text, err := s.callChat(ctx, model, blocks, callOpts{})
	if err != nil {
		return nil, err
	}
	return textResult(text), nil
}

// parseFloat 解析字符串为 float64。
func parseFloat(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	var f float64
	if _, err := fmt.Sscanf(s, "%g", &f); err == nil {
		return f, true
	}
	return 0, false
}
