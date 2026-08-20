// 模型测试代理：把「模型测试」页面的 /models 探测与 /chat/completions 请求
// 统一收敛到后台转发，避免前端直连不支持 CORS 的上游 base_url。
//
// 目标既可通过 channel_id 复用已存渠道（后端解密密钥，不回传明文），
// 也可直接提供临时 base_url + api_key（不落盘）。测试请求是「旁路探针」：
// 不写入转发日志（route-log），访问摘要（request_id、模型、耗时、tokens、
// 错误等）随响应回带，由前端「请求记录」面板直显；仅当上游是 Loadout
// 自身的导出服务时，由 router 内部正常写日志。
package adminapi

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"loadout/core/config"
	"loadout/plugins/contracts"
	"loadout/plugins/types"
)

// testTarget 模型测试的目标来源：channel_id 与 base_url/api_key 二选一。
// 选中渠道（预设）时始终传 channel_id，后端解密渠道密钥；渠道未存 K 时
// 用请求携带的临时 api_key 自动补全。sk_key_hash 为「Loadout 自带」模式：
// 前端传自建 SK key 的哈希，后端按哈希解析出明文 key，配 base_url 直接调用
// 自家网关（明文不出服务端）。suffix_mode 指定上游路径后缀模式。
type testTarget struct {
	ChannelID  string `json:"channel_id"`
	BaseURL    string `json:"base_url"`
	APIKey     string `json:"api_key"`
	SkKeyHash  string `json:"sk_key_hash"`
	SuffixMode string `json:"suffix_mode"` // chat(默认/常规) | gpt | claude
}

// testChatRequest 模型测试的 chat 请求体。
type testChatRequest struct {
	testTarget
	Model       string           `json:"model"`
	Messages    []map[string]any `json:"messages"`
	Stream      bool             `json:"stream"`
	Temperature *float64         `json:"temperature"`
	MaxTokens   *int             `json:"max_tokens"`
}

// resolveTestTarget 解析出 base_url 与明文 api_key。
// 优先级：
//  1. sk_key_hash 非空 → 「Loadout 自带」：按哈希解析自建 SK key 明文，base_url 用请求携带值。
//  2. channel_id 非空 → 渠道记录为准取 base_url；key 优先级：请求自定义 key > 渠道存储 key。
//  3. 否则用临时 base_url + api_key。
func (s *Service) resolveTestTarget(r *http.Request, in testTarget) (string, string, string, error) {
	if in.SkKeyHash != "" {
		// 自带模式：按 sk_key_hash 识别（非 base_url）。base_url 解析规则：
		//   - 相对路径（/v1、/ 或空）→ 用当前请求 Host 补全 scheme://Host；
		//   - 完整 URL（http(s)://...）→ 直接使用（用户自定义域名测试）。
		// 默认 /v1 对齐自家网关挂载路径。
		baseURL := strings.TrimSpace(in.BaseURL)
		if baseURL == "" || baseURL == "/" {
			baseURL = "/v1"
		}
		if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			if !strings.HasPrefix(baseURL, "/") {
				baseURL = "/" + baseURL
			}
			baseURL = scheme + "://" + r.Host + baseURL
		}
		plain, name, err := s.keys.ResolveAPIKey(in.SkKeyHash)
		if err != nil {
			return "", "", "", err
		}
		return baseURL, plain, name, nil
	}
	if in.ChannelID != "" {
		var baseURL, channelKey string
		if s.routing != nil {
			channel, _, err := s.channelByID(r.Context(), in.ChannelID)
			if err != nil {
				return "", "", "", err
			}
			baseURL = channel.BaseURL
			if channel.APIKeyCipher != "" {
				channelKey, _ = s.st.Decrypt(channel.APIKeyCipher)
			}
		} else {
			items, err := readSlice[types.Channel](s.st, types.FileChannels)
			if err != nil {
				return "", "", "", err
			}
			found := false
			for _, channel := range items {
				if channel.ID == in.ChannelID {
					baseURL = channel.BaseURL
					if channel.APIKeyCipher != "" {
						channelKey, _ = s.st.Decrypt(channel.APIKeyCipher)
					}
					found = true
					break
				}
			}
			if !found {
				return "", "", "", errNotFound("channel")
			}
		}
		key := strings.TrimSpace(in.APIKey)
		if key == "" {
			key = channelKey
		}
		return baseURL, key, "", nil
	}
	if strings.TrimSpace(in.BaseURL) == "" {
		return "", "", "", errors.New("缺少 channel_id 或 base_url")
	}
	return strings.TrimSpace(in.BaseURL), in.APIKey, "", nil
}

