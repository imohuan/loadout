package modelgateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"loadout/core/config"
	"loadout/plugins/contracts"
	gatewaykeys "loadout/plugins/gateway-keys"
	"loadout/plugins/types"
)

// HandleProxy 透明代理：/v1/{path...} 任意路径、任意方法、任意参数原样转发到匹配渠道。
//
// 流程：读原始 body（不解析不清洗）→ 轻量提取 model/stream → key 白名单 →
// 输入 hook（proxy:before-upstream，插件可改 Body/Path/Query/Header）→ 按 model 路由渠道 →
// 原样转发 {BaseURL}/{path}?{query} → 输出 hook（非流式改响应 / 流式逐块）→ failover。
func (s *Service) HandleProxy(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "读取请求体失败: "+err.Error())
		return
	}
	subPath := strings.TrimPrefix(r.URL.Path, "/v1/")
	if subPath == r.URL.Path {
		// 路径恰为 /v1（无尾斜杠）或非 /v1 前缀（防御）：按空剩余路径处理。
		subPath = ""
	}
	model, stream := sniffRequest(body, r)
	pipe := &ProxyPipeline{
		RequestID: r.Header.Get("X-Request-Id"),
		Request: &ProxyRequest{
			Method: r.Method,
			Path:   subPath,
			Query:  r.URL.RawQuery,
			Header: r.Header.Clone(),
			Body:   body,
			Model:  model,
			Stream: stream,
		},
		ResponseWriter: w,
		HTTPRequest:    r,
	}
	// request_id 兜底：中间件已把客户端或自生成的 id 写入请求头，此处仅为
	// 无中间件环境（测试/直连）补最后一道保险，保证 before-upstream hook
	// （如 vision 暂存视觉日志）执行时 RequestID 恒非空。
	if pipe.RequestID == "" {
		pipe.RequestID = newRequestID()
	}
	originalRequestID := pipe.RequestID
	if pipe.Metadata == nil {
		pipe.Metadata = map[string]any{}
	}
	setRequestIDHeader(w, pipe.RequestID)
	// 流式请求注入视觉流输出通道：能力插件（vision）可把识别过程实时输出到客户端。
	if stream {
		pipe.StreamWriter = newProxyVisionStreamWriter(w)
	}

	// key 白名单校验：model 非空才校验；空 model 放行（不做模型匹配，直接转发）。
	if key, ok := gatewaykeys.APIKeyFromContext(r.Context()); ok && pipe.Request.Model != "" {
		if !gatewaykeys.AllowedModel(key.Models, pipe.Request.Model) {
			writeOpenAIError(w, http.StatusForbidden, "permission_error", fmt.Sprintf("API key 无权访问模型 %q", pipe.Request.Model))
			return
		}
	}

	// 输入 hook：插件可改 Body/Path/Query/Header/Model。
	out, err := s.ctx.Waterfall(ProxyBeforeUpstream, pipe)
	if err != nil {
		s.proxyRejectedLog(r, pipe, err)
		writeGatewayError(w, err)
		return
	}
	rewritten, ok := out.(*ProxyPipeline)
	if !ok {
		s.proxyRejectedLog(r, pipe, fmt.Errorf("能力插件返回了非法载荷: %T", out))
		writeOpenAIError(w, http.StatusInternalServerError, "internal_error", "能力插件返回了非法载荷")
		return
	}
	pipe = rewritten
	// hook 若重建了 pipe 且丢失 request_id：沿用 hook 前的 id，保证与访问日志、
	// route-log、响应头保持同一 id（不生成新 id 破坏一致性）。
	if pipe.RequestID == "" {
		pipe.RequestID = originalRequestID
		setRequestIDHeader(w, pipe.RequestID)
	}
	if pipe.Metadata == nil {
		pipe.Metadata = map[string]any{}
	}
	started := time.Now()
	s.proxyBeginLog(r, pipe)

	// 转发（proxyForward 负责写出最终响应：成功透传或最终失败原样透传上游错误）。
	s.proxyForward(w, r, pipe, pipe.Request.Model, started)
}

