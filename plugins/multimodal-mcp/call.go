package multimodalmcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	modelgateway "loadout/plugins/model-gateway"
)

// callOpts 识别子请求的附加选项（当前为路由元数据扩展预留）。
type callOpts struct {
	// parentRequestID 主请求关联（供 request-log / UI 关联展示），可空。
	parentRequestID string
	// channelCandidates 候选渠道 id 列表（空 = 按 model 自动路由）。
	channelCandidates []string
}

// imageBlock 构造图片块：{"type":"image_url","image_url":{"url":"...","detail":"..."}}。
func imageBlock(url, detail string) map[string]any {
	iu := map[string]any{"url": url}
	if detail != "" {
		iu["detail"] = detail
	}
	return map[string]any{"type": "image_url", "image_url": iu}
}

// videoBlock 构造视频块：{"type":"video_url","video_url":{"url":"...","fps":fps}}。
func videoBlock(url string, fps any) map[string]any {
	vu := map[string]any{"url": url}
	if fps != nil {
		vu["fps"] = fps
	}
	return map[string]any{"type": "video_url", "video_url": vu}
}

// textBlock 构造文本块：{"type":"text","text":"..."}。
func textBlock(text string) map[string]any {
	return map[string]any{"type": "text", "text": text}
}

// audioBlockResponses 构造 responses 协议的音频块：
// 有 file_id 用 {"type":"input_audio","file_id":"..."}；否则 {"type":"input_audio","audio_url":"..."}。
func audioBlockResponses(url, fileID string) map[string]any {
	blk := map[string]any{"type": "input_audio"}
	if fileID != "" {
		blk["file_id"] = fileID
	} else {
		blk["audio_url"] = url
	}
	return blk
}

// audioBlockChat 构造 chat/completions 协议的音频块（与图片/视频走同一条通道）：
// {"type":"input_audio","input_audio":{"data":"<base64>","format":"mp3"}}。
// url 是 base64 data URI（data:audio/wav;base64,...）；format 从 mime 提取（wav/mp3/m4a 等）。
// chat 协议的 input_audio 只支持 data 内联，不支持 file_id；大音频若走了上传则返回错误。
func audioBlockChat(url, format string) map[string]any {
	return map[string]any{
		"type": "input_audio",
		"input_audio": map[string]any{
			"data":   url,
			"format": format,
		},
	}
}

// mimeToAudioFormat 把音频 mime 类型映射成 chat/completions 的 input_audio.format 取值。
// 识别不到时回退 "mp3"（方舟文档默认示例格式）。
func mimeToAudioFormat(mime string) string {
	switch strings.ToLower(mime) {
	case "audio/wav":
		return "wav"
	case "audio/mpeg", "audio/mp3":
		return "mp3"
	case "audio/mp4", "audio/m4a":
		return "m4a"
	case "audio/aac":
		return "aac"
	case "audio/ogg", "audio/oga":
		return "ogg"
	case "audio/flac":
		return "flac"
	case "audio/amr":
		return "amr"
	default:
		return "mp3"
	}
}

// inputTextBlockResponses 构造 responses 协议的文本块：{"type":"input_text","text":"..."}。
func inputTextBlockResponses(text string) map[string]any {
	return map[string]any{"type": "input_text", "text": text}
}

