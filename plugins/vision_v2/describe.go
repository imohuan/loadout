package visionv2

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"loadout/core/config"
	"loadout/core/db"
	"loadout/plugins/contracts"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// DescribeImage 按 id 识别图片：缓存(md5(id|prompt|v3))命中直接返回 (text, true, nil)；
// miss 时读本地文件、压缩、调视觉模型（viaModel 为该渠道绑定的视觉模型名），写缓存后返回 (text, false, nil)。
// Deprecated: 生产路径已迁移到 callVisionViaGateway（子请求走 model-gateway 主链路）。
// 本函数为直连 http.Client.Do 的旧实现，仅测试（DescribeImage 单测）保留引用，
// 不再被生产代码调用——不要把它接回生产链路，否则后门复活（request-log/安检/额度缺失）。
// streamWriter 非 nil 时识别过程实时输出（SSE reasoning delta）。
// ch 为已解析的视觉渠道（baseURL/apiKey）。
func (s *Service) DescribeImage(ctx context.Context, id, prompt string, streamWriter func(string) error, ch modelgateway.ResolvedChannel, viaModel string) (string, bool, error) {
	key := visionCacheKey(id, prompt)
	if config.VisionCacheEnabled {
		if text, ok := s.readCache(key); ok {
			return text, true, nil
		}
	}
	dataURI, err := s.buildDataURI(id)
	if err != nil {
		return "", false, err
	}
	text, err := s.describeImageWithDataURI(ctx, key, id, prompt, dataURI, streamWriter, ch, viaModel)
	if err != nil {
		return "", false, err
	}
	return text, false, nil
}

// buildDataURI 读取图片字节并构建（压缩后的）data URI，供视觉识别复用（一次读图、多候选共享）。
func (s *Service) buildDataURI(id string) (string, error) {
	raw, mime, err := s.loadImageBytes(id)
	if err != nil {
		return "", err
	}
	dataURI := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw)
	if config.VisionCompressEnabled {
		if c, err := CompressDataURI(dataURI); err == nil {
			dataURI = c
		} else if !errors.Is(err, ErrImageTooLarge) {
			s.lg.Warn("图片压缩失败，使用原图", "id", id, "err", err)
		}
	}
	return dataURI, nil
}

// describeImageWithDataURI 用已构建的 data URI 调视觉模型并写缓存、懒清理孤儿图片。
func (s *Service) describeImageWithDataURI(ctx context.Context, key, id, prompt, dataURI string, streamWriter func(string) error, ch modelgateway.ResolvedChannel, viaModel string) (string, error) {
	text, err := s.callVision(ctx, dataURI, prompt, ch, viaModel, streamWriter)
	if err != nil {
		return "", err
	}
	if config.VisionCacheEnabled {
		_ = s.writeCache(key, text)
	}
	s.cleanupStaleFiles() // 懒清理孤儿图片
	return text, nil
}

