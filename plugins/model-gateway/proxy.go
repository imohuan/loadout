package modelgateway

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

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
	s.proxyHandle(w, r, "v1")
}

// HandleProxyV2 透明代理 v2 版：语义与 v1 完全一致，仅在 model 含渠道名前缀时
// 锁定该渠道组（拆 hint/model 路由）并把转发体里的 model 改写回真实名。
func (s *Service) HandleProxyV2(w http.ResponseWriter, r *http.Request) {
	s.proxyHandle(w, r, "v2")
}

// proxyHandle v1/v2 共用的透明代理核心管线。差异点用 version 隔离（v1 时行为与
// 原 HandleProxy 逐字节一致）：
//  1. subPath 前缀按 version 解析；
//  2. 仅 v2：model 命中渠道名前缀时拆为 hint + realModel，写 __channel_hint，Model 改真实名；
//  3. 仅 v2 且 hint 命中：转发前把 body/query 里的 model 字段改写回真实名。
func (s *Service) proxyHandle(w http.ResponseWriter, r *http.Request, version string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "读取请求体失败: "+err.Error())
		return
	}
	subPath := strings.TrimPrefix(r.URL.Path, "/"+version+"/")
	if subPath == r.URL.Path {
		// 路径恰为 /v1（无尾斜杠）或非预期前缀（防御）：按空剩余路径处理。
		subPath = ""
	}
	model, stream := sniffRequest(body, r)
	// v2 前缀拆分：model 命中启用渠道 ChannelName 前缀 → hint + realModel。
	// 拆分在 pipe 构造前完成，保证后续（白名单/路由/hook）看到的都是真实模型名。
	var channelHint, modelFrom string
	if version == "v2" && model != "" {
		if hint, real, ok := splitV2Model(model, s.isChannelName(r.Context())); ok {
			channelHint = hint
			modelFrom = model // 客户端原始名（含前缀），用于转发前改写回真实名
			model = real
		}
	}
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
	if channelHint != "" {
		pipe.Metadata["__channel_hint"] = channelHint
	}
	setRequestIDHeader(w, pipe.RequestID)
	// 流式请求注入视觉流输出通道：能力插件（vision）可把识别过程实时输出到客户端。
	if stream {
		pipe.StreamWriter = newProxyVisionStreamWriter(w)
	}

	// key 白名单校验：model 非空才校验；空 model 放行（不做模型匹配，直接转发）。
	// v2 前缀命中时双形态校验：裸名或带前缀名任一命中即放行——/v2/models 输出的
	// 正是带前缀名（如 newapi/gpt-4o），用户按广告名配置白名单后调 /v2 不应 403。
	if key, ok := gatewaykeys.APIKeyFromContext(r.Context()); ok && pipe.Request.Model != "" {
		allowed := gatewaykeys.AllowedModel(key.Models, pipe.Request.Model)
		if !allowed && channelHint != "" {
			allowed = gatewaykeys.AllowedModel(key.Models, modelFrom)
		}
		if !allowed {
			writeOpenAIError(w, http.StatusForbidden, "permission_error", fmt.Sprintf("API key 无权访问模型 %q", pipe.Request.Model))
			return
		}
	}

	// 先写占位日志（running）：before-upstream hook（视觉识别可能耗时数秒~数十秒）
	// 期间 UI 就能看到这条记录，识别/转发完成后由各阶段更新状态。
	started := time.Now()
	s.proxyBeginLog(r, pipe)

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
	// hint 生命周期：hook 重建 pipe 会清 metadata，此处无条件写回，
	// 保证前缀锁定在 hook 之后依然生效（插件重建 pipe 也不丢）。
	if channelHint != "" {
		pipe.Metadata["__channel_hint"] = channelHint
	}
	// hook 可能设置了 __virtual_model：二次 Start（UPSERT）补全虚拟模型名，
	// 不新增记录（同一 request_id 合并）。
	s.proxyBeginLog(r, pipe)

	// v2 前缀命中：转发前把 body/query 里的 model 改写回真实名（上游不认识前缀名）。
	// 无前缀或 v1 时零改动（字节级透传，与 v1 完全一致）。
	if channelHint != "" && pipe.Request != nil {
		pipe.Request.Body = rewriteModelField(pipe.Request.Body, modelFrom, pipe.Request.Model)
		pipe.Request.Query = rewriteModelQuery(pipe.Request.Query, modelFrom, pipe.Request.Model)
	}

	// 转发（proxyForward 负责写出最终响应：成功透传或最终失败原样透传上游错误）。
	s.proxyForward(w, r, pipe, pipe.Request.Model, started)
}

