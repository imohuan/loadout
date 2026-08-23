package visionv2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"loadout/plugins/contracts"
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
// 与 executeToolLoop 对齐：视觉识别 attempt 的 step 按点分层级分配
// （主请求=1，视觉识别=1.1，续流=1.2，兄弟关系），先写 running 占位、结束再 success/failed 覆盖
// （db 按 request_id+step_no UPSERT）；续流结束补写主链路 attempt（Action=首次尝试）。
func (s *Service) toolLoopNonStream(pipe *modelgateway.ProxyPipeline, calls []ToolCall, format visionProxyFormat, bodyMap map[string]any, messages []any) ([]byte, error) {
	route, _ := pipe.Metadata["__vision_v2_route"].(*types.CapabilityRoute)
	if route == nil {
		return nil, errors.New("vision_v2: 缺少视觉路由")
	}
	ctx := context.Background()
	if pipe.HTTPRequest != nil && pipe.HTTPRequest.Context() != nil {
		ctx = pipe.HTTPRequest.Context()
	}
	round := 0
	for {
		if round >= maxToolRounds {
			return nil, fmt.Errorf("vision_v2: 工具循环超过 %d 轮", maxToolRounds)
		}
		if len(calls) == 0 {
			break
		}
		// step 分配（点分层级）：视觉识别 = 主链路段.子段（如 "1.1"），与 executeToolLoop 对齐。
		// 主链路段来自 __main_step，子段用 __vision_sub_step 计数器（识别/续流共享递增）。
		mainStep, _ := pipe.Metadata["__main_step"].(int)
		if mainStep == 0 {
			mainStep, _ = pipe.Metadata["__route_step"].(int) // 兜底
		}
		subStep := s.nextVisionSubStep(pipe)
		step := fmt.Sprintf("%d.%d", mainStep, subStep)

		// 1. 执行工具（识别）
		var toolResults []any
		for _, c := range calls {
			if c.Name != lookAtImageToolName {
				continue
			}
			start := time.Now()
			// running 占位：UI 实时看到"工具调用中"
			extra := map[string]any{
				"called_via_tool": true,
				"tool":            c.Name,
				"image_id":        c.ImageID,
				"prompt":          c.Prompt,
			}
			s.visionAttempt(ctx, pipe.RequestID, step, viaModelOf(route), start, 0, "running", "", "", "", 1, extra)
			s.lg.Info("视觉识别开始", "req", pipe.RequestID, "step", step, "tool", c.Name, "image_id", c.ImageID)
			text, chID, reqLogID, err := s.describeWithFailover(ctx, c.ImageID, c.Prompt, nil, route, pipe.RequestID)
			if err != nil {
				extra["cache_hit"] = false
				s.visionAttempt(ctx, pipe.RequestID, step, viaModelOf(route), start, time.Since(start), "failed", "", err.Error(), reqLogID, 1, extra)
				return nil, err
			}
			extra["cache_hit"] = (chID == "")
			if chID != "" {
				extra["via_channel"] = chID
			}
			s.visionAttempt(ctx, pipe.RequestID, step, viaModelOf(route), start, time.Since(start), "success", chID, "", reqLogID, 1, extra)
			s.lg.Info("视觉识别完成", "req", pipe.RequestID, "step", step, "image_id", c.ImageID, "cache_hit", chID == "", "duration_ms", time.Since(start).Milliseconds())
			toolResults = append(toolResults, buildToolResultMessage(c, text, format))
		}
		if len(toolResults) == 0 {
			break
		}
		s.lg.Info("工具结果写回", "req", pipe.RequestID, "round", round, "tool_calls", len(calls), "results", len(toolResults))
		// 2. 构造 assistant 工具消息 + 结果，追加。
		messages = append(messages, buildAssistantToolMessage(calls, format))
		messages = append(messages, toolResults...)
		bodyMap[msgArrayKey(format)] = messages
		// 3. 非流式续流请求（复用原主链路渠道，走 model-gateway 子请求通道）。
		reqBody, err := json.Marshal(bodyMap)
		if err != nil {
			return nil, err
		}
		continuationStart := time.Now()
		ch := s.mainChannelForContinuation(pipe)
		chID := ""
		if ch != nil {
			chID = ch.ID
		}
		_, respBody, err := s.continuationViaGateway(pipe, reqBody, nil)
		if err != nil {
			return nil, err
		}
		// 4. 续流结束：补写主链路 attempt（点分层级，如 "1.2"——主请求的子步骤，与视觉识别兄弟）。
		//    续流走 model-gateway 子请求通道（request-log 完整日志由网关侧自动记录），
		//    route-log 的 attempt 仍由这里补（主请求下的子步骤）。
		mainStep2, _ := pipe.Metadata["__main_step"].(int)
		if mainStep2 == 0 {
			mainStep2, _ = pipe.Metadata["__route_step"].(int)
		}
		subStep2 := s.nextVisionSubStep(pipe)
		contStep := fmt.Sprintf("%d.%d", mainStep2, subStep2)
		result, errMsg := "success", ""
		if err != nil {
			result, errMsg = "failed", err.Error()
		}
		if s.routeLog != nil {
			if _, lerr := s.routeLog.Attempt(ctx, contracts.RouteAttempt{
				RequestID:    pipe.RequestID,
				StepNo:       contStep,
				Action:       "首次尝试",
				Model:        pipe.Request.Model,
				ChannelID:    chID,
				StartedAt:    continuationStart,
				FinishedAt:   pointerTime(time.Now()),
				Result:       result,
				ErrorMessage: errMsg,
				Duration:     contracts.DurationMS(time.Since(continuationStart)),
				Stream:       false,
			}); lerr != nil {
				s.lg.Warn("route log 续流 attempt failed", "err", lerr)
			}
		}
		s.lg.Info("续流完成", "req", pipe.RequestID, "step", contStep, "new_calls", 0)
		if err != nil {
			return nil, err
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

// summarizeContinuationError 非流式续流失败响应的 body 摘要（截断 512 字节，
// 避免超长错误体塞满 error_message）。
func summarizeContinuationError(body []byte) string {
	const maxLen = 512
	if len(body) > maxLen {
		return string(body[:maxLen])
	}
	return string(body)
}

// HandleProxyAfterUpstream 非流式输出 hook：解析工具调用 → 循环执行 → 替换响应 Body。
// 工具执行失败不 return error（避免触发渠道 failover），改为写错误响应 Body。
func (s *Service) HandleProxyAfterUpstream(payload any) (any, error) {
	ap, ok := payload.(*modelgateway.AfterUpstreamPayload)
	if !ok || ap == nil || ap.Response == nil || ap.Pipe == nil || ap.Pipe.Request == nil {
		return payload, nil
	}
	// 子请求（视觉识别/续流走 model-gateway 主链路）：响应由调用方处理，不解析工具。
	if ap.Pipe.Metadata != nil {
		if v, _ := ap.Pipe.Metadata["__sub_request"].(bool); v {
			return payload, nil
		}
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
