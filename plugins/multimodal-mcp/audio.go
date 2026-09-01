package multimodalmcp

import (
	"context"
	"errors"
	"strings"

	"loadout/core/mcpkit"
)

// understandAudio 实现 understand_audio 工具：音频理解（task 决定识别模式）。
//   - args.audio 音频资源（url / data URI / file:// 本地路径，三态）。
//   - args.task  asr/timed/diarize/translate/caption；缺省用配置默认（默认 asr）。
//   - args.language 音频语言（可空，拼进 instructions）。
//   - args.source_lang / args.target_lang 翻译语种（translate 用）。
//   - args.prompt 可选自由描述提示；为空时按 task 生成默认提示。
//
// 走 chat/completions 协议（与图片/视频一致）：input_audio 块（data+format）+ text 块，
// 指令模板由 audioInstructions 按 task 选择并合并进文本提示。音频 >25MB 走上传（TODO），
// 否则 base64 内联。
func (s *Service) understandAudio(ctx context.Context, args map[string]any) (*mcpkit.ToolResult, error) {
	if err := s.checkEndpointEnabled(); err != nil {
		return nil, err
	}
	audioRef := strArg(args, "audio")
	if strings.TrimSpace(audioRef) == "" {
		return nil, errors.New("multimodal-mcp: understand_audio 缺少 audio 参数")
	}
	model, err := s.modelFor(ToolAudio)
	if err != nil {
		return nil, err
	}
	task := strArg(args, "task")
	if strings.TrimSpace(task) == "" {
		if d := s.defaultFor(ToolAudio, "task"); d != "" {
			task = d
		} else {
			task = "asr"
		}
	}
	language := strArg(args, "language")
	if strings.TrimSpace(language) == "" {
		if d := s.defaultFor(ToolAudio, "language"); d != "" {
			language = d
		}
	}
	srcLang := strArg(args, "source_lang")
	tgtLang := strArg(args, "target_lang")
	prompt := strArg(args, "prompt")

	// 资源三态解析：音频 >25MB 走 file_id（responses input_audio 支持 file_id）。
	// 注意：chat/completions 协议的 input_audio 只支持 data 内联，不支持 file_id，
	// 若大音频走了上传，此处返回明确错误提示改用 URL 或减小文件。
	url, fileID, err := s.resolveResource(ctx, audioRef, mimeByExt(strings.TrimPrefix(audioRef, "file://")), audioSizeLimit)
	if err != nil {
		return nil, err
	}
	if fileID != "" {
		return nil, errors.New("multimodal-mcp: 音频超过 base64 上限且当前协议的 input_audio 不支持 file_id，请改用公网 URL 或压缩音频")
	}
	mime := mimeByExt(strings.TrimPrefix(audioRef, "file://"))
	format := mimeToAudioFormat(mime)
	// 图片/视频走的 chat/completions 通道对音频同样可用，且当前渠道对 responses 的
	// input_audio 支持不佳（会报 content type not supported），故音频统一走 callChat。
	// chat 协议无顶层 instructions，把指令模板合并进用户文本提示。
	instructionNote := audioInstructions(task, language, srcLang, tgtLang)
	userPrompt := audioUserPrompt(task, prompt, srcLang, tgtLang)
	userText := userPrompt
	if strings.TrimSpace(instructionNote) != "" && strings.TrimSpace(userPrompt) != "" {
		userText = instructionNote + "\n\n" + userPrompt
	}
	blocks := []map[string]any{
		audioBlockChat(url, format),
		textBlock(userText),
	}
	return s.runRecognition(ctx, "understand_audio", "音频识别", model, map[string]any{
		"tool":        "understand_audio",
		"task":        task,
		"source":      classifySource(audioRef),
		"source_lang": srcLang,
		"target_lang": tgtLang,
	}, func() recognitionResult {
		text, reqLog, channel, err := s.callChat(ctx, model, blocks, callOpts{})
		return recognitionResult{text: text, reqLog: reqLog, channel: channel, err: err}
	})
}