// isChannelName 构建启用渠道 ChannelName 集合的判断函数（v2 前缀拆分用）。
// SQLite 优先，routing 为 nil 时回退 JSON 渠道表；ChannelName 为空/纯空白不参与。
func (s *Service) isChannelName(ctx context.Context) func(string) bool {
	names := map[string]bool{}
	if s.routing != nil {
		if channels, err := s.routing.ListChannels(ctx); err == nil {
			for _, ch := range channels {
				if ch.ManualEnabled && strings.TrimSpace(ch.ChannelName) != "" {
					names[ch.ChannelName] = true
				}
			}
			return func(name string) bool { return names[name] }
		}
	}
	var channels []types.Channel
	if err := s.st.Read(types.FileChannels, &channels); err == nil {
		for _, ch := range channels {
			if ch.Enabled && strings.TrimSpace(ch.ChannelName) != "" {
				names[ch.ChannelName] = true
			}
		}
	}
	return func(name string) bool { return names[name] }
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

// attemptResult 一次渠道尝试的失败详情（proxyAttempt 填回，proxyForward 汇总用）。
type attemptResult struct {
	err        error
	channelID  string
	statusCode int
	errorBody  string
	header     http.Header
}

// rejectBySecurity 安检拒绝的公共路径：写 attempt/finish 日志 + 返回错误响应。
// 供 proxyAttempt（每次尝试安检）与 proxyForward（无候选渠道早退分支）共用，
// 保证"内容违规拒绝"的日志语义一致。返回 true 表示已写出响应，调用方直接返回。
func (s *Service) rejectBySecurity(w http.ResponseWriter, r *http.Request, pipe *ProxyPipeline, model string, started time.Time, aerr error) bool {
	status := http.StatusBadRequest
	if gw, ok := aerr.(*GatewayError); ok && gw.Status != 0 {
		status = gw.Status
	}
	attemptStarted := time.Now()
	channelID, _ := pipe.Metadata["__current_channel"].(string)
	channelName, _ := pipe.Metadata["__stream_channel_name"].(string)
	s.proxyAttemptLog(r, pipe, model, channelID, channelName, nil, "", attemptStarted, "failed", "rejected", status, pipe.Request.Stream, contracts.TokenUsage{}, aerr, truncateErrorBody(aerr.Error()))
	s.proxyFinishLog(r, pipe, model, channelID, channelName, "failed", status, time.Since(started), aerr, pipe.Request.Stream, contracts.TokenUsage{}, "")
	writeGatewayError(w, aerr)
	return true
}

// proxyAttempt 单次渠道尝试：注入当前渠道上下文 → 安检（ProxyBeforeAttempt）→
// 构造并发送上游请求 → 输出处理（非流式 after-hook / 流式逐块）。
//
// 返回 (pipe', handled)：
//   - handled=true：已向客户端写出最终响应（成功，或安检拒绝），调用方应终止转发；
//   - handled=false：本次渠道尝试失败，详情写入 res（供 failover 汇总/切换）。
//
// pipe' 为安检可能改写后的管线，调用方应更新。
//
// newRequestInstanceID 生成请求实例唯一 ID：crypto/rand 16 字节 → 32 位 hex。
// 与外部 request_id（X-Request-Id，客户端可复用/重试）解耦：每个渠道尝试实例
// 一个 ID，request-log 插件用它做 request_logs 主键。
func newRequestInstanceID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// 与 ProxyBeforeUpstream（入口只执行一次：聚合改写/额度拦截）的区别：本函数每次
// 渠道尝试都触发安检，切换渠道/切换模型后能力路由按当前渠道上下文重新匹配。
func (s *Service) proxyAttempt(w http.ResponseWriter, r *http.Request, pipe *ProxyPipeline, ch ResolvedChannel, model string, started time.Time, res *attemptResult) (*ProxyPipeline, bool) {
	if res == nil {
		res = &attemptResult{}
	}
	attemptStarted := time.Now()
	pipe.Metadata["__last_tried_channel"] = ch.ID
	// 注入当前渠道上下文：安检钩子（sensitive/field-filter）按真实渠道重新匹配
	// 能力路由；聚合 failover 递归后 resolveChannels 也会据此锁定渠道（failover
	// 钩子会先重设 __current_channel，覆盖本值）。
	pipe.Metadata["__current_channel"] = ch.ID
	// 显式赋值（含空）：同循环内渠道从有 BaseURL 到无 BaseURL 时不残留旧值，
	// 聚合递归时也不会因残留值把 resolveChannels 锁死在旧渠道。
	pipe.Metadata["__current_channel_base_url"] = ch.BaseURL

	// 请求实例唯一 ID（request-log 插件消费）：在"实际请求发出"的位置生成——
	// 只有真走到渠道尝试的请求才有 ID，额度拦截等更早截断的请求不会有。
	// 幂等：failover 同 pipe 重复尝试时 metadata 已有，不重复生成。
	if pipe.Metadata[MetadataRequestLogID] == nil {
		pipe.Metadata[MetadataRequestLogID] = newRequestInstanceID()
	}

	// 安检：每次渠道尝试前执行输入侧能力（敏感词过滤、字段过滤）。
	out, aerr := s.ctx.Waterfall(ProxyBeforeAttempt, pipe)
	if aerr != nil {
		// 安检拒绝（如敏感词 error 模式命中）：请求内容违规，与渠道无关，终止整个请求。
		res.err = aerr
		res.channelID = ch.ID
		s.rejectBySecurity(w, r, pipe, model, started, aerr)
		return pipe, true
	}
	if rewritten, ok := out.(*ProxyPipeline); ok && rewritten != nil {
		pipe = rewritten
	}

	body := pipe.Request.Body // 每次尝试重新取：安检可能改写 body
	targetPath := pipe.Request.Path
	upstream := strings.TrimRight(openAIBaseURL(ch.BaseURL), "/") + "/" + targetPath
	if q := pipe.Request.Query; q != "" {
		upstream += "?" + q
	}
	reqBody := body
	if isCopilotTencentBaseURL(ch.BaseURL) {
		reqBody = stripCopilotClientMetadata(body)
	}
	upReq, err := http.NewRequestWithContext(r.Context(), pipe.Request.Method, upstream, bytes.NewReader(reqBody))
	if err != nil {
		res.err = err
		res.channelID = ch.ID
		s.proxyAttemptLog(r, pipe, model, ch.ID, ch.ChannelName, nil, "", attemptStarted, "failed", "network", 0, pipe.Request.Stream, contracts.TokenUsage{}, err, truncateErrorBody(err.Error()))
		return pipe, false
	}
	// headers：复制客户端原始 headers，去掉 hop-by-hop 与认证，换渠道 key。
	// 腾讯 copilot 网关（copilot.tencent.com）优先读取 x-api-key/api-key 做认证，
	// 客户端透传的这两个头会覆盖渠道 Authorization 导致 401；仅该平台定向剔除，
	// 其他平台保持原样透传（透明代理语义）。
	stripAltAuth := isCopilotTencentBaseURL(ch.BaseURL)
	for k, vv := range pipe.Request.Header {
		switch http.CanonicalHeaderKey(k) {
		case "Host", "Content-Length", "Authorization", "Connection", "Proxy-Connection", "Keep-Alive",
			"Transfer-Encoding", "Upgrade", "Te", "Trailer", "Proxy-Authenticate", "Proxy-Authorization":
			continue
		case "X-Api-Key", "Api-Key":
			if stripAltAuth {
				continue
			}
		}
		for _, v := range vv {
			upReq.Header.Add(k, v)
		}
	}
	if ch.APIKey != "" {
		upReq.Header.Set("Authorization", "Bearer "+ch.APIKey)
	}
	client := &http.Client{Timeout: config.UpstreamTimeout}
	if pipe.Request.Stream {
		client = &http.Client{}
	}
	resp, err := client.Do(upReq)
	if err != nil {
		res.err = err
		res.channelID = ch.ID
		s.proxyAttemptLog(r, pipe, model, ch.ID, ch.ChannelName, nil, "", attemptStarted, "failed", "network", 0, pipe.Request.Stream, contracts.TokenUsage{}, err, truncateErrorBody(err.Error()))
		s.lg.Error("upstream network error",
			"request_id", pipe.RequestID,
			"model", model,
			"channel_id", ch.ID,
			"channel_name", ch.Name,
			"channel_group", ch.ChannelName,
			"base_url", ch.BaseURL,
			"upstream", upstream,
			"error", err.Error(),
			"duration_ms", time.Since(attemptStarted).Milliseconds(),
		)
		if s.health != nil {
			_, _ = s.health.RecordFailure(r.Context(), contracts.RouteFailure{RequestID: pipe.RequestID, Model: model, ChannelID: ch.ID, Error: err.Error()})
		}
		return pipe, false
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		upBody, _ := readUpstreamErrorBody(resp.Body, resp.Header.Get("Content-Encoding"), 8192)
		resp.Body.Close()
		rawBody := string(upBody)
		msg := upstreamErrorMsg(upBody, resp.StatusCode)
		res.err = fmt.Errorf("%s", upstreamErrorSummary(resp.StatusCode, msg))
		res.channelID = ch.ID
		res.statusCode = resp.StatusCode
		res.errorBody = rawBody
		res.header = resp.Header.Clone()
		s.proxyAttemptLog(r, pipe, model, ch.ID, ch.ChannelName, nil, "", attemptStarted, "failed", "", resp.StatusCode, pipe.Request.Stream, contracts.TokenUsage{}, res.err, truncateErrorBody(rawBody))
		preview := rawBody
		if len(preview) > 2048 {
			preview = preview[:2048] + "...(truncated)"
		}
		s.lg.Error("upstream returned error",
			"request_id", pipe.RequestID,
			"model", model,
			"channel_id", ch.ID,
			"channel_name", ch.Name,
			"channel_group", ch.ChannelName,
			"base_url", ch.BaseURL,
			"upstream", upstream,
			"status", resp.StatusCode,
			"error_message", msg,
			"response_body", preview,
			"duration_ms", time.Since(attemptStarted).Milliseconds(),
		)
		if s.health != nil {
			_, _ = s.health.RecordFailure(r.Context(), contracts.RouteFailure{RequestID: pipe.RequestID, Model: model, ChannelID: ch.ID, StatusCode: resp.StatusCode, ErrorBody: rawBody, Error: res.err.Error()})
		}
		return pipe, false
	}

	// 命中：非流式读完整 body → 输出 hook → 写回；流式逐块透传。
	if !pipe.Request.Stream {
		respBody, rerr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if rerr != nil {
			res.err = rerr
			res.channelID = ch.ID
			s.proxyAttemptLog(r, pipe, model, ch.ID, ch.ChannelName, nil, "", attemptStarted, "failed", "network", resp.StatusCode, false, contracts.TokenUsage{}, rerr, truncateErrorBody(rerr.Error()))
			return pipe, false
		}
		proxyResp := &ProxyResponse{StatusCode: resp.StatusCode, Header: resp.Header.Clone(), Body: respBody}
		// token 计量基于上游原始响应体，必须在输出 hook 之前提取：
		// 输出 hook（如 field-filter 剔除 usage 字段）会改写 Body，事后提取会得到全 0，
		// 导致 volc-free-quota 不扣减、route_log token 列恒 0。
		usage := extractUsageNonStream(respBody)
		// 主 attempt 两阶段（与流式 proxyStreamAttempt 同语义）：after-hook 前写 running
		// 占位并分配 step=1，after-hook 返回后同 step UPSERT 覆盖为 success——非流式下
		// 视觉插件在 after-hook 内基于 __main_step 拼子段，得到 主=1、视觉识别=1.1、续流=1.2 的顺序
		//（此前主 attempt 在 after-hook 之后才写，顺序错成 视觉、续流、主 在前）。
		s.proxyStreamAttempt(r, pipe, model, ch.ID, ch.ChannelName, nil, "", attemptStarted, resp.StatusCode, false, false, contracts.TokenUsage{})
		out, herr := s.ctx.Waterfall(ProxyAfterUpstream, &AfterUpstreamPayload{Pipe: pipe, Response: proxyResp})
		if herr != nil {
			// 输出 hook 拒绝：视为渠道失败，换下一个（错误可被聚合插件捕获切换模型）。
			res.err = herr
			res.channelID = ch.ID
			// 注意：不设置 res.statusCode——与旧实现一致，输出 hook 拒绝视为渠道失败，
			// 最终 writeProxyError 按 statusOrDefault(0, 502) 返回 502，而非透传上游 200。
			// 覆盖 after-hook 前的 running 占位为 failed（同 step UPSERT，读 __stream_step 快照），
			// 避免主请求占位永远卡 running。
			if snap, ok := pipe.Metadata["__stream_step"].(int); ok && snap > 0 {
				stepAction, _ := pipe.Metadata["__stream_action"].(string)
				if stepAction == "" {
					stepAction = "首次尝试"
				}
				fin := time.Now()
				_, _ = s.routeLog.Attempt(context.WithoutCancel(r.Context()), contracts.RouteAttempt{
					// StepNo 字段类型为 string（contracts/routing.go:102），须显式转换。
					RequestID: pipe.RequestID, StepNo: fmt.Sprintf("%d", snap), Action: stepAction,
					Model: model, ChannelID: ch.ID, ChannelName: ch.ChannelName,
					StartedAt: attemptStarted, FinishedAt: &fin, Result: "failed",
					StatusCode: resp.StatusCode, ErrorMessage: herr.Error(),
					ErrorBody: truncateErrorBody(herr.Error()),
					Duration:  contracts.DurationMS(time.Since(attemptStarted)),
				})
			}
			return pipe, false
		}
		if rewritten, ok := out.(*AfterUpstreamPayload); ok && rewritten.Response != nil {
			proxyResp = rewritten.Response
		}
		s.ctx.Emit(ProxyUpstreamSucceeded, &ProxySuccessPayload{Pipe: pipe, Model: model, ChannelID: ch.ID, Usage: usage})
		if s.health != nil {
			_ = s.health.RecordSuccess(r.Context(), ch.ID, model)
		}
		s.proxyStreamAttempt(r, pipe, model, ch.ID, ch.ChannelName, nil, "", attemptStarted, proxyResp.StatusCode, true, false, usage)
		s.proxyFinishLog(r, pipe, model, ch.ID, ch.ChannelName, "success", proxyResp.StatusCode, time.Since(attemptStarted), nil, false, usage, "")
		writeProxyResponse(w, proxyResp)
		return pipe, true
	}

	// 流式：SSE 逐行透传，每行经 stream-chunk hook（无订阅者时 Waterfall 零开销）。
	// Emit 成功事件移到 proxyStream 之后：usage 在流结束才能拿到，
	// volc-free-quota 需要 usage.total_tokens 做本地扣减。
	// attempt 两阶段：转发前写 running 占位，流结束后同 step 更新 success——
	// 让 attempt 的 finished_at/duration 反映真实流耗时，不再出现
	// "上游 13s 已成功但请求卡 90s 进行中"的误导（旧逻辑在流开始前就标 success）。
	s.proxyStreamAttempt(r, pipe, model, ch.ID, ch.ChannelName, nil, "", attemptStarted, resp.StatusCode, false, true, contracts.TokenUsage{})
	usage := s.proxyStream(w, resp, pipe)
	s.proxyStreamAttempt(r, pipe, model, ch.ID, ch.ChannelName, nil, "", attemptStarted, resp.StatusCode, true, true, usage)
	s.ctx.Emit(ProxyUpstreamSucceeded, &ProxySuccessPayload{Pipe: pipe, Model: model, ChannelID: ch.ID, Usage: usage})
	if s.health != nil {
		_ = s.health.RecordSuccess(r.Context(), ch.ID, model)
	}
	s.proxyFinishLog(r, pipe, model, ch.ID, ch.ChannelName, "success", resp.StatusCode, time.Since(attemptStarted), nil, true, usage, "")
	return pipe, true
}

// proxyForward 按 model 路由候选渠道并依次尝试，任一成功即写出响应返回 nil；
// 全部失败时把最后一个渠道的 状态码+错误体 原样透传（透明代理语义），并返回 nil；
// 只有内部构造错误（无法发起任何请求）才返回 error。
func (s *Service) proxyForward(w http.ResponseWriter, r *http.Request, pipe *ProxyPipeline, model string, started time.Time) error {
	candidates := s.resolveProxyChannels(r.Context(), model, pipe)
	if len(candidates) == 0 {
		// 记录"候选解析失败"的 attempt，让 route-log UI 显示尝试过该模型
		//（聚合跳过不存在的目标场景下，用户能看到每个目标都试过）。
		noChErr := fmt.Errorf("没有可用渠道支持模型 %q", model)
		s.proxyAttemptLog(r, pipe, model, "", "", nil, "", time.Now(), "failed", "no_available_channel", 0, pipe.Request.Stream, contracts.TokenUsage{}, noChErr, truncateErrorBody(noChErr.Error()))
		// 无可用渠道：先过安检——内容违规（如敏感词 error 模式命中）应优先于
		// 502 拒绝，避免敏感请求因"渠道不可用"绕过安检（旧实现安检在入口执行，
		// 天然覆盖此分支；安检移到 proxyAttempt 后必须在此补一次）。
		if _, aerr := s.ctx.Waterfall(ProxyBeforeAttempt, pipe); aerr != nil {
			// 复用 rejectBySecurity：与 proxyAttempt 的安检拒绝语义一致。
			// 安检通过时的 pipe 改写在此无意义（随即 502 或 failover 改写 body），丢弃。
			s.rejectBySecurity(w, r, pipe, model, started, aerr)
			return nil
		}
		// 无可用渠道：若是聚合模型，触发失败事件让插件切下一个目标。
		if retry, ok := s.tryProxyAggregateFailover(pipe, model, noChErr, "", 0, ""); ok {
			return s.proxyForward(w, r, retry.Pipe, retry.Pipe.Request.Model, started)
		}
		s.proxyFinishLog(r, pipe, model, "", "", "failed", http.StatusBadGateway, time.Since(started), noChErr, false, contracts.TokenUsage{}, "")
		writeOpenAIError(w, http.StatusBadGateway, "no_available_channel", fmt.Sprintf("没有可用渠道支持模型 %q", model))
		return nil
	}

	// aggregate 指定了候选 Key 列表（Key 多选 / 渠道级展开）时只使用这些 Key。
	// 空结果不回退：聚合语义是「指定渠道不可用 → 触发 failover 换下一个 target」，
	// 回退默认候选会越界路由到未选 Key/渠道。
	if idsAny, ok := pipe.Metadata["__channel_candidates"]; ok {
		if ids, ok := idsAny.([]string); ok && len(ids) > 0 {
			filtered := candidates[:0]
			for _, ch := range candidates {
				for _, id := range ids {
					if ch.ID == id {
						filtered = append(filtered, ch)
						break
					}
				}
			}
			if len(filtered) > 0 {
				candidates = filtered
			} else {
				s.lg.Warn("候选 Key 列表均不可用，交由 failover 处理", "ids", ids, "model", model)
			}
		}
	}
	// aggregate 指定了渠道组（base_url）时只使用该组所有 Key。
	if baseURL, ok := pipe.Metadata["__current_channel_base_url"].(string); ok && baseURL != "" {
		filtered := candidates[:0]
		for _, ch := range candidates {
			if NormalizeBaseURL(ch.BaseURL) == NormalizeBaseURL(baseURL) {
				filtered = append(filtered, ch)
			}
		}
		if len(filtered) > 0 {
			candidates = filtered
		} else {
			s.lg.Warn("指定渠道不可用，交由 failover 处理", "base_url", baseURL, "model", model)
		}
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
			s.lg.Warn("指定渠道不可用，交由 failover 处理", "specified", specified, "model", model)
		}
	}

	var lastErr error
	var lastChannelID string
	var lastStatusCode int
	var lastErrorBody string
	var lastHeader http.Header

	for _, ch := range candidates {
		res := &attemptResult{}
		var handled bool
		pipe, handled = s.proxyAttempt(w, r, pipe, ch, model, started, res)
		if handled {
			return nil
		}
		lastErr = res.err
		lastChannelID = res.channelID
		lastStatusCode = res.statusCode
		lastErrorBody = res.errorBody
		lastHeader = res.header
	}

	// 全部渠道失败：聚合模型触发失败事件换目标，否则原样透传最后一条上游错误。
	if retry, ok := s.tryProxyAggregateFailover(pipe, model, lastErr, lastChannelID, lastStatusCode, lastErrorBody); ok {
		return s.proxyForward(w, r, retry.Pipe, retry.Pipe.Request.Model, started)
	}
	// 全渠道失败的汇总日志：单次请求最后一条错误已在具体 attempt 里写过了，这里再加一行
	// 汇总（request_id、model、尝试过的渠道数、最终状态、最终 message）方便按 request_id
	// 一次性看出整体结论；不留日志的话用户读 slog 只能拼凑多次 upstream-*。
	candidateCount, _ := pipe.Metadata["__main_route_step"].(int)
	lastChannelName := ""
	lastChannelGroup := ""
	if lastChannelID != "" {
		// 复用最后一次候选的展示名——与 attempt 日志里 channel_name/group 字段保持一致。
		for _, ch := range candidates {
			if ch.ID == lastChannelID {
				lastChannelName = ch.Name
				lastChannelGroup = ch.ChannelName
				break
			}
		}
	}
	s.lg.Error("all channels failed for model",
		"request_id", pipe.RequestID,
		"model", model,
		"last_channel_id", lastChannelID,
		"last_channel_name", lastChannelName,
		"last_channel_group", lastChannelGroup,
		"last_status", lastStatusCode,
		"last_error", errorText(lastErr),
		"attempts", candidateCount,
		"duration_ms", time.Since(started).Milliseconds(),
	)
	s.proxyFinishLog(r, pipe, model, lastChannelID, lastChannelName, "failed", statusOrDefault(lastStatusCode, http.StatusBadGateway), time.Since(started), lastErr, pipe.Request.Stream, contracts.TokenUsage{}, truncateErrorBody(lastErrorBody))
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
	var sawRealUsage bool
	var estTokens, chunksSinceWrite int
	var lastWrite time.Time
	reader := bufio.NewReader(resp.Body)
	// flushEst 节流写实时估算 token：流进行中（result=running，同 step UPSERT）让 UI
	// 看到"正在吐字"的 token 数，同时把估算值透出到返回值，流结束 success 收尾时带上。
	// 上游真实 usage 命中（sawRealUsage）或无可写 attempt（step==0）时不写。
	flushEst := func() {
		if estTokens == 0 || sawRealUsage {
			return
		}
		if s.routeLog == nil {
			return
		}
		step, _ := pipe.Metadata["__route_step"].(int)
		if step == 0 {
			return
		}
		action, _ := pipe.Metadata["__stream_action"].(string)
		model := pipe.Request.Model
		channelID, _ := pipe.Metadata["__last_tried_channel"].(string)
		channelName, _ := pipe.Metadata["__stream_channel_name"].(string)
		startedAt, _ := pipe.Metadata["__stream_started"].(time.Time)
		usage.CompletionTokens = estTokens
		ctx := context.WithoutCancel(pipe.HTTPRequest.Context())
		_, _ = s.routeLog.Attempt(ctx, contracts.RouteAttempt{
			RequestID: pipe.RequestID, StepNo: fmt.Sprintf("%d", step), Action: action,
			Model: model, ChannelID: channelID, ChannelName: channelName, StartedAt: startedAt,
			Result: "running", Stream: true, CompletionTokens: estTokens,
		})
		chunksSinceWrite = 0
		lastWrite = time.Now()
	}
	for {
		// 客户端断开：提前终止，不再继续读上游（顺带把已估算的 token 写掉）。
		select {
		case <-clientCtx.Done():
			flushEst()
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
				// 实时估算：CJK 约 1 token/字，其他约 4 字符/token；500ms 或 64 块节流。
				// 只统计 data: 前缀后的内容，避免 JSON 包装（键名/括号/换行）虚增 token 数。
				estTokens += estimateTokens(trimSSEDataPrefix(line))
				chunksSinceWrite++
				if time.Since(lastWrite) >= 500*time.Millisecond || chunksSinceWrite >= 64 {
					flushEst()
				}
			}
			if u := parseUsageLine(line); u.PromptTokens > 0 || u.CompletionTokens > 0 || u.CachedTokens > 0 {
				usage = u
				// 仅在拿到 completion_tokens 时锁定真实 usage：部分上游首块就带仅含
				// prompt_tokens 的 usage 行，过早锁定会抑制后续 completion 估算输出。
				if u.CompletionTokens > 0 {
					sawRealUsage = true
				}
			}
			if flusher != nil {
				flusher.Flush()
			}
			// SSE 结束标记：透传该行后主动退出，不再等上游关闭连接。
			// 上游（如火山引擎）keep-alive 下发完 [DONE] 不会立即关 body，
			// 继续 ReadString 会阻塞到连接 idle 超时（约 90s），表现为"上游已成功但请求卡进行中"。
			if isSSEDone(line) {
				flushEst()
				return usage
			}
		}
		if err != nil {
			if err != io.EOF {
				writeSSEError(w, flusher, "上游流式响应中断")
			}
			flushEst()
			return usage
		}
	}
}

