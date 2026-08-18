package vision

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"loadout/core/config"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// visionProxyFormat 描述一种可被视觉能力改写的消息格式。
type visionProxyFormat int

// visionStreamPrefix 视觉流输出前缀：标记这段输出为图片理解（客户端据此渲染）。
const visionStreamPrefix = "> **图片理解** : "

const (
	formatUnknown   visionProxyFormat = iota
	formatChat                        // chat/completions：messages，图片块 image_url
	formatClaude                      // /v1/messages：messages，图片块 image（source）
	formatResponses                   // /v1/responses：input，图片块 input_image
)

// visionFormatByPath 按剩余路径识别消息格式。
func visionFormatByPath(path string) (visionProxyFormat, bool) {
	switch path {
	case "chat/completions":
		return formatChat, true
	case "messages":
		return formatClaude, true
	case "responses":
		return formatResponses, true
	}
	return formatUnknown, false
}

// proxyMessageArray 从解析后的 body map 取消息数组（messages 或 input）。
func proxyMessageArray(m map[string]any, format visionProxyFormat) []any {
	switch format {
	case formatResponses:
		if input, ok := m["input"].([]any); ok {
			return input
		}
	default:
		if msgs, ok := m["messages"].([]any); ok {
			return msgs
		}
	}
	return nil
}

// proxyImage 提取单个 content 块里的图片信息。
// image 为 URL 或 data URI（claude 的 base64 source 会转成 data URI）。
type proxyImage struct {
	image string
}

// detectProxyImages 遍历消息数组，检出所有图片块，返回图片列表（URL/data URI）。
// 同时记录图片块位置，供 rewriteProxyImages 精准替换。
type proxyImageHit struct {
	msgIdx     int
	contentIdx int
}

func detectProxyImages(messages []any, format visionProxyFormat) ([]string, []proxyImageHit) {
	var images []string
	var hits []proxyImageHit
	for mi, rawMsg := range messages {
		msg, ok := rawMsg.(map[string]any)
		if !ok {
			continue
		}
		content, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		for ci, rawPart := range content {
			part, ok := rawPart.(map[string]any)
			if !ok {
				continue
			}
			typ, _ := part["type"].(string)
			var img string
			switch format {
			case formatChat:
				if typ == "image_url" {
					img = imageURLValue(part["image_url"])
				}
			case formatClaude:
				if typ == "image" {
					img = claudeImageValue(part["source"])
				}
			case formatResponses:
				if typ == "input_image" {
					img = imageURLValue(part["image_url"])
				}
			}
			if img != "" {
				images = append(images, img)
				hits = append(hits, proxyImageHit{msgIdx: mi, contentIdx: ci})
			}
		}
	}
	return images, hits
}

// imageURLValue 提取 image_url 字段：兼容字符串或 {"url": "..."} 对象。
func imageURLValue(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case map[string]any:
		if u, ok := val["url"].(string); ok {
			return u
		}
	}
	return ""
}

// claudeImageValue 提取 claude image 块的 source：base64 data 转 data URI，url 原样。
func claudeImageValue(v any) string {
	src, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	typ, _ := src["type"].(string)
	switch typ {
	case "base64":
		data, _ := src["data"].(string)
		media, _ := src["media_type"].(string)
		if data == "" {
			return ""
		}
		if media == "" {
			media = "image/png"
		}
		return "data:" + media + ";base64," + data
	case "url":
		u, _ := src["url"].(string)
		return u
	}
	return ""
}

// textBlockType 各格式文本块的 type 名。
func textBlockType(format visionProxyFormat) string {
	if format == formatResponses {
		return "input_text"
	}
	return "text"
}

// rewriteProxyImages 把命中的图片块改写：第一张图片替换为文字描述块，
// 其余图片块从 content 中删除（避免 N 张图 → N 份重复描述膨胀 prompt）。
func rewriteProxyImages(messages []any, format visionProxyFormat, hits []proxyImageHit, text string) []any {
	if len(hits) == 0 {
		return messages
	}
	// 先删除后续图片块（从后往前，避免索引偏移）。
	for i := len(hits) - 1; i >= 1; i-- {
		h := hits[i]
		msg, ok := messages[h.msgIdx].(map[string]any)
		if !ok {
			continue
		}
		content, ok := msg["content"].([]any)
		if !ok || h.contentIdx >= len(content) {
			continue
		}
		content = append(content[:h.contentIdx], content[h.contentIdx+1:]...)
		msg["content"] = content
	}
	// 第一张图片替换为描述文本块。
	h := hits[0]
	if msg, ok := messages[h.msgIdx].(map[string]any); ok {
		if content, ok := msg["content"].([]any); ok && h.contentIdx < len(content) {
			content[h.contentIdx] = map[string]any{"type": textBlockType(format), "text": text}
		}
	}
	return messages
}