// sniffRequest 轻量探测请求体：只提取 model 与 stream 两个字段（路由/白名单用），
// 不解析结构；body 不是 JSON 时退回 query 参数取 model/stream。
func sniffRequest(body []byte, r *http.Request) (model string, stream bool) {
	if len(body) > 0 {
		var probe struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		if err := json.Unmarshal(body, &probe); err == nil {
			return probe.Model, probe.Stream
		}
	}
	q := r.URL.Query()
	if model = q.Get("model"); model != "" {
		stream = q.Get("stream") == "true"
	}
	return model, stream
}

// newProxyVisionStreamWriter 构造视觉流输出回调：把视觉识别的 delta 以 SSE
// reasoning_content 块实时写出到客户端（与上游主模型流共用同一个 SSE 流，
// 视觉块先出、主模型流随后）。非流式请求不注入，无副作用。
func newProxyVisionStreamWriter(w http.ResponseWriter) func(string) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)
	return func(delta string) error {
		payload, err := json.Marshal(map[string]any{
			"choices": []any{map[string]any{
				"delta": map[string]any{"reasoning_content": delta},
			}},
		})
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}
}

// proxyForward 按 model 路由候选渠道并依次尝试，任一成功即写出响应返回 nil；
// 全部失败时把最后一个渠道的 状态码+错误体 原样透传（透明代理语义），并返回 nil；
// 只有内部构造错误（无法发起任何请求）才返回 error。
func (s *Service) proxyForward(w http.ResponseWriter, r *http.Request, pipe *ProxyPipeline, model string, started time.Time) error {
	candidates := s.resolveProxyChannels(r.Context(), model, pipe)
	if len(candidates) == 0 {
		// 无可用渠道：若是聚合模型，触发失败事件让插件切下一个目标。
		if retry, ok := s.tryProxyAggregateFailover(pipe, model, fmt.Errorf("没有可用渠道支持模型 %q", model), "", 0, ""); ok {
			return s.proxyForward(w, r, retry.Pipe, retry.Pipe.Request.Model, started)
		}
		s.proxyFinishLog(r, pipe, model, "", "failed", http.StatusBadGateway, time.Since(started), fmt.Errorf("没有可用渠道支持模型 %q", model), false, contracts.TokenUsage{})
		writeOpenAIError(w, http.StatusBadGateway, "no_available_channel", fmt.Sprintf("没有可用渠道支持模型 %q", model))
		return nil
	}

	// aggregate 插件指定了渠道时只使用该渠道。
	if specified, ok := pipe.Metadata["__current_channel"].(string); ok && specified != "" {
		filtered := candidates[:0]
		for _, ch := range candidates {
			if ch.ID == specified {
				filtered = append(filtered, ch)
			}
		}
		if len(filtered) > 0 {
			candidates = filtered
		} else {
			s.lg.Warn("指定渠道不可用或不在候选，回退默认候选", "specified", specified, "model", model)
		}
	}

	body := pipe.Request.Body
	targetPath := pipe.Request.Path
	var lastErr error
	var lastChannelID string
	var lastStatusCode int
	var lastErrorBody string
	var lastHeader http.Header

	for _, ch := range candidates {
		attemptStarted := time.Now()
		pipe.Metadata["__last_tried_channel"] = ch.ID
		upstream := strings.TrimRight(openAIBaseURL(ch.BaseURL), "/") + "/" + targetPath
		if q := pipe.Request.Query; q != "" {
			upstream += "?" + q
		}
		upReq, err := http.NewRequestWithContext(r.Context(), pipe.Request.Method, upstream, bytes.NewReader(body))
		if err != nil {
			lastErr = err
			lastChannelID = ch.ID
			s.proxyAttemptLog(r, pipe, model, ch.ID, attemptStarted, "failed", "network", 0, pipe.Request.Stream, contracts.TokenUsage{}, err)
			continue
		}
		// headers：复制客户端原始 headers，去掉 hop-by-hop 与认证，换渠道 key。
		for k, vv := range pipe.Request.Header {
			switch http.CanonicalHeaderKey(k) {
			case "Host", "Content-Length", "Authorization", "Connection", "Proxy-Connection", "Keep-Alive",
				"Transfer-Encoding", "Upgrade", "Te", "Trailer", "Proxy-Authenticate", "Proxy-Authorization":
				continue
			}
			for _, v := range vv {
				upReq.Header.Add(k, v)
			}
		}
		if ch.APIKey != "" {
			upReq.Header.Set("Authorization", "Bearer "+ch.APIKey)
		}

		// 非流式沿用总超时（config.UpstreamTimeout）；流式不设总超时，
		// 避免长 SSE 流（长推理/agent 任务）被到点截断，由服务层 WriteTimeout 兜底。
		client := &http.Client{Timeout: config.UpstreamTimeout}
		if pipe.Request.Stream {
			client = &http.Client{}
		}
		resp, err := client.Do(upReq)
		if err != nil {
			lastErr = err
			lastChannelID = ch.ID
			s.proxyAttemptLog(r, pipe, model, ch.ID, attemptStarted, "failed", "network", 0, pipe.Request.Stream, contracts.TokenUsage{}, err)
			if s.health != nil {
				_, _ = s.health.RecordFailure(r.Context(), contracts.RouteFailure{RequestID: pipe.RequestID, Model: model, ChannelID: ch.ID, Error: err.Error()})
			}
			continue
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			upBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			lastErr = fmt.Errorf("上游返回错误(%d): %s", resp.StatusCode, upstreamErrorMsg(upBody, resp.StatusCode))
			lastChannelID = ch.ID
			lastStatusCode = resp.StatusCode
			lastErrorBody = string(upBody)
			lastHeader = resp.Header.Clone()
			s.proxyAttemptLog(r, pipe, model, ch.ID, attemptStarted, "failed", "", resp.StatusCode, pipe.Request.Stream, contracts.TokenUsage{}, lastErr)
			if s.health != nil {
				_, _ = s.health.RecordFailure(r.Context(), contracts.RouteFailure{RequestID: pipe.RequestID, Model: model, ChannelID: ch.ID, StatusCode: resp.StatusCode, ErrorBody: lastErrorBody, Error: lastErr.Error()})
			}
			continue
		}

		// 命中：非流式读完整 body → 输出 hook → 写回；流式逐块透传。
		if !pipe.Request.Stream {
			respBody, rerr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if rerr != nil {
				lastErr = rerr
				s.proxyAttemptLog(r, pipe, model, ch.ID, attemptStarted, "failed", "network", resp.StatusCode, false, contracts.TokenUsage{}, rerr)
				continue
			}
			proxyResp := &ProxyResponse{StatusCode: resp.StatusCode, Header: resp.Header.Clone(), Body: respBody}
			out, herr := s.ctx.Waterfall(ProxyAfterUpstream, &AfterUpstreamPayload{Pipe: pipe, Response: proxyResp})
			if herr != nil {
				// 输出 hook 拒绝：视为渠道失败，换下一个（错误可被聚合插件捕获切换模型）。
				lastErr = herr
				lastChannelID = ch.ID
				s.proxyAttemptLog(r, pipe, model, ch.ID, attemptStarted, "failed", "", resp.StatusCode, false, contracts.TokenUsage{}, herr)
				continue
			}
			if rewritten, ok := out.(*AfterUpstreamPayload); ok && rewritten.Response != nil {
				proxyResp = rewritten.Response
			}
			usage := extractUsageNonStream(proxyResp.Body)
			s.ctx.Emit(ProxyUpstreamSucceeded, &ProxySuccessPayload{Pipe: pipe, Model: model, ChannelID: ch.ID})
			if s.health != nil {
				_ = s.health.RecordSuccess(r.Context(), ch.ID, model)
			}
			s.proxyAttemptLog(r, pipe, model, ch.ID, attemptStarted, "success", "", proxyResp.StatusCode, false, usage, nil)
			s.proxyFinishLog(r, pipe, model, ch.ID, "success", proxyResp.StatusCode, time.Since(attemptStarted), nil, false, usage)
			writeProxyResponse(w, proxyResp)
			return nil
		}

		// 流式：SSE 逐行透传，每行经 stream-chunk hook（无订阅者时 Waterfall 零开销）。
		s.ctx.Emit(ProxyUpstreamSucceeded, &ProxySuccessPayload{Pipe: pipe, Model: model, ChannelID: ch.ID})
		s.proxyAttemptLog(r, pipe, model, ch.ID, attemptStarted, "success", "", resp.StatusCode, true, contracts.TokenUsage{}, nil)
		usage := s.proxyStream(w, resp, pipe)
		if s.health != nil {
			_ = s.health.RecordSuccess(r.Context(), ch.ID, model)
		}
		s.proxyFinishLog(r, pipe, model, ch.ID, "success", resp.StatusCode, time.Since(attemptStarted), nil, true, usage)
		return nil
	}

	// 全部渠道失败：聚合模型触发失败事件换目标，否则原样透传最后一条上游错误。
	if retry, ok := s.tryProxyAggregateFailover(pipe, model, lastErr, lastChannelID, lastStatusCode, lastErrorBody); ok {
		return s.proxyForward(w, r, retry.Pipe, retry.Pipe.Request.Model, started)
	}
	s.proxyFinishLog(r, pipe, model, lastChannelID, "failed", statusOrDefault(lastStatusCode, http.StatusBadGateway), time.Since(started), lastErr, pipe.Request.Stream, contracts.TokenUsage{})
	writeProxyError(w, lastStatusCode, lastErrorBody, lastHeader, lastErr)
	return nil
}