// describeWithFailover 按路由 via_options 依次尝试视觉候选，任一成功返回
// (text, successChannelID, requestLogID, nil)；全部失败返回 ("", "", "", lastErr)。
// 缓存命中直接返回 (text, "", "", nil)（channelID 空 = 缓存命中）。
// requestLogID 为视觉识别子请求在 request-log 的关联 UUID（来自网关 final pipe 的
// __request_log_attempt_id），调用方写入视觉 attempt 的 request_log_id 列（前端日志按钮）。
// 候选渠道的 failover 交给 model-gateway 子请求通道（ForwardSubRequest）：每个 option
// 展开候选渠道 → 一次子请求，网关内部按 candidates 循环换渠道；option 之间按数组顺序
// 依次尝试（跨 viaModel 场景由本函数外层循环承担）。
// 本方法不写 route-log（写日志职责归调用方 tool_loop.go/after.go）。
func (s *Service) describeWithFailover(ctx context.Context, id, prompt string, streamWriter func(string) error, route *types.CapabilityRoute, parentRequestID string) (string, string, string, error) {
	key := visionCacheKey(id, prompt)
	if config.VisionCacheEnabled {
		if text, ok := s.readCache(key); ok {
			s.lg.Info("视觉缓存命中", "image_id", id, "prompt", prompt)
			return text, "", "", nil
		}
	}
	dataURI, err := s.buildDataURI(id)
	if err != nil {
		return "", "", "", err
	}
	var options []types.ViaOption
	if route != nil {
		options = route.ViaOptions
	}
	if len(options) == 0 {
		options = []types.ViaOption{{ViaModel: config.DefaultVisionModel}}
	}
	channels := s.loadChannels(ctx)
	var lastErr error
	var lastViaModel string
	var lastReqLogID string
	for _, opt := range options {
		viaModel := opt.ViaModel
		if viaModel == "" {
			viaModel = config.DefaultVisionModel
		}
		lastViaModel = viaModel
		// 展开候选渠道（渠道级组 → 组内全部启用 Key；Key 多选/单 Key → 各自 id；
		// 无渠道约束 → ids 空，网关按 model 自动路由）。
		keys := modelgateway.ExpandCandidateKeys(opt.ChannelID, opt.ChannelIDs, opt.ChannelBaseURL, channels)
		var ids []string
		for _, k := range keys {
			ids = append(ids, k.ChannelID)
		}
		start := time.Now()
		s.lg.Info("视觉候选 start", "via_model", viaModel, "candidates", ids, "image_id", id, "prompt", prompt, "compressed_bytes", len(dataURI))
		text, chID, reqLogID, err := s.callVisionViaGateway(ctx, dataURI, prompt, viaModel, ids, streamWriter, parentRequestID)
		if err == nil {
			s.lg.Info("视觉候选 success", "via_model", viaModel, "channel_id", chID, "duration_ms", time.Since(start).Milliseconds())
			if config.VisionCacheEnabled {
				_ = s.writeCache(key, text)
			}
			s.cleanupStaleFiles()
			return text, chID, reqLogID, nil
		}
		s.lg.Warn("视觉候选 failed", "via_model", viaModel, "candidates", ids, "err", err, "duration_ms", time.Since(start).Milliseconds())
		lastErr = err
		if reqLogID != "" {
			lastReqLogID = reqLogID
		}
	}
	// 防御兜底：全部候选失败但 lastErr 仍为 nil（如 options 为空等异常路径）时，
	// 返回明确错误而非 ("","",nil)——调用方会把 nil error 当缓存命中写空描述。
	if lastErr == nil {
		lastErr = fmt.Errorf("vision_v2: 视觉候选 %q 无可用渠道", lastViaModel)
	}
	return "", "", lastReqLogID, lastErr
}