// handleTestModels 代理上游 /models，返回模型 id 列表。上游失败以 error 字段返回（HTTP 200）。
func (s *Service) handleTestModels(w http.ResponseWriter, r *http.Request) {
	var in testTarget
	if !decodeJSON(w, r, &in) {
		return
	}
	baseURL, apiKey, _, err := s.resolveTestTarget(r, in)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	models, err := fetchChannelModels(r.Context(), baseURL, apiKey, 15*time.Second)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"models": []string{}, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

// testSuffixPath 按后缀模式返回上游路径（base_url 原样拼接，后缀即路径尾部）。
func testSuffixPath(mode string) string {
	switch strings.TrimSpace(mode) {
	case "gpt":
		return "/responses"
	case "claude":
		return "/messages"
	default: // chat 或空 = 常规 OpenAI 兼容
		return "/chat/completions"
	}
}

// buildTestPayload 按后缀模式把统一的 OpenAI chat 消息格式转换成上游请求体。
// 前端始终按 chat/completions 格式构造 messages（含 image_url 图片块），
// gpt（/responses）与 claude（/messages）两种协议的消息/图片结构不同，需在此转换。
func buildTestPayload(req testChatRequest) map[string]any {
	switch strings.TrimSpace(req.SuffixMode) {
	case "gpt":
		return buildResponsesPayload(req)
	case "claude":
		return buildClaudePayload(req)
	default:
		return buildChatPayload(req)
	}
}

// buildChatPayload OpenAI Chat Completions：原样透传。
func buildChatPayload(req testChatRequest) map[string]any {
	payload := map[string]any{"model": req.Model, "messages": req.Messages, "stream": req.Stream}
	if req.Stream {
		payload["stream_options"] = map[string]any{"include_usage": true}
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	if req.MaxTokens != nil {
		payload["max_tokens"] = *req.MaxTokens
	}
	return payload
}

// buildResponsesPayload OpenAI Responses API：messages → input，文本块 text →
// input_text，图片块 image_url:{url} 拍平为 image_url:"url"（URL 或 data URI 均可）。
func buildResponsesPayload(req testChatRequest) map[string]any {
	input := make([]map[string]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		item := map[string]any{"role": m["role"]}
		switch c := m["content"].(type) {
		case string:
			item["content"] = c
		case []any:
			blocks := make([]any, 0, len(c))
			for _, b := range c {
				block, ok := b.(map[string]any)
				if !ok {
					continue
				}
				switch block["type"] {
				case "image_url":
					blocks = append(blocks, map[string]any{"type": "input_image", "image_url": imageURLString(block)})
				default: // text
					blocks = append(blocks, map[string]any{"type": "input_text", "text": block["text"]})
				}
			}
			item["content"] = blocks
		}
		input = append(input, item)
	}
	payload := map[string]any{"model": req.Model, "input": input, "stream": req.Stream}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	if req.MaxTokens != nil {
		payload["max_output_tokens"] = *req.MaxTokens
	}
	return payload
}

// buildClaudePayload Anthropic Messages API：system 消息抽到顶层 system 字段；
// 图片块 image_url 转 image + source（data URI 拆 base64，否则按 URL）；max_tokens 必填。
func buildClaudePayload(req testChatRequest) map[string]any {
	system, systemSet := "", false
	messages := make([]map[string]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		role, _ := m["role"].(string)
		if role == "system" {
			if !systemSet {
				if c, ok := m["content"].(string); ok {
					system = c
					systemSet = true
				}
			}
			continue
		}
		item := map[string]any{"role": role}
		switch c := m["content"].(type) {
		case string:
			item["content"] = c
		case []any:
			blocks := make([]any, 0, len(c))
			for _, b := range c {
				block, ok := b.(map[string]any)
				if !ok {
					continue
				}
				if block["type"] == "image_url" {
					blocks = append(blocks, claudeImageBlock(imageURLString(block)))
				} else {
					blocks = append(blocks, block)
				}
			}
			item["content"] = blocks
		}
		messages = append(messages, item)
	}
	maxTokens := 4096
	if req.MaxTokens != nil {
		maxTokens = *req.MaxTokens
	}
	payload := map[string]any{"model": req.Model, "messages": messages, "stream": req.Stream, "max_tokens": maxTokens}
	if systemSet {
		payload["system"] = system
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	return payload
}

// imageURLString 从 OpenAI chat 图片块 {"image_url":{"url":"..."}} 提取 url 字符串。
func imageURLString(block map[string]any) string {
	if img, ok := block["image_url"].(map[string]any); ok {
		if url, ok := img["url"].(string); ok {
			return url
		}
	}
	return ""
}

// claudeImageBlock 把图片 url（https 或 data URI）转成 Claude image content block。
func claudeImageBlock(url string) map[string]any {
	if mediaType, data, ok := splitDataURI(url); ok {
		return map[string]any{"type": "image", "source": map[string]any{
			"type": "base64", "media_type": mediaType, "data": data,
		}}
	}
	return map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": url}}
}

// splitDataURI 解析 data:image/png;base64,xxxx 形式，返回 media_type、纯 base64 数据。
func splitDataURI(uri string) (mediaType, data string, ok bool) {
	if !strings.HasPrefix(uri, "data:") {
		return "", "", false
	}
	rest := strings.TrimPrefix(uri, "data:")
	comma := strings.Index(rest, ",")
	if comma < 0 {
		return "", "", false
	}
	header := rest[:comma]
	if !strings.HasSuffix(header, ";base64") {
		return "", "", false
	}
	return strings.TrimSuffix(header, ";base64"), rest[comma+1:], true
}

// handleTestChat 代理上游对话接口（按后缀模式选 /chat/completions、/responses、/messages），
// 支持非流式与流式（SSE）转发。测试请求不写入转发日志：访问摘要（request_id、模型、
// 耗时、tokens、错误等）随响应回带，供前端「请求记录」面板直显；仅当上游是 Loadout
// 自身的导出服务时，由 router 内部正常写日志。
func (s *Service) handleTestChat(w http.ResponseWriter, r *http.Request) {
	var req testChatRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	baseURL, apiKey, skKeyName, err := s.resolveTestTarget(r, req.testTarget)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Model) == "" {
		writeError(w, http.StatusBadRequest, "model 必填")
		return
	}
	if len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "messages 必填")
		return
	}

	payload := buildTestPayload(req)
	body, err := json.Marshal(payload)
	if err != nil {
		s.writeServerError(w, err)
		return
	}

	requestID, _ := newID()
	w.Header().Set("X-Request-Id", requestID)

	// base_url 原样拼接，不自动补 /v1（部分上游没有 v1 段，地址完全由用户/渠道决定）。
	started := time.Now()
	upReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, strings.TrimRight(baseURL, "/")+testSuffixPath(req.SuffixMode), bytes.NewReader(body))
	if err != nil {
		s.setTestLogHeader(w, buildTestLogSummary(requestID, req.Model, req.ChannelID, skKeyName, started, time.Now(), "failed", "network", http.StatusBadGateway, req.Stream, contracts.TokenUsage{}, err))
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	upReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		upReq.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := (&http.Client{Timeout: config.UpstreamTimeout}).Do(upReq)
	if err != nil {
		s.setTestLogHeader(w, buildTestLogSummary(requestID, req.Model, req.ChannelID, skKeyName, started, time.Now(), "failed", "network", http.StatusBadGateway, req.Stream, contracts.TokenUsage{}, err))
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		upstreamBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		s.lg.Warn("模型测试上游返回错误", "status", resp.StatusCode)
		message := fmt.Sprintf("上游返回错误(%d): %s", resp.StatusCode, upstreamErrorText(upstreamBody))
		s.setTestLogHeader(w, buildTestLogSummary(requestID, req.Model, req.ChannelID, skKeyName, started, time.Now(), "failed", failureClassForStatus(resp.StatusCode), resp.StatusCode, req.Stream, contracts.TokenUsage{}, errors.New(message)))
		// 透传上游错误体，便于前端展示原始错误。
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(upstreamBody)
		return
	}

	if !req.Stream {
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			s.setTestLogHeader(w, buildTestLogSummary(requestID, req.Model, req.ChannelID, skKeyName, started, time.Now(), "failed", "network", resp.StatusCode, false, contracts.TokenUsage{}, err))
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		if ct := resp.Header.Get("Content-Type"); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		s.setTestLogHeader(w, buildTestLogSummary(requestID, req.Model, req.ChannelID, skKeyName, started, time.Now(), "success", "", resp.StatusCode, false, extractUsageNonStream(respBody), nil))
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(respBody)
		return
	}

	// 流式：SSE 末尾追加 route_log 事件携带访问摘要（含最终 tokens），不写转发日志。
	s.streamTestUpstream(w, resp, started, req.Model, req.ChannelID, skKeyName, requestID)
}

