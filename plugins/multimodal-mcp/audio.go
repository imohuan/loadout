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
// 走 responses 协议：input_audio 块 + input_text 块，顶层 instructions 由 audioInstructions
// 按 task 选择。音频 >25MB 走 file_id（上传 TODO），否则 base64/URL。
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
	url, fileID, err := s.resolveResource(ctx, audioRef, mimeByExt(strings.TrimPrefix(audioRef, "file://")), audioSizeLimit)
	if err != nil {
		return nil, err
	}
	blocks := []map[string]any{
		audioBlockResponses(url, fileID),
		inputTextBlockResponses(audioUserPrompt(task, prompt, srcLang, tgtLang)),
	}
	instructions := audioInstructions(task, language, srcLang, tgtLang)
	return s.runRecognition(ctx, "understand_audio", "音频识别", model, map[string]any{
		"tool":       "understand_audio",
		"task":       task,
		"source":     classifySource(audioRef),
		"source_lang": srcLang,
		"target_lang": tgtLang,
	}, func() recognitionResult {
		text, reqLog, channel, err := s.callResponses(ctx, model, blocks, instructions, callOpts{})
		return recognitionResult{text: text, reqLog: reqLog, channel: channel, err: err}
	})
}