// callVisionViaGateway 经 model-gateway 子请求通道调用视觉模型（chat/completions 格式）。
// channelIDs 为候选渠道 id 列表（空 = 按 model 自动路由）；网关内部完成渠道 failover。
// streamWriter 非 nil 时流式：视觉模型 SSE delta 实时输出（经 toolStreamWriter → 客户端思考区），
// 同时累积返回完整文本。返回 (text, successChannelID, requestLogID, error)——requestLogID
// 为子请求在 request-log 的关联 UUID（__request_log_attempt_id，网关 per-attempt 写入）。
func (s *Service) callVisionViaGateway(ctx context.Context, dataURI, prompt string, viaModel string, channelIDs []string, streamWriter func(string) error, parentRequestID string) (string, string, string, error) {
	if s.gw == nil {
		return "", "", "", errors.New("vision_v2: model-gateway 子请求通道未初始化")
	}
	if viaModel == "" {
		viaModel = config.DefaultVisionModel
	}
	p := config.VisionDescriptionPrompt
	if strings.TrimSpace(p) == "" {
		p = builtinVisionPrompt
	}
	payload := struct {
		Model    string        `json:"model"`
		Messages []chatMessage `json:"messages"`
		Stream   bool          `json:"stream"`
	}{
		Model: viaModel,
		Messages: []chatMessage{{Role: "user", Content: []contentPart{
			{Type: "image_url", ImageURL: &imageURL{URL: dataURI}},
			{Type: "text", Text: "识别方向: " + prompt + "\n\n" + p},
		}}},
		Stream: streamWriter != nil,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", "", "", fmt.Errorf("vision_v2: 序列化视觉请求失败: %w", err)
	}
	pipe := &modelgateway.ProxyPipeline{
		Request: &modelgateway.ProxyRequest{
			Method: "POST",
			Path:   "chat/completions",
			Header: http.Header{"Content-Type": []string{"application/json"}},
			Body:   body,
			Model:  viaModel,
			Stream: streamWriter != nil,
		},
		Metadata: map[string]any{},
	}
	// 候选渠道注入：不设 __current_channel（防 proxyForward 锁死到单渠道，via_options failover 失效）。
	if len(channelIDs) > 0 {
		pipe.Metadata["__channel_candidates"] = channelIDs
	}
	// 子请求关联主请求（供 request-log / UI 关联展示）。
	if parentRequestID != "" {
		pipe.Metadata["__parent_request_id"] = parentRequestID
	}

	// 流式：网关逐行回调（上游 SSE 行）→ feedVisionLine 解析 content delta，
	// 实时输出（toolStreamWriter → 客户端思考区）+ 累积到 sb（函数作用域，结束后可读）。
	var sb strings.Builder
	var subWriter func(line []byte) error
	if streamWriter != nil {
		subWriter = func(line []byte) error {
			return feedVisionLine(string(line), &sb, streamWriter)
		}
	}

	final, respBody, err := s.gw.ForwardSubRequest(ctx, pipe, subWriter)
	reqLogID := ""
	if final != nil {
		if v, _ := final.Metadata[modelgateway.MetadataRequestLogAttemptID].(string); v != "" {
			reqLogID = v
		}
	}
	if err != nil {
		// 失败路径也带 reqLogID：request_logs 已有失败行，视觉 attempt 失败行应关联它，
		// 否则前端完整日志按钮在失败场景缺失。
		return "", "", reqLogID, err
	}
	if streamWriter != nil {
		// 流式：累积文本在 sb（回调已逐行填充）。
		if sb.Len() == 0 {
			return "", "", reqLogID, errors.New("vision_v2: 视觉模型未返回描述文本")
		}
		chID := ""
		if v, _ := final.Metadata["__last_tried_channel"].(string); v != "" {
			chID = v
		}
		return sb.String(), chID, reqLogID, nil
	}
	// 非流式：解析完整 JSON 响应。
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", "", reqLogID, fmt.Errorf("vision_v2: 解析视觉响应失败: %w", err)
	}
	if len(parsed.Choices) == 0 || parsed.Choices[0].Message.Content == "" {
		return "", "", reqLogID, errors.New("vision_v2: 视觉模型未返回描述文本")
	}
	chID := ""
	if v, _ := final.Metadata["__last_tried_channel"].(string); v != "" {
		chID = v
	}
	return parsed.Choices[0].Message.Content, chID, reqLogID, nil
}

// feedVisionLine 解析一行上游 SSE（readVisionStream 的单行语义）：data: 前缀的
// choices[0].delta.content 累积到 sb 并实时经 streamWriter 输出。供子请求流式回调用。
func feedVisionLine(line string, sb *strings.Builder, streamWriter func(string) error) error {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "data:") {
		return nil
	}
	data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	if data == "[DONE]" || data == "" {
		return nil
	}
	var parsed struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal([]byte(data), &parsed) == nil && len(parsed.Choices) > 0 {
		delta := parsed.Choices[0].Delta.Content
		if delta != "" {
			sb.WriteString(delta)
			if streamWriter != nil {
				if err := streamWriter(delta); err != nil {
					return fmt.Errorf("vision_v2: 写出视觉流失败: %w", err)
				}
			}
		}
	}
	return nil
}

// loadChannels 读取全部渠道（repo；复制旧 vision service.go:387）。
func (s *Service) loadChannels(ctx context.Context) []db.Channel {
	if s.repo == nil {
		return nil
	}
	channels, err := s.repo.ListChannels(ctx)
	if err != nil {
		s.lg.Warn("vision_v2: 读取渠道表失败", "err", err)
		return nil
	}
	return channels
}

