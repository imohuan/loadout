# 每次渠道尝试安检管线（attempt-security-pipeline）实施计划

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 让敏感词过滤、字段过滤等输入侧能力在**每次上游渠道尝试**（首次、同模型多渠道 failover、聚合切模型）时都执行，而不是只在请求入口执行一次。

**Architecture:** 把 `proxyForward` 的渠道尝试循环体抽出为独立函数 `proxyAttempt`（单次尝试 = 注入渠道上下文 → 触发新事件 `ProxyBeforeAttempt` 安检 → 构造请求 → 发送 → 输出处理）。`proxyForward` 只保留调度（路由候选 + 循环调用 `proxyAttempt` + 聚合 failover 递归）。安检插件从 `ProxyBeforeUpstream` 改挂到 `ProxyBeforeAttempt`，每次尝试按**当前渠道**重新匹配能力路由。聚合 failover 递归进入 `proxyForward` 后天然覆盖切模型场景。

**Tech Stack:** Go（无新增依赖）、现有 waterfall 事件系统（`core/plugin`）、`httptest` 测试基建。

---

## 现状与问题（背景）

- 输入侧能力（敏感词 `sensitive-filter`、字段 `field-filter`）挂在 `ProxyBeforeUpstream`，只在 `proxyHandle` 执行一次。
- `proxyForward` 内同模型多渠道 failover（for 循环）复用已处理 body，渠道上下文不重算；聚合切模型（`tryProxyAggregateFailover` 递归）**完全跳过 before 钩子**。
- 能力路由按 `pipe.Request.Model`（已被 aggregate 改写为首个目标模型）+ 渠道 scope 匹配 → 切换后规则失配。
- 修复方案：新增 `ProxyBeforeAttempt` 事件，`proxyAttempt` 函数内每次尝试触发；`body := pipe.Request.Body` 从循环外移到循环内。

---

### Task 1: 新增 ProxyBeforeAttempt 事件常量

**Files:**
- Modify: `plugins/model-gateway/types.go:175-198`（事件定义区域）

**Step 1: 修改代码**（常量 + 更新文档注释）

在 `ProxyStreamChunk` 与 `ProxyUpstreamFailed` 之间插入：

```go
// ProxyBeforeAttempt 每次上游渠道尝试前触发的 waterfall 事件（输入安检/修改）。
// 与 ProxyBeforeUpstream 的区别：后者只在请求入口执行一次（聚合改写、额度拦截），
// 本事件在 proxyForward 的每个渠道尝试前执行，保证切换渠道/切换模型后安检仍然生效。
const ProxyBeforeAttempt = "proxy:before-attempt"
```

同时把上方事件文档注释（175 行的 `// 插件通过三个 waterfall 事件介入` 及 179-181 行三个 bullet）更新为四个事件：175 行改为 `// 插件通过四个 waterfall 事件介入`，并补充第四行：

```go
//   - ProxyBeforeAttempt：每次渠道尝试前拦截/修改输入（安检能力）
```

**Step 2: 编译验证**

Run: `go build ./plugins/model-gateway/...`
Expected: 编译通过，无错误。

**Step 3: Commit**

```bash
git add plugins/model-gateway/types.go
git commit -m "feat(model-gateway): 新增 proxy:before-attempt 每次尝试安检事件"
```

---

### Task 2: 重构 proxyForward → 抽出 proxyAttempt（核心）

**Files:**
- Modify: `plugins/model-gateway/proxy.go:233-535`
- Test: `plugins/model-gateway/proxy_attempt_test.go`（新建）

**Step 1: 写失败测试**（新建 `proxy_attempt_test.go`）

