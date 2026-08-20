package vision

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

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
// 同时记录图片块位置与所在消息角色，供 rewriteProxyImages 精准替换与位置分级。
type proxyImageHit struct {
	msgIdx     int
	contentIdx int
	role       string
}

// proxyMessageRole 提取消息角色：chat/claude/responses 的 role 字段都在消息顶层
// （responses 的 message item 为 {"type":"message","role":"user","content":[...]}）。
func proxyMessageRole(msg map[string]any) string {
	if r, ok := msg["role"].(string); ok {
		return r
	}
	return ""
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
				hits = append(hits, proxyImageHit{msgIdx: mi, contentIdx: ci, role: proxyMessageRole(msg)})
			}
		}
	}
	return images, hits
}

// proxyImageGroup 一组同消息的图片及替换文本。
type proxyImageGroup struct {
	images []string
	hits   []proxyImageHit
	text   string
}

// splitProxyGroups 按消息位置分组：最后一条 user 消息的图片为新图组（需要识别），
// 其余消息按消息分别成旧图组（只读缓存/占位符，不调视觉模型）。
// keep 模式全部归新图组（保持现状行为）。
func splitProxyGroups(messages []any, images []string, hits []proxyImageHit) (proxyImageGroup, []proxyImageGroup) {
	if config.VisionHistoryMode == "keep" {
		return proxyImageGroup{images: images, hits: hits}, nil
	}
	lastUserIdx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if msg, ok := messages[i].(map[string]any); ok && proxyMessageRole(msg) == "user" {
			lastUserIdx = i
			break
		}
	}
	var newGroup proxyImageGroup
	oldByMsg := make(map[int]*proxyImageGroup)
	var oldOrder []int
	for i, h := range hits {
		if h.msgIdx == lastUserIdx {
			newGroup.images = append(newGroup.images, images[i])
			newGroup.hits = append(newGroup.hits, h)
			continue
		}
		g := oldByMsg[h.msgIdx]
		if g == nil {
			g = &proxyImageGroup{}
			oldByMsg[h.msgIdx] = g
			oldOrder = append(oldOrder, h.msgIdx)
		}
		g.images = append(g.images, images[i])
		g.hits = append(g.hits, h)
	}
	var oldGroups []proxyImageGroup
	for _, idx := range oldOrder {
		oldGroups = append(oldGroups, *oldByMsg[idx])
	}
	return newGroup, oldGroups
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
	// 分级：新图（最后一条 user 消息）完整识别；历史旧图只读缓存/占位符（不联网）。
	normalizeVisionHistoryMode(s.lg)
	newGroup, oldGroups := splitProxyGroups(messages, images, hits)
	// 仅当有新图需要识别（newGroup.hits>0）时才写视觉日志；纯历史旧图不记录，
	// 也不占用 step 空间（主链路保持从 1 开始）。
	visionLogging := len(newGroup.hits) > 0
	var lastErr error
	visionStep := 0 // 视觉 attempt 已使用的最大 step_no（与主链路共享单调递增空间）
	channels := s.loadChannels(context.Background())
	for idx, opt := range options {
		viaModel := opt.ViaModel
		if viaModel == "" {
			viaModel = config.DefaultVisionModel
		}
		attemptStart := time.Now()
		// 视觉 attempt 与主链路共享同一单调递增 step 空间（1, 2, 3...），按发生顺序排列。
		// 视觉循环结束后把视觉最后 step 写入 pipe.Metadata["__route_step"]，主链路
		// 从该值 +1 续接，保证全请求 step 连续且唯一。
		stepNo := idx + 1
		if visionLogging {
			visionStep = stepNo
			// 两阶段占位：识别开始即写 running，UI 在识别期间（可能数十秒）就能看到。
			s.visionAttempt(context.Background(), pipe.RequestID, stepNo, viaModel, attemptStart, 0, "running", "", "", len(newGroup.images))
		}
		// 旧图组先解析（只读缓存/占位符；key 与 Describe 一致，首轮缓存第二轮可命中）。
		for i := range oldGroups {
			oldGroups[i].text = s.resolveHistoryImages(oldGroups[i].images, viaModel)
		}
		// 仅新图启用流式「图片理解」输出（历史旧图不产生影子）。
		// 流式请求（pipe.StreamWriter 非 nil）时视觉识别过程实时输出到对话流
		// （reasoning_content SSE 块），识别完成后再改写请求体转发主模型。
		// 非流式请求保持原行为：识别结果直接替换进请求体。
		var streamWriter func(string) error
		if len(newGroup.hits) > 0 && pipe.StreamWriter != nil {
			// 首次 delta 前插入前缀，标记这段输出为图片理解。
			first := true
			base := pipe.StreamWriter
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
		var newText string
		var chID string
		if len(newGroup.hits) > 0 {
			newText, chID, err = s.describeViaOption(context.Background(), newGroup.images, opt, config.DefaultVisionModel, channels, streamWriter)
			if err != nil {
				lastErr = err
				s.lg.Warn("视觉候选失败，尝试下一个", "via_model", viaModel, "err", err)
				// 更新占位为 failed：让前端看到「尝试了 doubao 失败、然后 qwen3 成功」全貌。
				if visionLogging {
					s.visionAttempt(context.Background(), pipe.RequestID, stepNo, viaModel, attemptStart, time.Since(attemptStart), "failed", "", err.Error(), len(newGroup.images))
				}
				continue
			}
		}
		// 逐组改写：新图组替换为识别文本；各旧图组替换为缓存描述或占位符。
		if len(newGroup.hits) > 0 {
			rewriteProxyImages(messages, format, newGroup.hits, newText)
		}
		for _, g := range oldGroups {
			rewriteProxyImages(messages, format, g.hits, g.text)
		}
		body, err := replaceMessagesBody(bodyMap, format, messages)
		if err != nil {
			return nil, visionError(fmt.Sprintf("改写请求体失败: %v", err))
		}
		pipe.Request.Body = body
		// 识别成功：更新占位为 success（缓存命中时 chID 为空）。
		if visionLogging {
			s.visionAttempt(context.Background(), pipe.RequestID, stepNo, viaModel, attemptStart, time.Since(attemptStart), "success", chID, "", len(newGroup.images))
			// 视觉最后 step 写入 Metadata，主链路从 visionStep+1 续接。
			if pipe.Metadata != nil {
				pipe.Metadata["__route_step"] = visionStep
			}
		}
		return pipe, nil
	}
	// 循环结束：全部候选失败场景。仅在视觉写了 attempt 时把最后 step 写入（主链路续接）；
	// 纯历史旧图（visionLogging=false）不写，主链路保持从 1 开始。
	if visionLogging && pipe.Metadata != nil {
		pipe.Metadata["__route_step"] = visionStep
	}
	// 全部候选失败：每条候选的 running→failed 已在循环内更新，无需汇总记录。
	return nil, visionError(fmt.Sprintf("视觉能力调用失败: %v", lastErr))
}