// channelsForModel 按模型名解析渠道（SQLite）：已知模型（渠道 Models 精确包含）
// Deprecated: 生产路径已迁移到网关子请求通道；本函数仅 DescribeImage 单测引用。
// 优先，未知模型（渠道 Models 为空）兜底；与 modelgateway 主路由语义一致。
// 复制旧 vision service.go:428（含 channelHasModel 辅助）。
func (s *Service) channelsForModel(ctx context.Context, model string) ([]modelgateway.ResolvedChannel, error) {
	if s.repo == nil {
		return nil, fmt.Errorf("vision_v2: 渠道仓储未初始化")
	}
	channels, err := s.repo.ListChannels(ctx)
	if err != nil {
		return nil, fmt.Errorf("vision_v2: 读取渠道表失败: %w", err)
	}
	var known, unknown []modelgateway.ResolvedChannel
	for _, ch := range channels {
		if !ch.ManualEnabled {
			continue
		}
		key := ""
		if ch.APIKeyCipher != "" {
			k, err := s.st.Decrypt(ch.APIKeyCipher)
			if err != nil {
				continue // 解密失败跳过该渠道
			}
			key = k
		}
		rc := modelgateway.ResolvedChannel{ID: ch.ID, Name: ch.Name, ChannelName: ch.ChannelName, BaseURL: ch.BaseURL, APIKey: key}
		if len(ch.Models) == 0 {
			unknown = append(unknown, rc)
		} else if channelHasModel(ch.Models, model) {
			known = append(known, rc)
		}
	}
	return append(known, unknown...), nil
}

// channelHasModel 判断渠道模型目录是否精确包含 model。
func channelHasModel(models []db.ChannelModel, model string) bool {
	for _, m := range models {
		if m.Model == model {
			return true
		}
	}
	return false
}

// callVision 单渠道调用视觉模型（chat/completions 格式，独立于主请求格式）。
// viaModel 为该渠道绑定的视觉模型名（来自路由 via_options）；空时兜底 config.DefaultVisionModel。
// Deprecated: 生产路径已迁移到 callVisionViaGateway（子请求走 model-gateway 主链路）。
// 本函数为直连 http.Client.Do 的旧实现，仅测试（TestCallVision*）保留引用，
// 不再被生产代码调用——不要把它接回生产链路，否则后门复活（request-log/安检/额度缺失）。
// 日志不由本函数写：调用方（executeToolLoop / toolLoopNonStream）的 visionAttempt
// 会把视觉识别 attempt 挂在主请求折叠下作为 step=4.x 子步骤（与截图期望一致）。
func (s *Service) callVision(ctx context.Context, dataURI, prompt string, ch modelgateway.ResolvedChannel, viaModel string, streamWriter func(string) error) (string, error) {
	if viaModel == "" {
		viaModel = config.DefaultVisionModel
	}
	p := config.VisionDescriptionPrompt
	if strings.TrimSpace(p) == "" {
		p = builtinVisionPrompt
	}
	payload := struct {
		Model    string        `json:"model"`
		Messages []chatMessage `json:"messages"`
		Stream   bool          `json:"stream"`
	}{
		Model: viaModel,
		Messages: []chatMessage{{Role: "user", Content: []contentPart{
			{Type: "image_url", ImageURL: &imageURL{URL: dataURI}},
			{Type: "text", Text: "识别方向: " + prompt + "\n\n" + p},
		}}},
		Stream: streamWriter != nil,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("vision_v2: 序列化视觉请求失败: %w", err)
	}
	target := strings.TrimRight(ch.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if ch.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+ch.APIKey)
	}
	client := &http.Client{Timeout: config.VisionTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("vision_v2: 视觉模型返回错误(%d): %s", resp.StatusCode, string(b))
	}
	if streamWriter != nil {
		return readVisionStream(resp.Body, streamWriter)
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", err
	}
	if len(parsed.Choices) == 0 || parsed.Choices[0].Message.Content == "" {
		return "", errors.New("vision_v2: 视觉模型未返回描述文本")
	}
	return parsed.Choices[0].Message.Content, nil
}