// proxyStream 流式透传：透传上游响应头后逐行读取上游 SSE，每行触发
// ProxyStreamChunk hook（插件返回 nil 删除该块），最后返回提取的 usage。
func (s *Service) proxyStream(w http.ResponseWriter, resp *http.Response, pipe *ProxyPipeline) contracts.TokenUsage {
	defer resp.Body.Close()
	// 透传上游响应头（Content-Type: text/event-stream、Cache-Control 等），
	// 否则 Go 会在首次 Write 时嗅探 body 判成 text/plain，SSE 客户端会解析失败。
	copyProxyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	clientCtx := pipe.HTTPRequest.Context()
	var usage contracts.TokenUsage
	reader := bufio.NewReader(resp.Body)
	for {
		// 客户端断开：提前终止，不再继续读上游。
		select {
		case <-clientCtx.Done():
			return usage
		default:
		}
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			data := []byte(line)
			out, herr := s.ctx.Waterfall(ProxyStreamChunk, &StreamChunkPayload{Pipe: pipe, Data: data})
			if herr == nil {
				if chunk, ok := out.(*StreamChunkPayload); ok && chunk.Data != nil {
					data = chunk.Data
				} else {
					data = nil // 插件删除该块
				}
			}
			if len(data) > 0 {
				_, _ = w.Write(data)
			}
			if u := parseUsageLine(line); u.PromptTokens > 0 || u.CompletionTokens > 0 || u.CachedTokens > 0 {
				usage = u
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			if err != io.EOF {
				writeSSEError(w, flusher, "上游流式响应中断")
			}
			return usage
		}
	}
}