// testLogHeaderName 响应头名：携带 base64(JSON) 的测试访问摘要，供前端「请求记录」面板直显。
const testLogHeaderName = "X-Test-Log"

// setTestLogHeader 把访问摘要 base64 编码后写入响应头（必须在 WriteHeader 前调用）。
func (s *Service) setTestLogHeader(w http.ResponseWriter, summary testLogSummary) {
	w.Header().Set(testLogHeaderName, encodeTestLogSummary(summary))
}

// testLogSummary 模型测试请求的访问摘要：与前端 RouteLog 同形，随响应回带，不写库。
type testLogSummary struct {
	RequestID        string           `json:"request_id"`
	RequestedModel   string           `json:"requested_model"`
	FinalModel       string           `json:"final_model"`
	FinalChannelID   string           `json:"final_channel_id,omitempty"`
	SkKeyName        string           `json:"sk_key_name,omitempty"` // 自带模式下选中的 SK key 名称（请求记录展示用）
	StartedAt        time.Time        `json:"started_at"`
	Result           string           `json:"result"` // success | failed | stream_interrupted
	HTTPStatus       int              `json:"http_status"`
	DurationMS       int64            `json:"duration_ms"`
	ErrorMessage     string           `json:"error_message,omitempty"`
	Stream           bool             `json:"stream"`
	PromptTokens     int              `json:"prompt_tokens"`
	CompletionTokens int              `json:"completion_tokens"`
	CachedTokens     int              `json:"cached_tokens"`
	Attempts         []testLogAttempt `json:"attempts"`
}

