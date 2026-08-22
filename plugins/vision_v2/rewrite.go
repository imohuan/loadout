package visionv2

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

const placeholderPrefix = "<vision_img_"
const placeholderSuffix = ">"

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

// HandleProxyBeforeUpstream 主处理器（三格式）：
// 路由判断 → 图片替换占位符（落盘）→ 工具注入 → 写回 Body。无图/native/非视觉路径原样返回。
func (s *Service) HandleProxyBeforeUpstream(payload any) (any, error) {
	pipe, ok := payload.(*modelgateway.ProxyPipeline)
	if !ok || pipe == nil || pipe.Request == nil || len(pipe.Request.Body) == 0 {
		return payload, nil
	}
	format, ok := visionFormatByPath(pipe.Request.Path)
	if !ok {
		return payload, nil
	}
	// 路由判断（复制旧 DecideRouteScope 语义）
	scope := channelScopeFromMetadata(pipe.Metadata, s.requestChannelBaseURL)
	route, err := s.DecideRouteScope(pipe.Request.Model, scope)
	if err != nil {
		return nil, visionError(err.Error())
	}
	if route == nil || route.Route == types.RouteNative {
		return payload, nil // 原生视觉：透传
	}
	if route.Route == types.RouteError {
		return nil, visionError(fmt.Sprintf("模型 %q 不支持视觉能力", pipe.Request.Model))
	}

	// 图片 → 占位符
	var bodyMap map[string]any
	dec := json.NewDecoder(bytes.NewReader(pipe.Request.Body))
	dec.UseNumber()
	if err := dec.Decode(&bodyMap); err != nil {
		return payload, nil
	}
	messages := proxyMessageArray(bodyMap, format)
	if len(messages) == 0 {
		return payload, nil
	}
	changed, err := s.rewriteImagesToPlaceholders(context.Background(), messages, format)
	if err != nil {
		return nil, visionError(fmt.Sprintf("图片落盘失败: %v", err))
	}
	if !changed {
		return payload, nil
	}

	// 工具注入：按格式注入 look_at_image 工具
	bodyMap["tools"] = ensureLookAtImageTool(bodyMap["tools"], format)

	newBody, err := replaceMessagesBody(bodyMap, format, messages)
	if err != nil {
		return nil, visionError(err.Error())
	}
	pipe.Request.Body = newBody
	if pipe.Metadata != nil {
		pipe.Metadata["__vision_v2_active"] = true
		pipe.Metadata["__vision_v2_format"] = formatName(format)
		pipe.Metadata["__vision_v2_route"] = route
	}
	s.lg.Info("vision_v2: 图片替换为占位符", "path", pipe.Request.Path, "model", pipe.Request.Model)
	return pipe, nil
}

// rewriteImagesToPlaceholders 遍历消息 content，图片块 → <vision_img_{id}> 文本块。
func (s *Service) rewriteImagesToPlaceholders(ctx context.Context, messages []any, format visionProxyFormat) (bool, error) {
	changed := false
	for mi := range messages {
		msg, ok := messages[mi].(map[string]any)
		if !ok {
			continue
		}
		content, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		for ci := range content {
			part, ok := content[ci].(map[string]any)
			if !ok {
				continue
			}
			var img string
			switch format {
			case formatChat:
				if t, _ := part["type"].(string); t == "image_url" {
					img = imageURLValue(part["image_url"])
				}
			case formatClaude:
				if t, _ := part["type"].(string); t == "image" {
					img = claudeImageValue(part["source"])
				}
			case formatResponses:
				if t, _ := part["type"].(string); t == "input_image" {
					img = imageURLValue(part["image_url"])
				}
			}
			if img == "" {
				continue
			}
			var id string
			var err error
			if strings.HasPrefix(img, "data:") {
				id, err = s.SaveImageDataURI(img)
			} else if strings.HasPrefix(img, "http://") || strings.HasPrefix(img, "https://") {
				id, err = s.SaveImageURL(ctx, img)
			} else {
				continue // 未知形式：保留原块
			}
			if err != nil {
				s.lg.Warn("图片落盘失败，保留原块", "err", err)
				continue
			}
			content[ci] = map[string]any{"type": textBlockType(format), "text": placeholderPrefix + id + placeholderSuffix}
			changed = true
		}
	}
	return changed, nil
}

// formatName 格式枚举 → 字符串（metadata 标记用）。
func formatName(format visionProxyFormat) string {
	switch format {
	case formatChat:
		return "chat"
	case formatClaude:
		return "claude"
	case formatResponses:
		return "responses"
	}
	return "unknown"
}