// resolveProxyChannels 路由渠道：model 非空按模型匹配（复用健康检查/熔断），
// model 为空不做匹配，直接返回全部启用渠道（按配置顺序）。
func (s *Service) resolveProxyChannels(ctx context.Context, model string, pipe *ProxyPipeline) []ResolvedChannel {
	if model == "" {
		return s.allEnabledChannels(ctx)
	}
	channels, err := s.resolveChannels(ctx, model, pipe.Metadata)
	if err != nil {
		return nil
	}
	return channels
}

// allEnabledChannels 返回全部启用渠道（routing 为 nil 时读 JSON 渠道表，语义一致：
// 全部启用的渠道，不做模型匹配）。
func (s *Service) allEnabledChannels(ctx context.Context) []ResolvedChannel {
	if s.routing != nil {
		channels, err := s.routing.ListChannels(ctx)
		if err != nil {
			return nil
		}
		var out []ResolvedChannel
		for _, ch := range channels {
			if !ch.ManualEnabled {
				continue
			}
			key := ""
			if ch.APIKeyCipher != "" {
				key, _ = s.st.Decrypt(ch.APIKeyCipher)
			}
			out = append(out, ResolvedChannel{ID: ch.ID, Name: ch.Name, BaseURL: ch.BaseURL, APIKey: key})
		}
		return out
	}
	var channels []types.Channel
	if err := s.st.Read(types.FileChannels, &channels); err != nil {
		return nil
	}
	var out []ResolvedChannel
	for _, ch := range channels {
		if !ch.Enabled {
			continue
		}
		key := ""
		if ch.APIKeyCipher != "" {
			key, _ = s.st.Decrypt(ch.APIKeyCipher)
		}
		out = append(out, ResolvedChannel{ID: ch.ID, Name: ch.Name, BaseURL: ch.BaseURL, APIKey: key})
	}
	return out
}