// isSSEDone 判断一条 SSE 行是否为流结束标记 data: [DONE]（允许 data:[DONE] 无空格写法）。
func isSSEDone(line string) bool {
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "data:") {
		return false
	}
	return strings.TrimSpace(line[len("data:"):]) == "[DONE]"
}

// trimSSEDataPrefix 剥离 SSE data: 前缀与行尾换行，仅保留实际载荷内容，
// 供 token 估算统计，避免 JSON 包装（键名/括号）虚增 token 数。
func trimSSEDataPrefix(line string) string {
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "data:") {
		return ""
	}
	return line[len("data:"):]
}

// estimateTokens 极简 token 估算：CJK 字符 ≈ 1 token/字，其余字符 ≈ 4 字符/token
// （向上取整）。零依赖、±20% 精度，仅用于实时展示"正在吐字"，不用于计费/上下文窗口。
// 流结束后由上游真实 usage 覆盖。
func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	var cjk, other int
	for _, r := range s {
		if isCJK(r) {
			cjk++
		} else {
			other++
		}
	}
	return cjk + (other+3)/4
}

func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r)
}

// resolveProxyChannels 路由渠道：model 非空按模型匹配（复用健康检查/熔断），
// model 为空不做匹配，直接返回全部启用渠道（按配置顺序）。
//
// v2 前缀锁定：metadata["__channel_hint"] 非空时，候选只保留该渠道组（ChannelName == hint）
// 的启用 Key。hint 过滤必须在此函数最前（早退判断之前）——否则「有 hint 但 model 为空」
// 会绕过锁定。实现复用 __channel_candidates（Key 多选）机制：把 hint 组所有 channel ID
// 塞入 candidates，resolveChannels / allEnabledChannels 的既有过滤逻辑天然生效。
func (s *Service) resolveProxyChannels(ctx context.Context, model string, pipe *ProxyPipeline) []ResolvedChannel {
	if hint, _ := pipe.Metadata["__channel_hint"].(string); hint != "" {
		ids := s.channelIDsByChannelName(ctx, hint)
		if len(ids) == 0 {
			return nil // 前缀渠道不存在/全禁用：无候选（调用方走 no_available_channel 400）
		}
		pipe.Metadata["__channel_candidates"] = ids
	}
	if model == "" {
		channels := s.allEnabledChannels(ctx)
		if hint, _ := pipe.Metadata["__channel_hint"].(string); hint != "" {
			var out []ResolvedChannel
			for _, ch := range channels {
				if ch.ChannelName == hint {
					out = append(out, ch)
				}
			}
			return out
		}
		return channels
	}
	channels, err := s.resolveChannels(ctx, model, pipe.Metadata)
	if err != nil {
		return nil
	}
	return channels
}

