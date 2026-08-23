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
	scope := channelScopeFromMetadata(pipe.Metadata, s.requestChannelBaseURLs)
	route, err := s.DecideRouteScope(pipe.Request.Model, scope)
	if err != nil {
		return nil, visionError(err.Error())
	}
	// 非 proxy（native / 历史 error 降级）：原样透传。
	// failover 防御：若前一个 attempt 走 proxy 已改写 body（图片→占位符）并置
	// __vision_v2_active，本 attempt 命中 native 时若残留该标记，后续
	// StreamChunk/AfterUpstream 会误把 native 透传流当视觉请求拦截。这里清除标记，
	// 并放行透传（body 占位符已无法还原，属罕见「先 proxy 后 native」failover 的已知
	// 限制：native 模型收到占位符文本而非原图，见 plan 2026-08-23-vision-v2-route-context-fix.md）。
	if route == nil || route.Route != types.RouteProxy {
		if pipe.Metadata != nil {
			delete(pipe.Metadata, "__vision_v2_active")
			delete(pipe.Metadata, "__vision_v2_format")
			delete(pipe.Metadata, "__vision_v2_route")
		}
		return payload, nil
	}
	s.lg.Info("视觉路由命中", "model", pipe.Request.Model, "route", "proxy", "via_candidates", len(route.ViaOptions))

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
	changed, replacedCount, err := s.rewriteImagesToPlaceholders(context.Background(), messages, format)
	if err != nil {
		return nil, visionError(fmt.Sprintf("图片落盘失败: %v", err))
	}
	s.lg.Info("图片检出", "model", pipe.Request.Model, "path", pipe.Request.Path, "image_count", replacedCount)
	if !changed {
		return payload, nil
	}

	// 工具注入：按格式注入 look_at_image 工具
	tools, injected := ensureLookAtImageTool(bodyMap["tools"], format)
	bodyMap["tools"] = tools
	s.lg.Info("工具注入", "tool", lookAtImageToolName, "status", map[bool]string{true: "inject", false: "skip_existing"}[injected])

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
	s.lg.Info("请求改写完成", "model", pipe.Request.Model, "path", pipe.Request.Path, "replaced_images", replacedCount, "tools_injected", injected, "body_bytes", len(newBody))
	return pipe, nil
}

// rewriteImagesToPlaceholders 遍历消息 content，图片块 → <vision_img_{id}> 文本块。
// 返回 (changed, replacedCount, err)，replacedCount 为成功替换的图片张数。
func (s *Service) rewriteImagesToPlaceholders(ctx context.Context, messages []any, format visionProxyFormat) (bool, int, error) {
	changed := false
	replacedCount := 0
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
			replacedCount++
		}
	}
	return changed, replacedCount, nil
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