// tryProxyAggregateFailover 聚合模型失败切换（Proxy 版）：向 aggregate 发
// ProxyUpstreamFailed 事件取回改写后的管线（新 model + 新 body），继续转发。
// 返回 (retry, true) = 切换已发起，caller 必须用 retry.Pipe 递归继续转发。
func (s *Service) tryProxyAggregateFailover(pipe *ProxyPipeline, model string, failErr error, channelID string, statusCode int, errorBody string) (*ProxyRetry, bool) {
	if pipe.Metadata == nil {
		return nil, false
	}
	if _, ok := pipe.Metadata["__virtual_model"].(string); !ok {
		return nil, false
	}
	out, failErrEvent := s.ctx.Waterfall(ProxyUpstreamFailed, &ProxyFailurePayload{
		Pipe:       pipe,
		Model:      model,
		ChannelID:  channelID,
		Error:      failErr,
		StatusCode: statusCode,
		ErrorBody:  errorBody,
	})
	if failErrEvent != nil {
		return nil, false
	}
	retry, ok := out.(*ProxyRetry)
	if !ok || retry == nil || retry.Pipe == nil {
		return nil, false
	}
	if retry.Pipe.Metadata == nil {
		retry.Pipe.Metadata = map[string]any{}
	}
	retryCount, _ := retry.Pipe.Metadata["__retry_count"].(int)
	if retryCount >= 10 {
		return nil, false
	}
	retry.Pipe.Metadata["__retry_count"] = retryCount + 1
	return retry, true
}