// channelIDsByChannelName 返回指定 ChannelName（渠道组）下全部启用 Key 的 ID 列表。
// SQLite 优先，routing 为 nil 时回退 JSON 渠道表；无匹配返回空 slice。
func (s *Service) channelIDsByChannelName(ctx context.Context, channelName string) []string {
	if s.routing != nil {
		channels, err := s.routing.ListChannels(ctx)
		if err != nil {
			return nil
		}
		var out []string
		for _, ch := range channels {
			if ch.ManualEnabled && ch.ChannelName == channelName {
				out = append(out, ch.ID)
			}
		}
		return out
	}
	var channels []types.Channel
	if err := s.st.Read(types.FileChannels, &channels); err != nil {
		return nil
	}
	var out []string
	for _, ch := range channels {
		if ch.Enabled && ch.ChannelName == channelName {
			out = append(out, ch.ID)
		}
	}
	return out
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
			out = append(out, ResolvedChannel{ID: ch.ID, Name: ch.Name, ChannelName: ch.ChannelName, BaseURL: ch.BaseURL, APIKey: key})
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
		out = append(out, ResolvedChannel{ID: ch.ID, Name: ch.Name, ChannelName: ch.ChannelName, BaseURL: ch.BaseURL, APIKey: key})
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
	// hint 生命周期：聚合切换后的新目标不应被旧渠道前缀锁死（聚合是虚拟语义，
	// 切换目标是跨渠道的）。此处显式清除 hint 及其注入的候选 Key 列表——
	// resolveProxyChannels 在 hint 命中时把组内 Key 写进 __channel_candidates，
	// 若只删 hint 不删 candidates，递归 resolveChannels 仍按旧组 ids 过滤，
	// 新目标依旧被锁死在旧渠道组。
	delete(retry.Pipe.Metadata, "__channel_hint")
	delete(retry.Pipe.Metadata, "__channel_candidates")
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

// openAIBaseURL 规范化渠道 base_url：只去掉末尾斜杠，其余完全按用户配置原样返回，
// 不再自动补 /v1。很多模型的基础 URL 不是 v1 结尾（如 Claude、Gemini、厂商定制网关），
// 自动补全会导致 /v1/v1/xxx 类 404；需要 /v1 前缀时由用户在 base_url 里自行包含。
func openAIBaseURL(baseURL string) string {
	return strings.TrimRight(baseURL, "/")
}

// isCopilotTencentBaseURL 判断渠道 base_url 是否指向腾讯 copilot 网关
// （copilot.tencent.com）。该网关优先读取 x-api-key/api-key 头做认证，
// 转发时需剔除客户端透传的这两个头，避免覆盖渠道 Authorization 导致 401。
func isCopilotTencentBaseURL(baseURL string) bool {
	return strings.Contains(strings.ToLower(baseURL), "copilot.tencent.com")
}

// stripCopilotClientMetadata 剔除请求体中的 client_metadata 字段：
// 腾讯 copilot 网关以 DisallowUnknownFields 严格解析请求体，客户端（如元宝）
// 透传的该字段会触发 400 "json: unknown field \"client_metadata\""。
// 仅在命中 copilot 网关时调用；body 为空/非 JSON/无该字段时原样返回，
// 不重写 body，保持字节级透传语义。
func stripCopilotClientMetadata(body []byte) []byte {
	if len(body) == 0 || body[0] != '{' {
		return body
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return body
	}
	if _, ok := obj["client_metadata"]; !ok {
		return body
	}
	delete(obj, "client_metadata")
	out, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return out
}

// ---- proxy 版路由日志（与旧 HandleChat 的 route-log 语义一致，最后统一合并） ----

// proxyBeginLog 写/更新一条请求日志（Start 为 UPSERT，可重复调用：
// 首次在 before-upstream hook 之前写占位 running，hook 之后二次调用补全虚拟模型名）。
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
	}
}