type testLogAttempt struct {
	StepNo       int       `json:"step_no"`
	Action       string    `json:"action"`
	Result       string    `json:"result"`
	Model        string    `json:"model"`
	ChannelID    string    `json:"channel_id,omitempty"`
	StartedAt    time.Time `json:"started_at"`
	DurationMS   int64     `json:"duration_ms"`
	FailureClass string    `json:"failure_class,omitempty"`
	ErrorMessage string    `json:"error_message,omitempty"`
	Stream       bool      `json:"stream"`
}

// buildTestLogSummary 构造单步访问摘要（成功/失败/中断统一入口）。
// error_message 截断到 4KB，避免摘要超响应头大小限制。
func buildTestLogSummary(requestID, model, channelID, skKeyName string, started, finished time.Time, result, failureClass string, statusCode int, stream bool, usage contracts.TokenUsage, err error) testLogSummary {
	message := ""
	if err != nil {
		message = err.Error()
		if len([]rune(message)) > 4096 {
			message = string([]rune(message)[:4096])
		}
	}
	duration := finished.Sub(started).Milliseconds()
	return testLogSummary{
		RequestID: requestID, RequestedModel: model, FinalModel: model,
		FinalChannelID: channelID, SkKeyName: skKeyName, StartedAt: started, Result: result,
		HTTPStatus: statusCode, DurationMS: duration, ErrorMessage: message, Stream: stream,
		PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens, CachedTokens: usage.CachedTokens,
		Attempts: []testLogAttempt{{
			StepNo: 1, Action: "首次尝试", Result: result, Model: model, ChannelID: channelID,
			StartedAt: started, DurationMS: duration, FailureClass: failureClass, ErrorMessage: message, Stream: stream,
		}},
	}
}