// 复制的辅助类型（从旧 vision/service.go）：chatMessage、contentPart、imageURL、
// readVisionStream、visionAttempt、pointerTime（route-log 用）。

// visionAttempt 写一条视觉识别 attempt 到 route-log。
// stepNo 为点分层级字符串（主请求="1"，视觉识别="1.1"，续流="1.2"，由调用方按 __main_step.子段生成）。
// reqLogID 为视觉识别子请求在 request-log 的关联 UUID（网关 final pipe 读回），
// 写入 attempt 的 request_log_id 列，前端据此渲染「完整日志」按钮。
// extra 为额外 metadata（called_via_tool/tool/image_id/prompt/cache_hit 等），与 capability/image_count 合并写入。
// routeLog 为 nil（测试/旧管线）或 stepNo == ""（非法值）时静默跳过。
func (s *Service) visionAttempt(ctx context.Context, requestID string, stepNo string, model string, startedAt time.Time, dur time.Duration, result, channelID, errMsg, reqLogID string, imageCount int, extra map[string]any) {
	if s.routeLog == nil || requestID == "" || stepNo == "" {
		return
	}
	meta := map[string]any{"capability": "vision", "image_count": imageCount}
	for k, v := range extra {
		// prompt 超长截断：metadata 写 DB 前经 safeMetadata 清洗，超长文本可能被
		// 拒写导致整个 attempt 丢失（识别过程白跑），截断到 200 字符防患。
		if k == "prompt" {
			if s, ok := v.(string); ok && len(s) > 200 {
				meta[k] = s[:200] + "...(truncated)"
				continue
			}
		}
		meta[k] = v
	}
	if _, err := s.routeLog.Attempt(ctx, contracts.RouteAttempt{
		RequestID:    requestID,
		StepNo:       stepNo,
		Action:       "视觉识别",
		Model:        model,
		ChannelID:    channelID,
		RequestLogID: reqLogID,
		StartedAt:    startedAt,
		FinishedAt:   pointerTime(startedAt.Add(dur)),
		Result:       result,
		ErrorMessage: errMsg,
		Duration:     contracts.DurationMS(dur),
		Stream:       true, // 视觉识别始终流式（SSE 逐 delta 输出到客户端思考区）
		Metadata:     meta,
	}); err != nil {
		s.lg.Warn("route log vision attempt failed", "err", err)
	}
}

// readVisionStream 解析视觉模型的 SSE 流：累积 content delta，并实时通过 streamWriter 输出。
func readVisionStream(body io.Reader, streamWriter func(string) error) (string, error) {
	var sb strings.Builder
	reader := bufio.NewReader(body)
	for {
		line, err := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				break
			}
			var parsed struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
				} `json:"choices"`
			}
			if json.Unmarshal([]byte(data), &parsed) == nil && len(parsed.Choices) > 0 {
				delta := parsed.Choices[0].Delta.Content
				if delta != "" {
					sb.WriteString(delta)
					if err := streamWriter(delta); err != nil {
						return "", fmt.Errorf("vision_v2: 写出视觉流失败: %w", err)
					}
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return "", fmt.Errorf("vision_v2: 读取视觉流失败: %w", err)
		}
	}
	if sb.Len() == 0 {
		return "", errors.New("vision_v2: 视觉模型未返回描述文本")
	}
	return sb.String(), nil
}

// pointerTime 返回 time.Time 的指针（RouteAttempt.FinishedAt 需要）。
func pointerTime(t time.Time) *time.Time { return &t }

// chatMessage 视觉请求里的一条消息。
type chatMessage struct {
	Role    string        `json:"role"`
	Content []contentPart `json:"content"`
}

// contentPart 视觉请求 content 数组里的一个分段。
type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

// imageURL 视觉请求里 image_url 分段的对象形态。
type imageURL struct {
	URL string `json:"url"`
}