```go
package modelgateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"loadout/plugins/types"
)

// TestProxyBeforeAttemptFiresPerAttempt 多渠道 failover 时每个渠道尝试都触发
// proxy:before-attempt，注入正确的当前渠道上下文，且第二个渠道收到安检改写后的 body。
func TestProxyBeforeAttemptFiresPerAttempt(t *testing.T) {
	svc, _ := newTestService(t)
	echoA, _ := newEchoServer(t, `{"error":{"message":"boom"}}`, 500, nil)
	defer echoA.Close()
	echoB, getRecordsB := newEchoServer(t, `{"id":"resp_ok","object":"response"}`, 0, nil)
	defer echoB.Close()
	if err := svc.st.Write(types.FileChannels, []types.Channel{
		{ID: "ch-a", Name: "渠道A", BaseURL: echoA.URL, Enabled: true},
		{ID: "ch-b", Name: "渠道B", BaseURL: echoB.URL, Enabled: true},
	}); err != nil {
		t.Fatalf("写渠道表失败: %v", err)
	}

	var mu sync.Mutex
	var attempts []string
	var sawMarked bool
	svc.ctx.On(ProxyBeforeAttempt, func(payload any) (any, error) {
		pipe, ok := payload.(*ProxyPipeline)
		if !ok || pipe == nil {
			return payload, nil
		}
		mu.Lock()
		ch, _ := pipe.Metadata["__current_channel"].(string)
		attempts = append(attempts, ch)
		mu.Unlock()
		// 模拟安检改写 body：加标记字段
		pipe.Request.Body = []byte(`{"marked":true,"model":"gpt-5","input":[{"role":"user","content":"hi"}]}`)
		return pipe, nil
	})

	body := `{"model":"gpt-5","input":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	rr := httptest.NewRecorder()
	svc.HandleProxy(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200", rr.Code)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(attempts) != 2 {
		t.Fatalf("ProxyBeforeAttempt 触发 %d 次, 期望 2（两个渠道各一次）", len(attempts))
	}
	if attempts[0] != "ch-a" || attempts[1] != "ch-b" {
		t.Fatalf("触发渠道顺序 = %v, 期望 [ch-a ch-b]", attempts)
	}
	recs := getRecordsB()
	if len(recs) != 1 {
		t.Fatalf("渠道B收到 %d 个请求, 期望 1", len(recs))
	}
	sawMarked = strings.Contains(string(recs[0].Body), `"marked":true`)
	if !sawMarked {
		t.Fatalf("渠道B收到的 body 不是安检改写后的: %s", recs[0].Body)
	}
}

// TestProxyBeforeAttemptRejectsStopsRequest 安检钩子返回错误时终止整个请求（不换渠道）。
func TestProxyBeforeAttemptRejectsStopsRequest(t *testing.T) {
	svc, _ := newTestService(t)
	echoA, _ := newEchoServer(t, `{"error":{"message":"boom"}}`, 500, nil)
	defer echoA.Close()
	echoB, getRecordsB := newEchoServer(t, `{"id":"resp_ok","object":"response"}`, 0, nil)
	defer echoB.Close()
	if err := svc.st.Write(types.FileChannels, []types.Channel{
		{ID: "ch-a", Name: "渠道A", BaseURL: echoA.URL, Enabled: true},
		{ID: "ch-b", Name: "渠道B", BaseURL: echoB.URL, Enabled: true},
	}); err != nil {
		t.Fatalf("写渠道表失败: %v", err)
	}

	svc.ctx.On(ProxyBeforeAttempt, func(payload any) (any, error) {
		return payload, &GatewayError{Type: "sensitive_filter_error", Msg: "请求命中敏感词规则"}
	})

	body := `{"model":"gpt-5","input":[{"role":"user","content":"bad"}]}`
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	rr := httptest.NewRecorder()
	svc.HandleProxy(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d, 期望 400（安检拒绝）", rr.Code)
	}
	recs := getRecordsB()
	if len(recs) != 0 {
		t.Fatalf("安检拒绝后渠道B不应收到请求, 实际 %d 个", len(recs))
	}
}
```

**Step 2: 运行测试确认失败**

Run: `go test ./plugins/model-gateway/ -run 'TestProxyBeforeAttempt' -v`
Expected: FAIL — `Waterfall("proxy:before-attempt")` 无 handler（mockCtx 无注册），测试期望 2 次触发，实际 0 次。第二个测试因首个渠道 A 返回 500（现有 failover 逻辑）会触发渠道 B，预期 FAIL。

**Step 3: 重构实现**（修改 `proxy.go`）

**整段替换 `proxy.go:295-499`**（循环外的 `body := pipe.Request.Body` / `targetPath := pipe.Request.Path` / `lastErr` 等声明 + 整个 `for` 循环体），替换为下面的新代码。旧循环体 304-498 全部由 `proxyAttempt` 承载，勿残留任何旧变量（`body`/`targetPath` 已移入 `proxyAttempt`，`lastErr` 等由循环内 `res` 收集），否则与新增声明重复编译失败。

在 `proxyForward` 前新增类型与函数：

```go
// attemptResult 一次渠道尝试的失败详情（proxyAttempt 填回，proxyForward 汇总用）。
type attemptResult struct {
	err        error
	channelID  string
	statusCode int
	errorBody  string
	header     http.Header
}