// proxyAttemptLog 记录一次渠道尝试（成功/失败/跳过）。
// step_no 共享单调递增空间：视觉 hook 写视觉 attempt 时会更新 pipe.Metadata["__route_step"]
// 到视觉最后 step，本函数从该值 +1 续接，保证视觉 + 主链路 step 连续（1, 2, 3, ...）。
// action 判断改用独立的「主链路步数」计数器（__main_route_step），不被视觉 step 占用
// 干扰——避免视觉写完后主链路首次尝试被误判为「切换渠道」。
//
// channelID/channelIDs/channelBaseURL 三种粒度（与 AggregateTarget 一致）：
//   - 单 Key 路径：channelID 非空、其余空。
//   - 聚合目标 rejected（proxyRejectedLog）：channelID = t.ChannelID，channelIDs =
//     t.ChannelIDs（Key 多选），channelBaseURL = t.ChannelBaseURL（渠道级）。
//
// 前端 RouteLogTable 由此渲染 "@ 渠道名(Key1, Key2)" 而非空 channel_id。
func (s *Service) proxyAttemptLog(r *http.Request, pipe *ProxyPipeline, model, channelID, channelName string, channelIDs []string, channelBaseURL string, started time.Time, result, failureClass string, statusCode int, stream bool, usage contracts.TokenUsage, err error, errorBody string) {
	if s.routeLog == nil {
		return
	}
	step, _ := pipe.Metadata["__route_step"].(int)
	step++
	pipe.Metadata["__route_step"] = step
	// 主链路段（顶层编号）：视觉插件读它拼子段（视觉识别=1.1、续流=1.2）。
	// 新主段开始时重置子段计数器，保证每个主链路段从 .1 重新数。
	pipe.Metadata["__main_step"] = step
	pipe.Metadata["__vision_sub_step"] = 0
	// 主链路内部步数（不计视觉），用于独立判断「首次尝试 vs 切换渠道/模型」。
	mainStep, _ := pipe.Metadata["__main_route_step"].(int)
	pipe.Metadata["__main_route_step"] = mainStep + 1
	action := "首次尝试"
	if mainStep > 0 {
		action = "切换渠道"
		if pipe.Metadata["__virtual_model"] != nil {
			action = "切换模型"
		}
	}
	message := ""
	if err != nil {
		message = err.Error()
	}
	// 本次 attempt 的 request-log 关联：request-log 插件在 before-attempt 生成新 UUID
	// 写入 MetadataRequestLogAttemptID（并覆写 MetadataRequestLogID）；写 attempt 行时
	// 落 request_log_id 列，前端内层行据此渲染「日志」跳转。
	requestLogID, _ := pipe.Metadata[MetadataRequestLogAttemptID].(string)
	if _, logErr := s.routeLog.Attempt(r.Context(), contracts.RouteAttempt{
		RequestID:        pipe.RequestID,
		StepNo:           fmt.Sprintf("%d", step),
		Action:           action,
		Model:            model,
		ChannelID:        channelID,
		ChannelName:      channelName,
		ChannelIDs:       append([]string(nil), channelIDs...),
		ChannelBaseURL:   channelBaseURL,
		StartedAt:        started,
		FinishedAt:       pointer(time.Now()),
		Result:           result,
		FailureClass:     failureClass,
		StatusCode:       statusCode,
		ErrorMessage:     message,
		ErrorBody:        errorBody,
		Duration:         contracts.DurationMS(time.Since(started)),
		Stream:           stream,
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		CachedTokens:     usage.CachedTokens,
		RequestLogID:     requestLogID,
	}); logErr != nil {
		s.lg.Warn("route log attempt failed", "err", logErr)
	}
}