// callChat 走 chat/completions 协议识别（Path="chat/completions"）：
// content 是块数组（image_url / video_url / text），返回识别文本
// （解析 choices[0].message.content）。非流式（streamWriter=nil）拿完整 body。
// 返回 (text, reqLogID, channelID, err)：reqLogID 为子请求在 request-log 的关联
// UUID（供 route-log 关联），channelID 为成功渠道 id（供 route-log 展示）。
func (s *Service) callChat(ctx context.Context, model string, contentBlocks []map[string]any, opts callOpts) (text, reqLogID, channelID string, err error) {
	payload := map[string]any{
		"model": model,
		"messages": []any{map[string]any{
			"role":    "user",
			"content": contentBlocks,
		}},
		"stream": false,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", "", "", fmt.Errorf("multimodal-mcp: 序列化 chat 请求失败: %w", err)
	}
	pipe := &modelgateway.ProxyPipeline{
		Request: &modelgateway.ProxyRequest{
			Method: "POST",
			Path:   "chat/completions",
			Header: http.Header{"Content-Type": []string{"application/json"}},
			Body:   body,
			Model:  model,
			Stream: false,
		},
		Metadata: map[string]any{},
	}
	applyOpts(pipe, opts)
	final, respBody, err := s.gw.ForwardSubRequest(ctx, pipe, nil)
	reqLogID = extractReqLogID(final)
	channelID = extractLastChannel(final)
	if err != nil {
		return "", reqLogID, channelID, err
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", reqLogID, channelID, fmt.Errorf("multimodal-mcp: 解析 chat 识别响应失败: %w", err)
	}
	if len(parsed.Choices) == 0 || strings.TrimSpace(parsed.Choices[0].Message.Content) == "" {
		return "", reqLogID, channelID, errors.New("multimodal-mcp: 模型未返回识别文本")
	}
	return parsed.Choices[0].Message.Content, reqLogID, channelID, nil
}

// callResponses 走 responses 协议识别（Path="responses"）：
// content 是 input 里 user 消息的块数组（input_audio / input_text），
// instructions 为顶层提示词（音频 task 模板）。返回识别文本（解析 output 的 output_text）。
func (s *Service) callResponses(ctx context.Context, model string, contentBlocks []map[string]any, instructions string, opts callOpts) (text, reqLogID, channelID string, err error) {
	payload := map[string]any{
		"model": model,
		"input": []any{map[string]any{
			"role":    "user",
			"content": contentBlocks,
		}},
		"stream": false,
	}
	if strings.TrimSpace(instructions) != "" {
		payload["instructions"] = instructions
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", "", "", fmt.Errorf("multimodal-mcp: 序列化 responses 请求失败: %w", err)
	}
	pipe := &modelgateway.ProxyPipeline{
		Request: &modelgateway.ProxyRequest{
			Method: "POST",
			Path:   "responses",
			Header: http.Header{"Content-Type": []string{"application/json"}},
			Body:   body,
			Model:  model,
			Stream: false,
		},
		Metadata: map[string]any{},
	}
	applyOpts(pipe, opts)
	final, respBody, err := s.gw.ForwardSubRequest(ctx, pipe, nil)
	reqLogID = extractReqLogID(final)
	channelID = extractLastChannel(final)
	if err != nil {
		return "", reqLogID, channelID, err
	}
	// responses 输出：output 数组里的 message.output_text 文本。
	var parsed struct {
		Output []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", reqLogID, channelID, fmt.Errorf("multimodal-mcp: 解析 responses 识别响应失败: %w", err)
	}
	for _, item := range parsed.Output {
		for _, c := range item.Content {
			if c.Type == "output_text" && strings.TrimSpace(c.Text) != "" {
				return c.Text, reqLogID, channelID, nil
			}
		}
	}
	return "", reqLogID, channelID, errors.New("multimodal-mcp: 模型未返回识别文本")
}

// applyOpts 把 callOpts 落到子请求 pipe 的 metadata。
func applyOpts(pipe *modelgateway.ProxyPipeline, opts callOpts) {
	if opts.parentRequestID != "" {
		pipe.Metadata["__parent_request_id"] = opts.parentRequestID
	}
	if len(opts.channelCandidates) > 0 {
		pipe.Metadata["__channel_candidates"] = opts.channelCandidates
	}
}

// extractReqLogID 从子请求 final pipe 提取 request-log 关联 UUID（__request_log_attempt_id）。
// final 为 nil 时返回空串。
func extractReqLogID(final *modelgateway.ProxyPipeline) string {
	if final == nil {
		return ""
	}
	if v, _ := final.Metadata[modelgateway.MetadataRequestLogAttemptID].(string); v != "" {
		return v
	}
	return ""
}

// extractLastChannel 从子请求 final pipe 提取最后尝试的渠道 id（__last_tried_channel）。
func extractLastChannel(final *modelgateway.ProxyPipeline) string {
	if final == nil {
		return ""
	}
	if v, _ := final.Metadata["__last_tried_channel"].(string); v != "" {
		return v
	}
	return ""
}