// copyProxyHeaders 把上游响应头复制到目标 Header（剔除 hop-by-hop 与 Content-Length，
// 后者由 Go 自动计算）。
func copyProxyHeaders(dst, src http.Header) {
	for k, vv := range src {
		switch http.CanonicalHeaderKey(k) {
		case "Connection", "Proxy-Connection", "Keep-Alive", "Transfer-Encoding", "Upgrade",
			"Te", "Trailer", "Proxy-Authenticate", "Proxy-Authorization", "Content-Length":
			continue
		}
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

// writeProxyResponse 把代理响应（状态码/headers/body）写回客户端。
// 响应头全量透传（输出 hook 对 Header 的修改生效），仅剔除 hop-by-hop。
func writeProxyResponse(w http.ResponseWriter, resp *ProxyResponse) {
	copyProxyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(resp.Body)
}

// writeProxyError 全部渠道失败时原样透传最后一条上游错误（有 body 用 body，
// 否则用 error 文本生成 OpenAI 格式错误）。header 非空时透传（Retry-After 等，
// SDK 的 429/503 退避逻辑依赖）。
func writeProxyError(w http.ResponseWriter, statusCode int, errorBody string, header http.Header, err error) {
	if statusCode >= 400 && errorBody != "" {
		if ct, body := probeUpstreamError(errorBody); ct != "" {
			if header != nil {
				copyProxyHeaders(w.Header(), header)
			}
			w.Header().Set("Content-Type", ct)
			w.WriteHeader(statusCode)
			_, _ = w.Write([]byte(body))
			return
		}
	}
	writeOpenAIError(w, statusOrDefault(statusCode, http.StatusBadGateway), "upstream_error", errorText(err))
}

// probeUpstreamError 尝试按上游错误体原样透传（保留其 Content-Type 与字节）。
func probeUpstreamError(body string) (string, string) {
	if len(body) == 0 {
		return "", ""
	}
	return "application/json", body
}

func statusOrDefault(code int, def int) int {
	if code == 0 {
		return def
	}
	return code
}

// openAIBaseURL 规范化渠道 base_url：已含版本段（v1、v2、v1beta 等）原样返回，
// 否则补 /v1。兼容「https://api.openai.com/v1」与漏写 /v1 的地址两种写法。
func openAIBaseURL(baseURL string) string {
	base := strings.TrimRight(baseURL, "/")
	if idx := strings.LastIndex(base, "/"); idx >= 0 {
		last := base[idx+1:]
		if strings.HasPrefix(last, "v") && len(last) > 1 && last[1] >= '0' && last[1] <= '9' {
			return base
		}
	}
	return base + "/v1"
}

// ---- proxy 版路由日志（与旧 HandleChat 的 route-log 语义一致，最后统一合并） ----

// proxyBeginLog 转发开始时写一条 start 日志。
func (s *Service) proxyBeginLog(r *http.Request, pipe *ProxyPipeline) {
	if s.routeLog == nil || pipe.Request == nil {
		return
	}
	virtual, _ := pipe.Metadata["__virtual_model"].(string)
	requested := pipe.Request.Model
	if virtual != "" {
		requested = virtual
	}
	if err := s.routeLog.Start(r.Context(), contracts.RouteRequest{RequestID: pipe.RequestID, RequestedModel: requested, VirtualModel: virtual, StartedAt: time.Now()}); err != nil {
		s.lg.Warn("route log start failed", "err", err)
		return
	}
	// 视觉识别结果（成功）在此 flush：视觉 hook 先于 proxyBeginLog 执行，
	// 只能暂存到 Metadata，待父记录建立后再落库。
	s.flushVisionAttempt(r.Context(), pipe)
}

// flushVisionAttempt 在 route_requests 建立后，把 vision 插件暂存的视觉识别结果
// 写入 route_attempts：step_no 用 -1（独立于主链路 1..N 序号空间，排序时天然在前），
// action 固定「视觉识别」，不参与主链路的首次/切换判断。
func (s *Service) flushVisionAttempt(ctx context.Context, pipe *ProxyPipeline) {
	if s.routeLog == nil || pipe == nil || pipe.Metadata == nil {
		return
	}
	v, ok := pipe.Metadata[contracts.MetadataKeyVisionAttempt].(contracts.VisionAttemptLog)
	if !ok {
		return
	}
	if _, err := s.routeLog.Attempt(ctx, contracts.RouteAttempt{
		RequestID:    pipe.RequestID,
		StepNo:       -1,
		Action:       "视觉识别",
		Model:        v.ViaModel,
		ChannelID:    v.ChannelID,
		StartedAt:    v.StartedAt,
		FinishedAt:   pointer(v.StartedAt.Add(v.Duration.Duration())),
		Result:       v.Result,
		ErrorMessage: v.ErrorMessage,
		Duration:     v.Duration,
		Metadata:     map[string]any{"capability": "vision", "image_count": v.ImageCount},
	}); err != nil {
		s.lg.Warn("route log vision attempt failed", "err", err)
	}
}

// proxyAttemptLog 记录一次渠道尝试（成功/失败/跳过）。
func (s *Service) proxyAttemptLog(r *http.Request, pipe *ProxyPipeline, model, channelID string, started time.Time, result, failureClass string, statusCode int, stream bool, usage contracts.TokenUsage, err error) {
	if s.routeLog == nil {
		return
	}
	step, _ := pipe.Metadata["__route_step"].(int)
	step++
	pipe.Metadata["__route_step"] = step
	action := "首次尝试"
	if step > 1 {
		action = "切换渠道"
	}
	if pipe.Metadata["__virtual_model"] != nil && step > 1 {
		action = "切换模型"
	}
	message := ""
	if err != nil {
		message = err.Error()
	}
	if _, logErr := s.routeLog.Attempt(r.Context(), contracts.RouteAttempt{
		RequestID:        pipe.RequestID,
		StepNo:           step,
		Action:           action,
		Model:            model,
		ChannelID:        channelID,
		StartedAt:        started,
		FinishedAt:       pointer(time.Now()),
		Result:           result,
		FailureClass:     failureClass,
		StatusCode:       statusCode,
		ErrorMessage:     message,
		Duration:         contracts.DurationMS(time.Since(started)),
		Stream:           stream,
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		CachedTokens:     usage.CachedTokens,
	}); logErr != nil {
		s.lg.Warn("route log attempt failed", "err", logErr)
	}
}

// proxyFinishLog 收尾一条转发日志（成功或最终失败）。
func (s *Service) proxyFinishLog(r *http.Request, pipe *ProxyPipeline, model, channelID, result string, status int, dur time.Duration, err error, stream bool, usage contracts.TokenUsage) {
	if s.routeLog == nil {
		return
	}
	message := ""
	if err != nil {
		message = err.Error()
	}
	if fErr := s.routeLog.Finish(r.Context(), contracts.RouteFinish{
		RequestID:        pipe.RequestID,
		FinishedAt:       time.Now(),
		Result:           result,
		FinalModel:       model,
		FinalChannelID:   channelID,
		HTTPStatus:       status,
		Duration:         contracts.DurationMS(dur),
		ErrorMessage:     message,
		Stream:           stream,
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		CachedTokens:     usage.CachedTokens,
	}); fErr != nil {
		s.lg.Warn("route log finish failed", "err", fErr)
	}
}

// proxyRejectedLog 能力链路前置拒绝（before hook 报错）时写一条 failed 日志。
// 聚合模型"无可用目标"等场景：目标列表在 metadata 时，每个目标写一条 skipped attempt，
// 让用户在同一行日志下看到完整候选清单。
func (s *Service) proxyRejectedLog(r *http.Request, pipe *ProxyPipeline, err error) {
	if s.routeLog == nil {
		return
	}
	if pipe.RequestID == "" {
		pipe.RequestID = newRequestID()
	}
	status := http.StatusBadRequest
	var gw *GatewayError
	if errors.As(err, &gw) && gw.Status != 0 {
		status = gw.Status
	}
	virtual := ""
	if pipe.Metadata != nil {
		virtual, _ = pipe.Metadata["__virtual_model"].(string)
	}
	requested := pipe.Request.Model
	if virtual != "" {
		requested = virtual
	}
	stream := pipe.Request != nil && pipe.Request.Stream
	if sErr := s.routeLog.Start(r.Context(), contracts.RouteRequest{RequestID: pipe.RequestID, RequestedModel: requested, VirtualModel: virtual, StartedAt: time.Now()}); sErr != nil {
		s.lg.Warn("route log start failed", "err", sErr)
		return
	}
	// 视觉识别失败（vision hook 返回错误）时，暂存结果在此 flush。
	s.flushVisionAttempt(r.Context(), pipe)
	var lastTarget types.AggregateTarget
	if pipe.Metadata != nil {
		if targets, ok := pipe.Metadata["__aggregate_targets"].([]types.AggregateTarget); ok && len(targets) > 0 {
			started := time.Now()
			for _, t := range targets {
				s.proxyAttemptLog(r, pipe, t.Model, t.ChannelID, started, "skipped", "no_available", 0, stream, contracts.TokenUsage{}, fmt.Errorf("目标 %q 当前不可用", t.Model))
			}
			lastTarget = targets[len(targets)-1]
		}
	}
	if fErr := s.routeLog.Finish(r.Context(), contracts.RouteFinish{
		RequestID:      pipe.RequestID,
		FinishedAt:     time.Now(),
		Result:         "failed",
		FinalModel:     lastTarget.Model,
		FinalChannelID: lastTarget.ChannelID,
		HTTPStatus:     status,
		Duration:       contracts.DurationMS(0),
		ErrorMessage:   errorText(err),
		Stream:         stream,
	}); fErr != nil {
		s.lg.Warn("route log finish failed", "err", fErr)
	}
}