// proxyAttempt 单次渠道尝试：注入当前渠道上下文 → 安检（ProxyBeforeAttempt）→
// 构造并发送上游请求 → 输出处理（非流式 after-hook / 流式逐块）。
//
// 返回 (pipe', handled)：
//   - handled=true：已向客户端写出最终响应（成功，或安检拒绝），调用方应终止转发；
//   - handled=false：本次渠道尝试失败，详情写入 res（供 failover 汇总/切换）。
// pipe' 为安检可能改写后的管线，调用方应更新。
//
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
	if ch.BaseURL != "" {
		pipe.Metadata["__current_channel_base_url"] = ch.BaseURL
	}

	// 安检：每次渠道尝试前执行输入侧能力（敏感词过滤、字段过滤）。
	out, aerr := s.ctx.Waterfall(ProxyBeforeAttempt, pipe)
	if aerr != nil {
		// 安检拒绝（如敏感词 error 模式命中）：请求内容违规，与渠道无关，终止整个请求。
		res.err = aerr
		res.channelID = ch.ID
		s.proxyAttemptLog(r, pipe, model, ch.ID, ch.ChannelName, nil, "", attemptStarted, "failed", "rejected", 0, pipe.Request.Stream, contracts.TokenUsage{}, aerr, truncateErrorBody(aerr.Error()))
		status := http.StatusBadRequest
		if gw, ok := aerr.(*GatewayError); ok && gw.Status != 0 {
			status = gw.Status
		}
		s.proxyFinishLog(r, pipe, model, ch.ID, ch.ChannelName, "failed", status, time.Since(started), aerr, pipe.Request.Stream, contracts.TokenUsage{}, "")
		writeGatewayError(w, aerr)
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
		usage := extractUsageNonStream(respBody)
		s.proxyStreamAttempt(r, pipe, model, ch.ID, ch.ChannelName, nil, "", attemptStarted, resp.StatusCode, false, false, contracts.TokenUsage{})
		out, herr := s.ctx.Waterfall(ProxyAfterUpstream, &AfterUpstreamPayload{Pipe: pipe, Response: proxyResp})
		if herr != nil {
			res.err = herr
			res.channelID = ch.ID
			// 注意：不设置 res.statusCode——与旧实现一致，输出 hook 拒绝视为渠道失败，
			// 最终 writeProxyError 按 statusOrDefault(0, 502) 返回 502，而非透传上游 200。
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

	// 流式：SSE 逐行透传。
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
```

把 `proxyForward` 的循环体替换为：

```go
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
```

原循环体（含 `body`/`targetPath` 声明）已在上文整段删除，此处新循环块自带声明，与 295-499 的删除范围完全对齐，无重复声明。

**Step 4: 运行测试确认通过**

Run: `go test ./plugins/model-gateway/ -run 'TestProxyBeforeAttempt' -v`
Expected: PASS（两个测试）

Run: `go test ./plugins/model-gateway/`
Expected: 全部 PASS（现有测试无 handler 时安检为 no-op，行为不变）

**Step 5: Commit**

```bash
git add plugins/model-gateway/proxy.go plugins/model-gateway/proxy_attempt_test.go
git commit -m "refactor(model-gateway): 抽出 proxyAttempt，每次渠道尝试触发安检"
```

---

### Task 3: 聚合切模型后安检仍触发

**Files:**
- Test: `plugins/model-gateway/proxy_attempt_test.go`（追加）

**Step 1: 写失败测试**（追加到 `proxy_attempt_test.go`）

```go
// TestProxyBeforeAttemptFiresAfterAggregateFailover 聚合模型切目标后，递归的
// 新渠道尝试同样触发 proxy:before-attempt（修复"切模型跳过安检"）。
func TestProxyBeforeAttemptFiresAfterAggregateFailover(t *testing.T) {
	svc, _ := newTestService(t)
	echo1, _ := newEchoServer(t, `{"error":{"message":"down"}}`, 500, nil)
	defer echo1.Close()
	echo2, _ := newEchoServer(t, `{"id":"resp_ok","object":"response"}`, 0, nil)
	defer echo2.Close()
	if err := svc.st.Write(types.FileChannels, []types.Channel{
		{ID: "ch-1", Name: "渠道1", BaseURL: echo1.URL, Enabled: true, Models: []string{"gpt-4o"}},
		{ID: "ch-2", Name: "渠道2", BaseURL: echo2.URL, Enabled: true, Models: []string{"claude-3"}},
	}); err != nil {
		t.Fatalf("写渠道表失败: %v", err)
	}

	// 入口 before 钩子（模拟 aggregate）：虚拟模型 auto → 目标1 gpt-4o
	svc.ctx.On(ProxyBeforeUpstream, func(payload any) (any, error) {
		pipe := payload.(*ProxyPipeline)
		if pipe.Metadata == nil {
			pipe.Metadata = map[string]any{}
		}
		pipe.Metadata["__virtual_model"] = "auto"
		pipe.Metadata["__aggregate_targets"] = []types.AggregateTarget{
			{Model: "gpt-4o", ChannelID: "ch-1"},
			{Model: "claude-3", ChannelID: "ch-2"},
		}
		pipe.Metadata["__failed_targets"] = []string{}
		pipe.Request.Model = "gpt-4o"
		return pipe, nil
	})
	// failover 钩子（模拟 aggregate 切目标2，重设渠道上下文：与真实 applyTargetMetadata
	// 语义一致，__current_channel_base_url 清空、__channel_candidates 置 nil，
	// 否则残留的旧渠道 base_url/候选列表会让递归 resolveChannels 锁死在旧渠道）
	svc.ctx.On(ProxyUpstreamFailed, func(payload any) (any, error) {
		f := payload.(*ProxyFailurePayload)
		f.Pipe.Metadata["__failed_targets"] = append(f.Pipe.Metadata["__failed_targets"].([]string), "gpt-4o@ch-1")
		f.Pipe.Metadata["__current_channel"] = "ch-2"
		f.Pipe.Metadata["__current_channel_base_url"] = ""
		f.Pipe.Metadata["__channel_candidates"] = nil
		f.Pipe.Request.Model = "claude-3"
		return &ProxyRetry{Pipe: f.Pipe}, nil
	})
	// 安检钩子：记录每次触发的 model@渠道
	var mu sync.Mutex
	var seen []string
	svc.ctx.On(ProxyBeforeAttempt, func(payload any) (any, error) {
		pipe := payload.(*ProxyPipeline)
		mu.Lock()
		ch, _ := pipe.Metadata["__current_channel"].(string)
		seen = append(seen, pipe.Request.Model+"@"+ch)
		mu.Unlock()
		return payload, nil
	})

	body := `{"model":"auto"}`
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	rr := httptest.NewRecorder()
	svc.HandleProxy(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200", rr.Code)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("ProxyBeforeAttempt 触发 %d 次, 期望 2（目标1 + 目标2 各一次）: %v", len(seen), seen)
	}
	if seen[0] != "gpt-4o@ch-1" || seen[1] != "claude-3@ch-2" {
		t.Fatalf("安检触发顺序 = %v, 期望 [gpt-4o@ch-1 claude-3@ch-2]", seen)
	}
}
```

**Step 2: 运行测试确认通过**（Task 2 的重构已让递归路径生效）

Run: `go test ./plugins/model-gateway/ -run 'TestProxyBeforeAttemptFiresAfterAggregateFailover' -v`
Expected: PASS

**Step 3: Commit**

```bash
git add plugins/model-gateway/proxy_attempt_test.go
git commit -m "test(model-gateway): 聚合切模型后安检仍触发的回归测试"
```

---

### Task 4: sensitive-filter 改挂 ProxyBeforeAttempt

**Files:**
- Modify: `plugins/sensitive-filter/plugin.go:46-64`
- Modify: `plugins/sensitive-filter/service.go:108-112`（注释）

**Step 1: 修改挂载点**

`plugin.go` 第 63 行：

```go
	// 订阅每次渠道尝试安检事件（proxy:before-attempt）：切换渠道/切换模型后
	// 敏感词过滤仍按当前渠道上下文重新匹配路由执行。
	ctx.On(modelgateway.ProxyBeforeAttempt, svc.HandleProxyBeforeUpstream)
```

`service.go` 第 108-112 行注释更新：

```go
// HandleProxyBeforeUpstream 每次渠道尝试安检 hook（proxy:before-attempt）：
// 对请求体做敏感词过滤。仅处理合法 JSON body（非 JSON 原样透传，避免误伤二进制/表单）；
// 未命中路由、native 路由原样透传。
```

**Step 2: 编译 + 现有测试**

Run: `go build ./plugins/sensitive-filter/... && go test ./plugins/sensitive-filter/`
Expected: PASS（service_test 直接调方法，不受挂载点影响）

**Step 3: Commit**

```bash
git add plugins/sensitive-filter/plugin.go plugins/sensitive-filter/service.go
git commit -m "refactor(sensitive-filter): 改挂 proxy:before-attempt，每次渠道尝试重新安检"
```

---

### Task 5: field-filter 改挂 ProxyBeforeAttempt

**Files:**
- Modify: `plugins/field-filter/plugin.go:52-55`
- Modify: `plugins/field-filter/service.go:110-113`（注释）

**Step 1: 修改挂载点**

`plugin.go` 第 53 行：

```go
	// 请求方向安检挂在每次渠道尝试事件（proxy:before-attempt）：切换渠道/模型后
	// 字段规则按当前渠道上下文重新匹配；响应方向保持 proxy:after-upstream。
	ctx.On(modelgateway.ProxyBeforeAttempt, svc.HandleProxyBeforeUpstream)
	ctx.On(modelgateway.ProxyAfterUpstream, svc.HandleProxyAfterUpstream)
```

`service.go` 第 110-112 行注释更新：

```go
// HandleProxyBeforeUpstream 每次渠道尝试安检 hook（proxy:before-attempt）：
// 转发上游前按配置剔除/保留请求体字段、剔除请求头。
```

**Step 2: 编译 + 现有测试**

Run: `go build ./plugins/field-filter/... && go test ./plugins/field-filter/`
Expected: PASS

**Step 3: Commit**

```bash
git add plugins/field-filter/plugin.go plugins/field-filter/service.go
git commit -m "refactor(field-filter): 改挂 proxy:before-attempt，每次渠道尝试重新安检"
```

---

### Task 6: 全量回归 + 文档

**Files:**
- Test: 全部相关插件

**Step 1: 全量测试**

Run: `go test ./plugins/model-gateway/ ./plugins/sensitive-filter/ ./plugins/field-filter/ ./plugins/aggregate/ ./plugins/volc-free-quota/ ./core/plugin/`
Expected: 全部 PASS

注意检查：
- `aggregate` 测试（聚合 before 钩子在入口的行为不变）
- `volc-free-quota` 测试（入口额度拦截不变）
- `proxy_vision_log_test.go`（入口 before 钩子 handler 行为不变）

**Step 2: 端到端冒烟**（可选，有真实渠道配置时）

手动验证：后台配置敏感词规则绑定渠道 A；请求经渠道 A（失败）→ 渠道 B，检查 B 收到的 body 已过滤、route-log 两条 attempt 均走安检。

**Step 3: Commit**

```bash
git add -A
git commit -m "chore: 每次渠道尝试安检管线回归验证"
```

---

## 设计决策记录

1. **为什么新事件而非复用 ProxyBeforeUpstream**：入口事件还挂着 aggregate（模型改写）和 volc-free-quota（额度拦截），它们有 `__aggregate_processed` 等防重标记、且额度拦截语义是"整请求级"。复用会引入语义混乱。
2. **安检拒绝 = 终止整个请求**（不换渠道）：敏感词 error 模式是内容违规，与渠道无关，换渠道也违规。与入口行为一致。
3. **`__current_channel` 每次尝试覆盖写入**：同模型多渠道场景，下一次尝试会覆盖上一渠道的值；聚合递归场景，failover 钩子会先重设渠道上下文（真实 aggregate 的 `applyTargetMetadata` 会清 `__current_channel_base_url`、置 `__channel_candidates=nil`，本函数写入只作兜底）。`__current_channel_base_url` 残留值在非聚合场景无副作用（无后续 resolve），聚合场景由 failover 钩子清掉。
4. **vision_v2 不动**：挂在 after-upstream，每次渠道尝试本就执行。
5. **性能**：每次尝试多一次能力路由表查询 + 过滤执行（毫秒级）。后续可优化为"model 变化时才重查路由"，本次不做（YAGNI）。
6. **审查修正记录（子代理 code review）**：
   - P0-1：`contracts.RouteAttempt.StepNo` 类型为 `string`（routing.go:102），attempt 日志处必须 `fmt.Sprintf("%d", snap)`（已修正）。
   - P0-2：重构删除范围统一为"整段替换 proxy.go:295-499"，避免旧声明残留导致重复声明编译失败（已修正）。
   - P1-1：输出 hook 拒绝分支不设置 `res.statusCode`，保持原语义（最终 502 而非透传上游 200）（已修正）。
   - P1-2：Task 3 聚合 mock 的 failover 钩子须同时清 `__current_channel_base_url` 与 `__channel_candidates`，复刻真实 `applyTargetMetadata`（已修正）。
   - P2：Task 1 注释"三个事件"→"四个"；proxyForward 行号 233-535；安检拒绝日志状态码从 `GatewayError.Status` 提取（均已修正）。