// replaceMessagesBody 把改写后的消息数组写回 body（其他字段原样保留）。
func replaceMessagesBody(m map[string]any, format visionProxyFormat, messages []any) ([]byte, error) {
	switch format {
	case formatResponses:
		m["input"] = messages
	default:
		m["messages"] = messages
	}
	return json.Marshal(m)
}

// HandleProxyBeforeUpstream 透明代理输入 hook：在原始请求体上检测图片并替换。
// 仅处理三种对话格式（chat/completions、messages、responses），其他路径原样透传。
// 解析失败或无图时也原样透传，绝不阻塞代理链路。
func (s *Service) HandleProxyBeforeUpstream(payload any) (any, error) {
	pipe, ok := payload.(*modelgateway.ProxyPipeline)
	if !ok || pipe == nil || pipe.Request == nil {
		return payload, nil
	}
	format, ok := visionFormatByPath(pipe.Request.Path)
	if !ok || len(pipe.Request.Body) == 0 {
		return payload, nil
	}

	var bodyMap map[string]any
	dec := json.NewDecoder(bytes.NewReader(pipe.Request.Body))
	dec.UseNumber()
	if err := dec.Decode(&bodyMap); err != nil {
		s.lg.Debug("vision: 请求体非 JSON，跳过视觉处理", "path", pipe.Request.Path)
		return payload, nil
	}
	messages := proxyMessageArray(bodyMap, format)
	if len(messages) == 0 {
		return payload, nil
	}
	images, hits := detectProxyImages(messages, format)
	if len(images) == 0 {
		return payload, nil
	}

	model := pipe.Request.Model
	s.lg.Info("检测到图片", "path", pipe.Request.Path, "model", model, "图片数", len(images))
	// 渠道约束：aggregate 先于本插件执行并写入 __current_channel（聚合模型指定渠道）；
	// 普通请求渠道未知（空），仅全渠道路由命中。
	channelID, _ := pipe.Metadata["__current_channel"].(string)
	route, err := s.DecideRoute(model, channelID)
	if err != nil {
		return nil, visionError(err.Error())
	}
	if route == nil || route.Route == types.RouteNative {
		return payload, nil
	}
	if route.Route == types.RouteError {
		return nil, visionError(fmt.Sprintf("模型 %q 不支持视觉能力", model))
	}

	// proxy：按 via_options 依次尝试视觉兜底（视觉模型 + 可选渠道，失败换下一个）。
	options := route.ViaOptions
	if len(options) == 0 {
		options = []types.ViaOption{{ViaModel: config.DefaultVisionModel}}
	}
	var lastErr error
	for _, opt := range options {
		viaModel := opt.ViaModel
		if viaModel == "" {
			viaModel = config.DefaultVisionModel
		}
	// 流式请求（pipe.StreamWriter 非 nil）时视觉识别过程实时输出到对话流
	// （reasoning_content SSE 块），识别完成后再改写请求体转发主模型。
	// 非流式请求保持原行为：识别结果直接替换进请求体。
	streamWriter := pipe.StreamWriter
	if streamWriter != nil {
		// 首次 delta 前插入前缀，标记这段输出为图片理解。
		first := true
		base := streamWriter
		streamWriter = func(delta string) error {
			if first {
				first = false
				if err := base(visionStreamPrefix); err != nil {
					return err
				}
			}
			return base(delta)
		}
	}
	text, err := s.Describe(context.Background(), images, viaModel, opt.ChannelID, streamWriter)
		if err != nil {
			lastErr = err
			s.lg.Warn("视觉候选失败，尝试下一个", "via_model", viaModel, "err", err)
			continue
		}
		rewriteProxyImages(messages, format, hits, text)
		body, err := replaceMessagesBody(bodyMap, format, messages)
		if err != nil {
			return nil, visionError(fmt.Sprintf("改写请求体失败: %v", err))
		}
		pipe.Request.Body = body
		return pipe, nil
	}
	return nil, visionError(fmt.Sprintf("视觉能力调用失败: %v", lastErr))
}