// ===== 资源三态处理（url / data URI / file:// 本地路径）=====

// classifySource 判断资源引用的来源类型（供 route-log metadata 展示）：http/data/file/raw。
func classifySource(ref string) string {
	ref = strings.TrimSpace(ref)
	switch {
	case strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://"):
		return "url"
	case strings.HasPrefix(ref, "data:"):
		return "data"
	case strings.HasPrefix(ref, "file://"):
		return "file"
	default:
		return "raw"
	}
}

// resolveResource 把资源的三种输入形态解析成请求可直接引用的形式：
//   - http(s) URL：原样返回（url 使用，fileID 空）；
//   - data URI：原样返回；
//   - file:// 本地路径：读文件。大小 ≤ sizeLimit → base64 转 data URI；
//     大小 > sizeLimit → 调上传（s.uploadAndGetID，由子代理C实现）拿 file_id。
//
// 返回 (url, fileID, error)：url 用于 image_url/video_url/audio_url 的 url 字段；
// fileID 用于 responses 的 input_audio/file_id 等字段。两者至多一个非空。
// mime 是本地文件 base64 编码时需要的 media type（如 image/jpeg、video/mp4、audio/mpeg），
// 由调用方按资源类型传入（可用 http.DetectContentType 兜底）。
func (s *Service) resolveResource(ctx context.Context, ref, defaultMime string, sizeLimit int64) (url, fileID string, err error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", errors.New("multimodal-mcp: 资源引用为空")
	}
	// 1. data URI：原样返回。
	if strings.HasPrefix(ref, "data:") {
		return ref, "", nil
	}
	// 2. http(s) URL：原样返回。
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref, "", nil
	}
	// 3. file:// 本地路径。
	var localPath string
	switch {
	case strings.HasPrefix(ref, "file://"):
		localPath = strings.TrimPrefix(ref, "file://")
		if u := strings.TrimPrefix(localPath, "localhost"); u != localPath {
			localPath = strings.TrimPrefix(u, "/")
		}
	default:
		// 兜底：既非 URL 也非 data URI 也非 file://，视为本地裸路径。
		localPath = ref
	}
	info, statErr := os.Stat(localPath)
	if statErr != nil {
		return "", "", fmt.Errorf("multimodal-mcp: 读取本地资源失败: %w", statErr)
	}
	raw, readErr := os.ReadFile(localPath)
	if readErr != nil {
		return "", "", fmt.Errorf("multimodal-mcp: 读取本地资源失败: %w", readErr)
	}
	mime := defaultMime
	if strings.TrimSpace(mime) == "" {
		mime = http.DetectContentType(raw)
	}
	// 大文件走上传（file_id），小文件 base64 内联。
	if info.Size() > sizeLimit {
		// uploadAndGetID 由上传子代理（子代理C）实现：方舟 Files API 上传 + 轮询 active，
		// 接收媒体类型、文件字节与文件名。
		fid, uerr := s.uploadAndGetID(ctx, mime, raw, filepath.Base(localPath))
		if uerr != nil {
			return "", "", fmt.Errorf("multimodal-mcp: 资源超过 %d 字节需走上传，但上传失败: %w", sizeLimit, uerr)
		}
		return "", fid, nil
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw), "", nil
}

// mimeByExt 按扩展名推断媒体类型（图片/视频/音频），用于本地文件 base64 data URI。
// 识别不到时返回空串（调用方用 DetectContentType 兜底）。
func mimeByExt(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".mov":
		return "video/quicktime"
	case ".mkv":
		return "video/x-matroska"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".m4a":
		return "audio/mp4"
	case ".aac":
		return "audio/aac"
	case ".ogg", ".oga":
		return "audio/ogg"
	case ".flac":
		return "audio/flac"
	case ".wma":
		return "audio/x-ms-wma"
	case ".amr":
		return "audio/amr"
	default:
		return ""
	}
}

// sizeThresholds 各资源 base64 内联上限（图片 10MB / 视频 50MB / 音频 25MB，方舟文档）。
const (
	imageSizeLimit = 10 << 20 // 10MB
	videoSizeLimit = 50 << 20 // 50MB
	audioSizeLimit = 25 << 20 // 25MB
)