// encodeTestLogSummary 把摘要 base64 编码；若压缩后仍超 6KB（异常情况），
// 清空 error_message 与 attempts 兜底，保证不突破常见反代 header 大小限制。
func encodeTestLogSummary(summary testLogSummary) string {
	raw, err := json.Marshal(summary)
	if err != nil {
		return ""
	}
	if len(raw) > 6*1024 {
		summary.ErrorMessage = ""
		summary.Attempts = nil
		raw, _ = json.Marshal(summary)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// streamTestUpstream 把上游 SSE 响应原样转发给客户端，同时扫描最后一个 usage chunk，
// 并在流结束（正常/中断）时追加 route_log 事件携带访问摘要。测试请求不写转发日志。
func (s *Service) streamTestUpstream(w http.ResponseWriter, resp *http.Response, started time.Time, model, channelID, skKeyName, requestID string) {
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)

	var usage contracts.TokenUsage
	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			if _, writeErr := io.WriteString(w, line); writeErr != nil {
				writeTestLogSSEEvent(w, flusher, buildTestLogSummary(requestID, model, channelID, skKeyName, started, time.Now(), "stream_interrupted", "network", http.StatusOK, true, usage, errors.New("客户端连接中断")))
				return
			}
			if u := parseTestUsageLine(line); u.PromptTokens > 0 || u.CompletionTokens > 0 || u.CachedTokens > 0 {
				usage = u
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			if err != io.EOF {
				writeTestLogSSEEvent(w, flusher, buildTestLogSummary(requestID, model, channelID, skKeyName, started, time.Now(), "stream_interrupted", "network", http.StatusOK, true, usage, errors.New("上游流式响应中断")))
				writeTestSSEError(w, flusher, "上游流式响应中断")
				return
			}
			writeTestLogSSEEvent(w, flusher, buildTestLogSummary(requestID, model, channelID, skKeyName, started, time.Now(), "success", "", http.StatusOK, true, usage, nil))
			return
		}
	}
}

// writeTestLogSSEEvent 在 SSE 流末尾追加 route_log 事件，data 为 base64(JSON summary)。
func writeTestLogSSEEvent(w http.ResponseWriter, flusher http.Flusher, summary testLogSummary) {
	block, err := json.Marshal(summary)
	if err != nil {
		return
	}
	_, _ = io.WriteString(w, "event: route_log\n")
	_, _ = io.WriteString(w, "data: "+base64.StdEncoding.EncodeToString(block)+"\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

// upstreamErrorText 尽量从上游错误体提取 message，否则返回原始文本（截断）。
func upstreamErrorText(body []byte) string {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Error.Message != "" {
		return parsed.Error.Message
	}
	text := strings.TrimSpace(string(body))
	if len(text) > 512 {
		return text[:512]
	}
	if text == "" {
		return "无错误详情"
	}
	return text
}

// failureClassForStatus 按 HTTP 状态粗略分类（测试场景的轻量分类，不依赖 model-health）。
func failureClassForStatus(status int) string {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "auth"
	case status == http.StatusTooManyRequests:
		return "rate_limit"
	case status == http.StatusPaymentRequired:
		return "model_quota"
	case status >= 500:
		return "temporary"
	default:
		return "unknown"
	}
}

// writeTestSSEError 向流式响应补发一个标准 error 事件。
func writeTestSSEError(w http.ResponseWriter, flusher http.Flusher, message string) {
	block, err := json.Marshal(map[string]any{
		"error": map[string]string{"message": message, "type": "upstream_stream_error"},
	})
	if err != nil {
		return
	}
	_, _ = io.WriteString(w, "data: "+string(block)+"\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

// parseTestUsageLine 从单条 SSE data 行提取 usage 字段（OpenAI 流式尾部 chunk）。
func parseTestUsageLine(line string) contracts.TokenUsage {
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "data:") {
		return contracts.TokenUsage{}
	}
	data := strings.TrimSpace(line[len("data:"):])
	if data == "" || data == "[DONE]" {
		return contracts.TokenUsage{}
	}
	var parsed struct {
		Usage struct {
			PromptTokens        int `json:"prompt_tokens"`
			CompletionTokens    int `json:"completion_tokens"`
			PromptTokensDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(data), &parsed); err != nil {
		return contracts.TokenUsage{}
	}
	return contracts.TokenUsage{
		PromptTokens:     parsed.Usage.PromptTokens,
		CompletionTokens: parsed.Usage.CompletionTokens,
		CachedTokens:     parsed.Usage.PromptTokensDetails.CachedTokens,
	}
}

// extractUsageNonStream 从非流式上游 JSON 响应里提取 usage 字段。
func extractUsageNonStream(body []byte) contracts.TokenUsage {
	var parsed struct {
		Usage struct {
			PromptTokens        int `json:"prompt_tokens"`
			CompletionTokens    int `json:"completion_tokens"`
			PromptTokensDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return contracts.TokenUsage{}
	}
	return contracts.TokenUsage{
		PromptTokens:     parsed.Usage.PromptTokens,
		CompletionTokens: parsed.Usage.CompletionTokens,
		CachedTokens:     parsed.Usage.PromptTokensDetails.CachedTokens,
	}
}
