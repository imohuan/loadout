package visionv2

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// parseToolCallsNonStream 从非流式响应体解析三格式工具调用。
// 决策：整轮只处理"全部是 look_at_image"的调用；混合或全为非本插件工具 → 返回 nil（整轮透传）。
func parseToolCallsNonStream(body []byte, format visionProxyFormat) []ToolCall {
	var calls []ToolCall
	switch format {
	case formatClaude:
		var resp struct {
			Content []struct {
				Type  string         `json:"type"`
				ID    string         `json:"id"`
				Name  string         `json:"name"`
				Input map[string]any `json:"input"`
			} `json:"content"`
			StopReason string `json:"stop_reason"`
		}
		if json.Unmarshal(body, &resp) != nil || resp.StopReason != "tool_use" {
			return nil
		}
		for _, c := range resp.Content {
			if c.Type != "tool_use" {
				continue
			}
			if c.Name != lookAtImageToolName {
				return nil // 混入非本插件工具：整轮透传
			}
			calls = append(calls, ToolCall{ID: c.ID, Name: c.Name,
				ImageID: strField(c.Input, "image_id"), Prompt: strField(c.Input, "prompt")})
		}
	case formatResponses:
		var resp struct {
			Output []struct {
				Type      string `json:"type"`
				ID        string `json:"id"`
				CallID    string `json:"call_id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"output"`
		}
		if json.Unmarshal(body, &resp) != nil {
			return nil
		}
		for _, o := range resp.Output {
			if o.Type != "function_call" {
				continue
			}
			if o.Name != lookAtImageToolName {
				return nil // 混入非本插件工具：整轮透传
			}
			tc := ToolCall{ID: o.ID, Name: o.Name}
			var args struct {
				ImageID string `json:"image_id"`
				Prompt  string `json:"prompt"`
			}
			if json.Unmarshal([]byte(o.Arguments), &args) == nil {
				tc.ImageID = args.ImageID
				tc.Prompt = args.Prompt
			}
			calls = append(calls, tc)
		}
	default: // formatChat
		var resp struct {
			Choices []struct {
				Message struct {
					ToolCalls []struct {
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if json.Unmarshal(body, &resp) != nil {
			return nil
		}
		for _, ch := range resp.Choices {
			if ch.FinishReason != "tool_calls" {
				continue
			}
			for _, tc := range ch.Message.ToolCalls {
				if tc.Function.Name != lookAtImageToolName {
					return nil // 混入非本插件工具：整轮透传
				}
				t := ToolCall{ID: tc.ID, Name: tc.Function.Name}
				var args struct {
					ImageID string `json:"image_id"`
					Prompt  string `json:"prompt"`
				}
				if json.Unmarshal([]byte(tc.Function.Arguments), &args) == nil {
					t.ImageID = args.ImageID
					t.Prompt = args.Prompt
				}
				calls = append(calls, t)
			}
		}
	}
	return calls
}

func strField(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// toolLoopNonStream 非流式工具循环：执行工具 → 按格式构造消息 → 非流式新请求 → 循环，
// 返回最终响应 Body。错误返回给调用方。
func (s *Service) toolLoopNonStream(pipe *modelgateway.ProxyPipeline, calls []ToolCall, format visionProxyFormat, bodyMap map[string]any, messages []any) ([]byte, error) {
	round := 0
	for {
		if round >= maxToolRounds {
			return nil, fmt.Errorf("vision_v2: 工具循环超过 %d 轮", maxToolRounds)
		}
		if len(calls) == 0 {
			break
		}
		var toolResults []any
		for _, c := range calls {
			if c.Name != lookAtImageToolName {
				continue
			}
			route, _ := pipe.Metadata["__vision_v2_route"].(*types.CapabilityRoute)
			if route == nil {
				return nil, fmt.Errorf("vision_v2: 缺少视觉路由")
			}
			ctx := context.Background()
			if pipe.HTTPRequest != nil && pipe.HTTPRequest.Context() != nil {
				ctx = pipe.HTTPRequest.Context()
			}
			text, err := s.describeWithFailover(ctx, c.ImageID, c.Prompt, nil, route, pipe.RequestID)
			if err != nil {
				return nil, err
			}
			toolResults = append(toolResults, buildToolResultMessage(c, text, format))
		}
		if len(toolResults) == 0 {
			break
		}
		messages = append(messages, buildAssistantToolMessage(calls, format))
		messages = append(messages, toolResults...)
		bodyMap[msgArrayKey(format)] = messages
		reqBody, err := json.Marshal(bodyMap)
		if err != nil {
			return nil, err
		}
		resp, err := s.doUpstream(pipe, reqBody)
		if err != nil {
			return nil, err
		}
		var respBody []byte
		if resp.Body != nil {
			respBody, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
		}
		next := parseToolCallsNonStream(respBody, format)
		if len(next) == 0 {
			return respBody, nil // 最终响应
		}
		calls = next
		round++
	}
	// 无工具调用分支：返回原 body（调用方处理）
	return nil, nil
}

// HandleProxyAfterUpstream 非流式输出 hook：解析工具调用 → 循环执行 → 替换响应 Body。
// 工具执行失败不 return error（避免触发渠道 failover），改为写错误响应 Body。
func (s *Service) HandleProxyAfterUpstream(payload any) (any, error) {
	ap, ok := payload.(*modelgateway.AfterUpstreamPayload)
	if !ok || ap == nil || ap.Response == nil || ap.Pipe == nil || ap.Pipe.Request == nil {
		return payload, nil
	}
	format, ok := visionFormatByPath(ap.Pipe.Request.Path)
	if !ok || len(ap.Response.Body) == 0 {
		return payload, nil
	}
	calls := parseToolCallsNonStream(ap.Response.Body, format)
	if len(calls) == 0 {
		return payload, nil
	}
	var bodyMap map[string]any
	dec := json.NewDecoder(bytes.NewReader(ap.Pipe.Request.Body))
	dec.UseNumber()
	if err := dec.Decode(&bodyMap); err != nil {
		return payload, nil
	}
	messages := proxyMessageArray(bodyMap, format)
	final, err := s.toolLoopNonStream(ap.Pipe, calls, format, bodyMap, messages)
	if err != nil {
		s.lg.Warn("vision_v2: 非流式工具循环失败", "req", ap.Pipe.RequestID, "err", err)
		errBody, _ := json.Marshal(map[string]any{"error": map[string]any{
			"message": "视觉工具执行失败: " + err.Error(), "type": "vision_capability_error"}})
		ap.Response.StatusCode = http.StatusBadGateway
		ap.Response.Body = errBody
		return ap, nil // 不 return error：避免触发渠道 failover
	}
	if final != nil {
		ap.Response.Body = final
	}
	return ap, nil
}