// proxyStreamAttempt 流式尝试的两阶段日志：done=false 时分配 step 写 running 占位，
// done=true 时复用同一 step 更新为 success（finished_at/duration 为真实流耗时）。
// 与 proxyAttemptLog 的区别：后者在流开始前就标 success，attempt 时间只到"收到响应头"，
// 而流式主链路可能要再传数十秒，导致 UI 显示"已成功"但请求仍在进行中。
// 语义对齐视觉链路的 visionAttempt（先 running 后 success，同 step UPSERT）。
func (s *Service) proxyStreamAttempt(r *http.Request, pipe *ProxyPipeline, model, channelID, channelName string, channelIDs []string, channelBaseURL string, started time.Time, statusCode int, done bool, stream bool, usage contracts.TokenUsage) {
	if s.routeLog == nil {
		return
	}
	step, _ := pipe.Metadata["__route_step"].(int)
	// done=true 收尾时复用 begin 阶段算好的 action（可能为"切换渠道/切换模型"），
	// 否则 success UPSERT 会把 action 覆盖回"首次尝试"，前端日志显示错误。
	action, _ := pipe.Metadata["__stream_action"].(string)
	if action == "" {
		action = "首次尝试"
	}
	var firstByte *time.Time
	if !done {
		// 首次：分配 step 并递增主链路计数器（后续 attempt 的「首次/切换」判定依赖它）。
		step++
		pipe.Metadata["__route_step"] = step
		// 快照本次分配 step：done=true 收尾时读 __stream_step，避免被工具循环
		//（视觉识别/续流）推进后的运行时 __route_step 撞号覆盖其它 attempt，
		// 同时保证主请求 attempt（step1）的 running→success 收尾落在自己 step 上。
		pipe.Metadata["__stream_step"] = step
		// 主链路段（顶层编号）+ 重置子段：视觉插件读 __main_step 拼子段
		//（视觉识别=1.1、续流=1.2），新主段从 .1 重新数。
		pipe.Metadata["__main_step"] = step
		pipe.Metadata["__vision_sub_step"] = 0
		mainStep, _ := pipe.Metadata["__main_route_step"].(int)
		pipe.Metadata["__main_route_step"] = mainStep + 1
		if mainStep > 0 {
			action = "切换渠道"
			if pipe.Metadata["__virtual_model"] != nil {
				action = "切换模型"
			}
		}
		// TTFB：running 占位记录收到上游响应头的时刻；success 收尾不传，
		// 由 route-log 的 COALESCE 保留首次写入的 first_byte_at。
		now := time.Now()
		firstByte = &now
		// 供 proxyStream 节流写实时估算 token 时复用本 attempt 的 step/action/started。
		pipe.Metadata["__stream_action"] = action
		pipe.Metadata["__stream_started"] = started
		pipe.Metadata["__stream_channel_name"] = channelName
	} else if snap, ok := pipe.Metadata["__stream_step"].(int); ok && snap > 0 {
		// 收尾：用 begin 阶段快照的 step（不被后续工具循环推进的运行时 __route_step 影响），
		// 同 step UPSERT 覆盖为 success（主请求 attempt step1 的 running→success 在此完成）。
		step = snap
	}
	result := "running"
	var finished *time.Time
	var duration contracts.DurationMS
	if done {
		result = "success"
		finished = pointer(time.Now())
		duration = contracts.DurationMS(time.Since(started))
	}
	// 与 proxyFinishLog 一致：attempt 写入必须用与 client disconnect 解耦的 context，
	// 防止客户端提前断开时 ExecContext 静默失败（running/success 都写不进库）。
	attemptCtx := context.WithoutCancel(r.Context())
	// 本次 attempt 的 request-log 关联（同 proxyAttemptLog 语义，两阶段共用同一 UUID）。
	requestLogID, _ := pipe.Metadata[MetadataRequestLogAttemptID].(string)
	if _, logErr := s.routeLog.Attempt(attemptCtx, contracts.RouteAttempt{
		RequestID:        pipe.RequestID,
		StepNo:           fmt.Sprintf("%d", step),
		Action:           action,
		Model:            model,
		ChannelID:        channelID,
		ChannelName:      channelName,
		ChannelIDs:       append([]string(nil), channelIDs...),
		ChannelBaseURL:   channelBaseURL,
		StartedAt:        started,
		FinishedAt:       finished,
		FirstByteAt:      firstByte,
		Result:           result,
		StatusCode:       statusCode,
		Duration:         duration,
		Stream:           stream,
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		CachedTokens:     usage.CachedTokens,
		RequestLogID:     requestLogID,
	}); logErr != nil {
		s.lg.Warn("route log stream attempt failed", "err", logErr)
	}
}

// proxyFinishLog 收尾一条转发日志（成功或最终失败）。
func (s *Service) proxyFinishLog(r *http.Request, pipe *ProxyPipeline, model, channelID, channelName, result string, status int, dur time.Duration, err error, stream bool, usage contracts.TokenUsage, errorBody string) {
	if s.routeLog == nil {
		return
	}
	message := ""
	if err != nil {
		message = err.Error()
	}
	// 关键：写 finish 日志必须用与 client disconnect 解耦的 context。
	// 客户端提前断开时 r.Context() 已被 cancel，再用它走 DB ExecContext 会静默失败，
	// 导致 route_requests.finished_at 永远 NULL、status 永远卡在 running。
	finishCtx := context.WithoutCancel(r.Context())
	if fErr := s.routeLog.Finish(finishCtx, contracts.RouteFinish{
		RequestID:        pipe.RequestID,
		FinishedAt:       time.Now(),
		Result:           result,
		FinalModel:       model,
		FinalChannelID:   channelID,
		FinalChannelName: channelName,
		HTTPStatus:       status,
		Duration:         contracts.DurationMS(dur),
		ErrorMessage:     message,
		ErrorBody:        errorBody,
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
	// 占位日志已在 hook 之前写入（HandleProxy），此处 Start 为幂等 UPSERT（补虚拟模型）。
	if sErr := s.routeLog.Start(r.Context(), contracts.RouteRequest{RequestID: pipe.RequestID, RequestedModel: requested, VirtualModel: virtual, StartedAt: time.Now()}); sErr != nil {
		s.lg.Warn("route log start failed", "err", sErr)
		return
	}
	var lastTarget types.AggregateTarget
	// 渠道名称反查：聚合目标只有 id/base_url，被拒时渠道名从渠道表补
	//（Key 删除后前端仍能显示「@渠道名(Unknown)」）。
	nameByID := map[string]string{}
	if s.routing != nil {
		if chans, err := s.routing.ListChannels(r.Context()); err == nil {
			for _, c := range chans {
				nameByID[c.ID] = c.ChannelName
			}
		}
	}
	if pipe.Metadata != nil {
		if targets, ok := pipe.Metadata["__aggregate_targets"].([]types.AggregateTarget); ok && len(targets) > 0 {
			started := time.Now()
			// 上游拦截器（volc-free-quota 等）可在 metadata 里预设 unavailable_reason，
			// 它是插件自身最准确的拦截原因（不依赖 model_states 的瞬时状态，
			// 因为"恢复全部异常"按钮或成功请求会把 model_states 重置成 available + 空 last_error）。
			pluginReason, _ := pipe.Metadata["__unavailable_reason"].(string)
			pluginClass, _ := pipe.Metadata["__unavailable_failure_class"].(string)
			for _, t := range targets {
				msg := fmt.Sprintf("目标 %q 当前不可用", t.Model)
				var failureClass = "no_available"
				if pluginReason != "" {
					msg = fmt.Sprintf("目标 %q 不可用：%s", t.Model, pluginReason)
				} else {
					// 收集候选 channel_id：聚合目标 ChannelID（渠道级 Key 单选）+ ChannelIDs（Key 多选）。
					// volc-free-quota.disableModelForFreeQuota 按具体 channel_id 写入 model_states，
					// 只查 t.ChannelID 会漏掉 Key 多选场景的真实 last_error。
					candidateIDs := append([]string(nil), t.ChannelIDs...)
					if t.ChannelID != "" {
						candidateIDs = append([]string{t.ChannelID}, candidateIDs...)
					}
					if reason := s.unavailableReason(t.Model, candidateIDs); reason != "" {
						msg = fmt.Sprintf("目标 %q 不可用：%s", t.Model, reason)
					}
				}
				if pluginClass != "" {
					failureClass = pluginClass
				}
				s.proxyAttemptLog(r, pipe, t.Model, t.ChannelID, nameByID[t.ChannelID], append([]string(nil), t.ChannelIDs...), t.ChannelBaseURL, started, "skipped", failureClass, 0, stream, contracts.TokenUsage{}, errors.New(msg), "")
			}
			lastTarget = targets[len(targets)-1]
		}
	}
	if fErr := s.routeLog.Finish(r.Context(), contracts.RouteFinish{
		RequestID:           pipe.RequestID,
		FinishedAt:          time.Now(),
		Result:              "failed",
		FinalModel:          lastTarget.Model,
		FinalChannelID:      lastTarget.ChannelID,
		FinalChannelName:    nameByID[lastTarget.ChannelID],
		FinalChannelIDs:     append([]string(nil), lastTarget.ChannelIDs...),
		FinalChannelBaseURL: lastTarget.ChannelBaseURL,
		HTTPStatus:          status,
		Duration:            contracts.DurationMS(0),
		ErrorMessage:        errorText(err),
		Stream:              stream,
	}); fErr != nil {
		s.lg.Warn("route log finish failed", "err", fErr)
	}
}

// unavailableReason 查询 model_states 获取目标模型当前不可用的具体原因
// （如 "模型免费额度用完" 冷却中 / 已禁用），返回空串表示未命中或查询失败。
//
// 用途：聚合模型全部目标不可用时，proxyRejectedLog 逐个目标写 skipped attempt，
// 用这里的真实原因替代笼统的 "当前不可用"，让用户在转发日志里直接看到
// 是"额度用完"还是"冷却/禁用"，而不是误以为模型坏了。
//
// channelIDs 接收多个候选 channel_id（聚合目标在 Key 多选模式下具体到 Key 失败场景，
// volc-free-quota 写入 model_states 时使用的是具体 channel_id），任一命中即返回。
func (s *Service) unavailableReason(model string, channelIDs []string) string {
	if s.database == nil || model == "" || len(channelIDs) == 0 {
		return ""
	}
	placeholders := strings.Repeat("?,", len(channelIDs))
	placeholders = strings.TrimSuffix(placeholders, ",")
	args := make([]any, 0, len(channelIDs)+1)
	args = append(args, model)
	for _, id := range channelIDs {
		if id == "" {
			continue
		}
		args = append(args, id)
	}
	if len(args) <= 1 {
		return ""
	}
	// 重新算 placeholder 个数（去掉空 string 后），保持与 args 一一对应。
	placeholders = strings.TrimSuffix(strings.Repeat("?,", len(args)-1), ",")
	var lastErr string
	var status string
	err := s.database.QueryRow(`SELECT status, COALESCE(last_error, '') FROM model_states WHERE model = ? AND channel_id IN (`+placeholders+`) ORDER BY fail_count DESC, updated_at DESC LIMIT 1`, args...).Scan(&status, &lastErr)
	if err != nil {
		return ""
	}
	if lastErr != "" {
		return lastErr
	}
	switch status {
	case "cooling":
		return "冷却中"
	case "disabled":
		return "已禁用"
	}
	return ""
}
