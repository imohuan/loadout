# vision_v2 工具调用式图片识别插件 实现计划（修订版 v3：三格式全支持）

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 新建 `plugins/vision_v2` 插件，把图片识别从"请求前偷偷改写"改为"占位符 + 模型主动工具调用"模式，并在注册表注释掉旧 `plugins/vision`。**支持 chat/completions、claude messages、responses 三格式**。

**Architecture:** 请求侧（ProxyBeforeUpstream，三格式）把图片块替换为 `<vision_img_{hash12}>` 占位文本、图片落盘 `vision-files/{id}.bin`、按格式注入 `look_at_image` 工具（chat 的 `function` 嵌套 / claude 的 `name+input_schema` / responses 的平铺 `type+name+parameters`）。响应侧（ProxyStreamChunk 流式 / ProxyAfterUpstream 非流式，按格式解析）检测模型调用 `look_at_image` 时，在 **[DONE]/流结束 chunk 的 hook 调用内**同步执行工具循环——按 `image_id` 读文件、带 `prompt` 方向识别（缓存 `md5(id|prompt|v3)`）、识别过程按**当前响应格式**写流、按格式构造工具结果消息发起新请求续流。输出流过滤占位符泄露。工具循环上限 5 轮。

**Tech Stack:** Go 1.22+（module `loadout`）、标准库、modernc.org/sqlite（core/db）、httptest（测试，不用 fakellm 多阶段）。

---

## 修订记录

- **v3（本版）**：按用户要求，P0-1 决策反转——**三格式全支持**，删除"仅 chat/completions"限定。新增 `formatAdapter` 抽象隔离三格式协议差异（工具 schema、图片块、工具调用事件、工具结果消息、识别流输出格式）。
- v2：子代理 review 修复 4 P0 / 7 P1 / 3 P2（见 v2 版修订记录，除 P0-1 外全部保留）：BeforeUpstream 主处理器编排；工具循环触发点在 [DONE] chunk hook 内；Service 并发安全状态；NewService 设 cacheDir；DescribeImage 统一签名；`{id}.bin` + DetectContentType；新请求复用原渠道；httptest 多阶段；route-log；非流式错误不触发 failover；id 12 位；SSE 按 index；懒清理；落盘原始字节。

## 关键设计决策

1. **v2 独立包**，不 import 旧 `plugins/vision`。压缩/渠道解析/视觉模型调用从旧代码复制。
2. **图片 ID = 内容 md5 截断 12 位**。同图跨会话/模型同 ID；ID 即文件名。
3. **落盘**：`{config.VisionCacheDir}/files/{id}.bin`（原始字节），describe 时 `http.DetectContentType`。
4. **缓存**：`{config.VisionCacheDir}/md5(id|prompt|v3).txt`，TTL 复用 `VisionCacheTTLHours`。
5. **URL 图片**：下载落盘，失败保留原块。
6. **能力路由复用**：复制旧 `DecideRouteScope`——native 透传 / proxy 占位符+工具 / error 报错。
7. **三格式适配（formatAdapter）**：
   | 维度 | chat/completions | claude /v1/messages | responses /v1/responses |
   |---|---|---|---|
   | 图片块 type | `image_url` | `image`（source） | `input_image` |
   | 文本块 type | `text` | `text` | `input_text` |
   | 消息数组字段 | `messages` | `messages` | `input` |
   | 工具 schema | `{"type":"function","function":{"name","description","parameters"}}` | `{"name","description","input_schema"}` | `{"type":"function","name","description","parameters"}` |
   | 流式工具调用事件 | `choices[].delta.tool_calls` | `content_block_start`(tool_use) + `content_block_delta`(input_json_delta) + `content_block_stop` | `response.output_item.added`(function_call) + `response.function_call_arguments.delta` + `response.output_item.done` |
   | 结束信号 | `finish_reason:"tool_calls"` | `message_delta.stop_reason:"tool_use"` + `message_stop` | `response.output_item.done` 收齐 |
   | 工具结果消息 | `{"role":"tool","tool_call_id","content"}` | `{"role":"user","content":[{"type":"tool_result","tool_use_id","content"}]}` | input 追加 `{"type":"function_call_output","call_id","output"}` |
   | assistant 工具消息 | 顶层 `tool_calls` | content 数组 `{"type":"tool_use","id","name","input"}` | input 追加 `{"type":"function_call","id","call_id","name","arguments"}` |
   | 识别流输出格式 | `choices[].delta.reasoning_content` | `content_block_delta`(text_delta) | `response.output_text.delta` |
8. **工具循环**：上限 5 轮。流式：首轮流透传内容、拦截工具调用，流结束 chunk hook 内执行循环并续流；非流式：循环后替换响应 Body。
9. **占位符泄露过滤**：透传 content 含 `<vision_img_` → 剔除（累积缓冲防跨块）。
10. **渠道复用**：工具循环新请求读 `pipe.Metadata["__last_tried_channel"]` → `__current_channel` → 无则用路由 via_options 首渠道，走 `resolveChannel`。

---

### Task 0: 插件骨架 + Service 状态管理 + 注册切换

**Files:**
- Create: `plugins/vision_v2/plugin.go`、`plugins/vision_v2/service.go`
- Modify: `plugins/registry.go`
- Test: `plugins/vision_v2/service_test.go`

**Step 1: plugin.go** — 同 v2 版（订阅 ProxyBeforeUpstream / ProxyStreamChunk / ProxyAfterUpstream）。

**Step 2: service.go** — 同 v2 版：`toolLoopState`（acc/pending/calls/active/round + `format visionProxyFormat` + `filter *PlaceholderFilter`），`Service` 含 `mu` + `states map[string]*toolLoopState`，`NewService` 设 `cacheDir=config.VisionCacheDir`。

```go
type toolLoopState struct {
	format  visionProxyFormat    // chat / claude / responses
	acc     *StreamAccumulator   // 按格式解析（内部持 format）
	pending bool
	calls   []ToolCall
	active  bool
	round   int
	filter  *PlaceholderFilter
}
// state()/dropState() 带锁；dropState 在流结束/错误路径调用。
```

**Step 3: registry.go** — 注释 `vision`、注册 `visionv2`。

**Step 4: 验证** — `go build ./... && go vet ./plugins/vision_v2/`。

**Step 5: Commit** — `feat(vision-v2): 骨架 + 并发安全状态 + 注册切换`

---

### Task 1: 图片落盘（image_store.go）

**Files:** Create `image_store.go` + 测试
**实现：同 v2 版**——`imageID(data) string`（md5 前 12 位）、`SaveImageDataURI`/`SaveImageURL` → `saveBytes` 落 `files/{id}.bin`（幂等）、`loadImageBytes(id) (raw, mime, err)`（`http.DetectContentType`）、`cleanupStaleFiles`（懒清理 TTL 孤儿）。
**测试：同 v2 版**（12 位 id、幂等、非法输入报错、文件存在）。
**Commit:** `feat(vision-v2): 图片落盘（12 位内容 hash，懒清理）`

---

### Task 2: HandleProxyBeforeUpstream 主处理器（三格式编排）

**Files:** Create `rewrite.go`（三格式图片→占位符，复制旧 vision/proxy.go 的 `visionFormatByPath/proxyMessageArray/imageURLValue/claudeImageValue/textBlockType/replaceMessagesBody`）、Create `routes.go`（复制旧 `DecideRouteScope/channelScopeFromMetadata/requestChannelBaseURL`）、Test `rewrite_test.go`

**Step 1: 失败测试（三格式各一条）**

```go
func TestRewriteChat(t *testing.T)  // chat/completions + image_url → <vision_img_、无 image_url、tools 注入
func TestRewriteClaude(t *testing.T) // messages + image(source) → <vision_img_、无 image、tools 注入(claude schema)
func TestRewriteResponses(t *testing.T) // responses + input_image → <vision_img_、无 input_image、tools 注入(responses schema)
func TestRewriteNativePassthrough(t *testing.T) // 路由 native → 原样返回
func TestRewriteNonVisionPath(t *testing.T) // /v1/other → 原样返回
```

**Step 2/3: 实现**

```go
func (s *Service) HandleProxyBeforeUpstream(payload any) (any, error) {
	pipe, ok := payload.(*modelgateway.ProxyPipeline)
	if !ok || pipe == nil || pipe.Request == nil || len(pipe.Request.Body) == 0 { return payload, nil }
	format, ok := visionFormatByPath(pipe.Request.Path) // 三格式都返回 ok
	if !ok { return payload, nil }
	// 路由
	scope := channelScopeFromMetadata(pipe.Metadata, s.requestChannelBaseURL)
	route, err := s.DecideRouteScope(pipe.Request.Model, scope)
	if err != nil { return nil, visionError(err.Error()) }
	if route == nil || route.Route == types.RouteNative { return payload, nil }
	if route.Route == types.RouteError { return nil, visionError(fmt.Sprintf("模型 %q 不支持视觉能力", pipe.Request.Model)) }

	// 图片 → 占位符（按格式）
	var bodyMap map[string]any
	dec := json.NewDecoder(bytes.NewReader(pipe.Request.Body))
	dec.UseNumber()
	if err := dec.Decode(&bodyMap); err != nil { return payload, nil }
	messages := proxyMessageArray(bodyMap, format)
	if len(messages) == 0 { return payload, nil }
	changed, err := s.rewriteImagesToPlaceholders(messages, format)
	if err != nil { return nil, visionError(fmt.Sprintf("图片落盘失败: %v", err)) }
	if !changed { return payload, nil }

	// 工具注入（按格式）
	bodyMap["tools"] = ensureLookAtImageTool(bodyMap["tools"], format)

	newBody, err := json.Marshal(bodyMap)
	if err != nil { return nil, visionError(err.Error()) }
	pipe.Request.Body = newBody
	pipe.Metadata["__vision_v2_active"] = true
	pipe.Metadata["__vision_v2_format"] = formatName(format)
	pipe.Metadata["__vision_v2_route"] = route
	s.lg.Info("vision_v2: 图片替换为占位符", "path", pipe.Request.Path, "model", pipe.Request.Model)
	return pipe, nil
}

// rewriteImagesToPlaceholders 遍历消息 content，图片块 → <vision_img_{id}> 文本块。
func (s *Service) rewriteImagesToPlaceholders(messages []any, format visionProxyFormat) (bool, error) {
	changed := false
	for mi := range messages {
		msg, ok := messages[mi].(map[string]any)
		if !ok { continue }
		content, ok := msg["content"].([]any)
		if !ok { continue }
		for ci := range content {
			part, ok := content[ci].(map[string]any)
			if !ok { continue }
			var img string
			switch format {
			case formatChat:      if t, _ := part["type"].(string); t == "image_url" { img = imageURLValue(part["image_url"]) }
			case formatClaude:    if t, _ := part["type"].(string); t == "image" { img = claudeImageValue(part["source"]) }
			case formatResponses: if t, _ := part["type"].(string); t == "input_image" { img = imageURLValue(part["image_url"]) }
			}
			if img == "" { continue }
			id, err := s.saveImage(pipeCtx, img) // data URI→SaveImageDataURI；http(s)→SaveImageURL
			if err != nil { s.lg.Warn("图片落盘失败，保留原块", "err", err); continue }
			content[ci] = map[string]any{"type": textBlockType(format), "text": placeholderPrefix + id + placeholderSuffix}
			changed = true
		}
	}
	return changed, nil
}
```

**Step 4/5: PASS + Commit** — `feat(vision-v2): BeforeUpstream 三格式编排`

---

### Task 3: 工具注入（tools.go，三格式 schema）

**Files:** Create `tools.go` + 测试

**Step 1: 失败测试**

```go
func TestEnsureToolChat(t *testing.T)       // tools 数组元素 function 嵌套；已有同名跳过
func TestEnsureToolClaude(t *testing.T)     // {"name","description","input_schema"}；已有同名跳过
func TestEnsureToolResponses(t *testing.T)  // {"type":"function","name","description","parameters"}；已有同名跳过
```

**Step 2/3: 实现（三模板，共用描述文本）**

```go
const lookAtImageToolName = "look_at_image"
const lookAtImageToolDesc = "图片以 <vision_img_xxx> 标记形式出现在对话中（xxx 为图片 id）。需要查看图片内容时调用本工具：把标记中的 id 传给 image_id，并用 prompt 说明你想从图片中获取的信息方向（如颜色、文字、布局、产品、实体关系等）。支持一次传多张图。不要输出标记本身。"

func ensureLookAtImageTool(tools any, format visionProxyFormat) []any {
	existing, _ := tools.([]any)
	for _, raw := range existing { // 同名检测（格式不同字段名不同，分别判断）
		if tool, ok := raw.(map[string]any); ok && toolHasName(tool, format) { return existing }
	}
	return append(existing, toolSchema(format))
}

func toolSchema(format visionProxyFormat) map[string]any {
	switch format {
	case formatClaude:
		return map[string]any{"name": lookAtImageToolName, "description": lookAtImageToolDesc,
			"input_schema": map[string]any{"type": "object", "properties": map[string]any{
				"image_id": map[string]any{"type": "string", "description": "图片 id"},
				"prompt":   map[string]any{"type": "string", "description": "识别方向"},
				"image_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}},
				"required": []any{"image_id"}}}
	case formatResponses:
		return map[string]any{"type": "function", "name": lookAtImageToolName, "description": lookAtImageToolDesc,
			"parameters": map[string]any{"type": "object", "properties": map[string]any{
				"image_id": map[string]any{"type": "string"}, "prompt": map[string]any{"type": "string"},
				"image_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}},
				"required": []any{"image_id"}}}
	default: // formatChat
		return map[string]any{"type": "function", "function": map[string]any{
			"name": lookAtImageToolName, "description": lookAtImageToolDesc,
			"parameters": map[string]any{"type": "object", "properties": map[string]any{
				"image_id": map[string]any{"type": "string"}, "prompt": map[string]any{"type": "string"},
				"image_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}},
				"required": []any{"image_id"}}}}
	}
}

// toolHasName 按格式判断工具元素是否名为 look_at_image：
// chat: tool["function"]["name"]；claude: tool["name"]；responses: tool["name"]。
```

**Step 4/5: PASS + Commit** — `feat(vision-v2): 三格式工具 schema 注入`

---

### Task 4: 缓存（cache.go）

**Files:** Create `cache.go` + 测试
**实现：同 v2 版**——`visionCacheKey(id, prompt) = md5(id|prompt|v3)`、`readCache`/`writeCache`（TTL）。
**Commit:** `feat(vision-v2): 缓存 key=md5(id|prompt|v3)`

---

### Task 5: 按 id 识别（describe.go）

**Files:** Create `describe.go` + 测试
**实现：同 v2 版**——`DescribeImage(ctx, id, prompt, streamWriter, ch)`（缓存命中返回 hit；miss → loadImageBytes → CompressDataURI → callVision 单渠道 → 写缓存 + cleanupStaleFiles）；`callVision` 构造 user 消息 `image_url(data URI) + text("识别方向: {prompt}\n\n{内置模板}")`，走 `{ch.BaseURL}/chat/completions`（**视觉模型调用统一走 chat 格式，与主请求格式无关**——视觉模型是独立调用）。`readVisionStream` 复制旧实现。
**Commit:** `feat(vision-v2): 按 id+方向识别（缓存优先）`

---

### Task 6: SSE 流解析（stream.go，三格式）

**Files:** Create `stream.go` + 测试

**Step 1: 失败测试（chat 多工具 + claude + responses 各一条）**

```go
func TestStreamChatMultiTool(t *testing.T)     // 同 v2 版（index 分离）
func TestStreamClaudeToolUse(t *testing.T)     // content_block_start(tool_use) + content_block_delta(input_json_delta) + content_block_stop → 1 个调用
func TestStreamResponsesToolCall(t *testing.T) // output_item.added(function_call) + function_call_arguments.delta + output_item.done → 1 个调用
```

**Step 2: FAIL** — 未定义。

**Step 3: 实现（StreamAccumulator 按格式分发）**

```go
type ToolCall struct {
	ID      string
	Name    string
	ImageID string
	Prompt  string
}

// StreamAccumulator 三格式工具调用解析。SSE 行可能是 "data: {...}"（chat/responses）
// 或 "event: xxx" / "data: {...}" 两行（claude），Feed 按行累积内部状态。
type StreamAccumulator struct {
	format visionProxyFormat
	mu     sync.Mutex
	chat   *chatAcc     // chat: by index
	claude *claudeAcc   // claude: by content block index, tool_use_id/name 从 content_block_start 记
	resp   *respAcc     // responses: by item_id
}

func NewStreamAccumulator(format visionProxyFormat) *StreamAccumulator { ... }

// Feed 处理一行；工具调用收齐后追加到 *calls。
func (a *StreamAccumulator) Feed(line string, calls *[]ToolCall) {
	switch a.format {
	case formatClaude:
		// 行以 "event: " 开头记录当前事件名；"data: " 解析 JSON：
		//   content_block_start: 记 tool_use {index, id, name}（input 可能为空对象）
		//   content_block_delta: index 匹配时累积 delta.input_json_delta.partial_json
		//   content_block_stop: index 收齐 → parseToolCall → append
		//   message_stop: 无收齐的 tool_use 强制收齐（防御）
	case formatResponses:
		// response.output_item.added: item{type:function_call, id, call_id, name} → 建 acc
		// response.function_call_arguments.delta: 按 item_id 累积 delta
		// response.output_item.done: item.arguments 完整 → parseToolCall → append
	default: // formatChat（同 v2 版按 index）
	}
}
```

> **Claude 行格式注意**：Claude SSE 是 `event: content_block_delta\ndata: {...}\n\n`。解析器必须先捕获 `event:` 行确定事件类型，再解析 `data:` 行。chat/responses 只有 `data:` 行（事件类型在 JSON 的 `type` 字段）。

**Step 4/5: PASS + Commit** — `feat(vision-v2): 三格式 SSE 工具调用解析`

---

### Task 7: 工具循环 + 流式续流（tool_loop.go，三格式）

**Files:** Create `tool_loop.go` + 测试

**Step 1: 失败测试（chat 流式端到端；claude/responses 构造函数单测）**

```go
func TestToolLoopStreamChat(t *testing.T) // httptest 上游第 1 次 tool_calls 流、第 2 次文本流；断言流中含"图片理解"、最终文本、无 <vision_img_、无 tool_calls
func TestBuildToolMessagesChat(t *testing.T)     // calls → assistant(tool_calls) + role:tool 消息
func TestBuildToolMessagesClaude(t *testing.T)   // calls → assistant content tool_use 块 + user tool_result 块
func TestBuildToolMessagesResponses(t *testing.T) // calls → input 追加 function_call + function_call_output
```

**Step 2: FAIL** — 未定义。

**Step 3: 实现**

```go
const maxToolRounds = 5

// HandleProxyStreamChunk：透传 content（占位符过滤）；工具调用相关 chunk 置 nil；
// 流结束（chat: [DONE]；claude: message_stop；responses: response.completed 或 EOF）
// 且本轮流有工具调用 → 在此次 hook 调用内同步执行 executeToolLoop。
func (s *Service) HandleProxyStreamChunk(payload any) (any, error) {
	sp, ok := payload.(*modelgateway.StreamChunkPayload)
	if !ok || sp == nil || sp.Data == nil || sp.Pipe == nil { return payload, nil }
	st := s.state(sp.Pipe.RequestID)
	if st.active { return nil, nil }
	line := string(sp.Data)
	if st.filter != nil {
		if cleaned := st.filter.Filter(line); cleaned != line { sp.Data = []byte(cleaned); line = cleaned }
	}
	if !st.pending {
		st.acc.Feed(line, &st.calls)
		if len(st.calls) > 0 { st.pending = true }
	}
	if st.pending {
		sp.Data = nil // 工具调用相关行不转发
		if isStreamEnd(line, st.format) {
			_, err := s.executeToolLoop(sp.Pipe, st)
			s.dropState(sp.Pipe.RequestID)
			if err != nil {
				writeSSEErrorChunk(sp.Pipe.ResponseWriter, err.Error()) // 按当前格式写错误块
				return nil, nil
			}
		}
	}
	return sp, nil
}

// isStreamEnd 按格式判断流结束标记行：
// chat: data: [DONE]；claude: event: message_stop；responses: data: {"type":"response.completed"} 或 EOF。
func isStreamEnd(line string, format visionProxyFormat) bool { ... }

// executeToolLoop 工具循环（三格式共用骨架，消息构造走 formatAdapter）：
//  1. 逐个执行工具 → DescribeImage（识别流按 st.format 写）→ 收集结果
//  2. 按格式构造 assistant 工具消息 + 工具结果消息，append 到 messages
//  3. 新请求（复用原渠道）→ 读新流逐行写客户端（占位符过滤）→ 继续检测工具调用
//  4. 无新调用或超轮数退出
func (s *Service) executeToolLoop(pipe *modelgateway.ProxyPipeline, st *toolLoopState) (bool, error) {
	bodyMap, err := pipeBodyMap(pipe.Request.Body)
	if err != nil { return false, err }
	messages := proxyMessageArray(bodyMap, st.format)
	for st.round < maxToolRounds {
		calls := st.calls
		if len(calls) == 0 { return true, nil }
		var toolResults []any
		for _, c := range calls {
			if c.Name != lookAtImageToolName { continue }
			ch := s.channelForTool(pipe)
			if ch == nil { return false, errors.New("vision_v2: 无法定位主链路渠道") }
			text, hit, err := s.DescribeImage(pipe.HTTPRequest.Context(), c.ImageID, c.Prompt, s.toolStreamWriter(pipe, st.format), *ch)
			if err != nil { return false, err }
			_ = hit
			s.visionAttempt(pipe.RequestID, c.ImageID, hit, err) // route-log
			toolResults = append(toolResults, buildToolResultMessage(c, text, st.format))
		}
		if len(toolResults) == 0 { return true, nil }
		messages = append(messages, buildAssistantToolMessage(calls, st.format))
		messages = append(messages, toolResults...)
		bodyMap[msgArrayKey(st.format)] = messages
		reqBody, _ := json.Marshal(bodyMap)
		resp, err := s.doUpstream(ctxFor(pipe), reqBody, pipe)
		if err != nil { return false, err }
		var newCalls []ToolCall
		acc := NewStreamAccumulator(st.format)
		flusher, _ := pipe.ResponseWriter.(http.Flusher)
		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadString('\n')
			if len(line) > 0 {
				if _, werr := fmt.Fprint(pipe.ResponseWriter, line); werr != nil { return false, werr }
				if flusher != nil { flusher.Flush() }
				acc.Feed(line, &newCalls)
			}
			if isStreamEnd(line, st.format) || err != nil { break }
		}
		resp.Body.Close()
		if len(newCalls) == 0 { return true, nil }
		st.calls = newCalls
		st.round++
	}
	return false, fmt.Errorf("vision_v2: 工具循环超过 %d 轮", maxToolRounds)
}

// buildAssistantToolMessage / buildToolResultMessage 按格式构造（见决策表）：
//   chat:     assistant{top-level tool_calls} + {"role":"tool","tool_call_id","content"}
//   claude:   assistant{content:[{"type":"tool_use","id","name","input"}]} + {"role":"user","content":[{"type":"tool_result","tool_use_id","content"}]}
//   responses: input 追加 {"type":"function_call","id","call_id","name","arguments"} + {"type":"function_call_output","call_id","output"}

// toolStreamWriter 按格式把识别 delta 写成 SSE 块（前缀"图片理解"）：
//   chat:      {"choices":[{"delta":{"reasoning_content":delta}}]}
//   claude:    {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":delta}}
//   responses: {"type":"response.output_text.delta","item_id":"msgalert","output_index":0,"delta":delta}
func (s *Service) toolStreamWriter(pipe *modelgateway.ProxyPipeline, format visionProxyFormat) func(string) error { ... }

// channelForTool / doUpstream：读 __last_tried_channel → __current_channel → 路由 via_options 首渠道；
// 复用原渠道 baseURL/APIKey + 请求 header；POST {baseURL}/{原path}。
```

**Step 4/5: PASS + Commit** — `feat(vision-v2): 三格式流式工具循环 + 续流`

---

### Task 8: 非流式路径（after.go，三格式）

**Files:** Create `after.go` + 测试

**Step 1: 失败测试**

```go
func TestAfterChatToolLoop(t *testing.T)       // 上游返回 finish_reason=tool_calls + tool_calls → 循环 → Body 替换为最终文本
func TestAfterClaudeToolLoop(t *testing.T)     // 上游返回 content 含 tool_use + stop_reason=tool_use → 循环
func TestAfterResponsesToolLoop(t *testing.T)  // 上游返回 output 含 function_call → 循环
func TestAfterErrorNoFailover(t *testing.T)    // 工具执行失败 → 返回 nil error + 错误 Body（不触发 failover）
```

**Step 2: FAIL** — 未定义。

**Step 3: 实现**

```go
func (s *Service) HandleProxyAfterUpstream(payload any) (any, error) {
	ap, ok := payload.(*modelgateway.AfterUpstreamPayload)
	if !ok || ap == nil || ap.Response == nil || ap.Pipe == nil { return payload, nil }
	format, ok := visionFormatByPath(ap.Pipe.Request.Path)
	if !ok || !s.isActive(ap.Pipe) { return payload, nil }
	calls := parseToolCallsNonStream(ap.Response.Body, format) // 三格式解析（见决策表）
	if len(calls) == 0 { return payload, nil }
	bodyMap, err := pipeBodyMap(ap.Pipe.Request.Body)
	if err != nil { return payload, nil }
	messages := proxyMessageArray(bodyMap, format)
	// 工具循环（非流式，复用 executeToolLoop 骨架，但新请求非流式、最终 Body 直接替换）
	final, err := s.toolLoopNonStream(ap.Pipe, messages, calls, format)
	if err != nil {
		errBody, _ := json.Marshal(map[string]any{"error": map[string]any{
			"message": "视觉工具执行失败: " + err.Error(), "type": "vision_capability_error"}})
		ap.Response.StatusCode = http.StatusBadGateway
		ap.Response.Body = errBody
		return ap, nil // 不 return error（P1-11：避免触发渠道 failover）
	}
	ap.Response.Body = final
	return ap, nil
}
```

**Step 4/5: PASS + Commit** — `feat(vision-v2): 三格式非流式工具循环`

---

### Task 9: 集成验证 + 全量回归

**Step 1:**

```bash
go build ./...
go vet ./plugins/vision_v2/ ./plugins/model-gateway/
go test ./plugins/vision_v2/ -count=1
go test ./plugins/model-gateway/ -count=1
```
Expected: 全绿（`TestVisionE2EFlushOnSuccess/FlushOnFail` 为既有测试 bug，允许失败）。

**Step 2: 手动端到端（三格式各一次）**

```bash
# chat
curl -s http://127.0.0.1:PORT/v1/chat/completions -d '{"model":"...","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"看图"},{"type":"image_url","image_url":{"url":"data:image/png;base64,..."}}]}]}'
# claude
curl -s http://127.0.0.1:PORT/v1/messages -d '{"model":"...","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"看图"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"..."}}]}],"tools":[...]}'
# responses
curl -s http://127.0.0.1:PORT/v1/responses -d '{"model":"...","stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"看图"},{"type":"input_image","image_url":"data:image/png;base64,..."}]}],"tools":[...]}'
```

预期：三格式各自 → 图片变 `<vision_img_xxx>`、tools 注入对应格式、模型调工具时识别流按对应格式输出、最终回复无占位符泄露。

**Step 3: Commit** — `feat(vision-v2): 集成验证`

---

## 风险与回滚

- **模型不调工具**：占位符被忽略 → 图片内容不进上下文。缓解：工具描述强制 + 实测；模型从不调用则 1 分钟回滚 registry.go 切 v1。
- **Claude/Responses 流式事件结构**：以官方协议为准（本项目无既有 claude/responses 流式工具调用代码可抄），Task 6 单测先行锁定解析；若实测与上游实现有出入，以单测调整。
- **chunk hook 内同步工具循环**：主循环被阻塞是有意设计，顺序安全；超时由 VisionTimeout + 客户端 ctx 控制。
- **git 历史**：仓库 8/22 压平为单 commit（6b2d291），本计划 commit 在其上追加。

## 验收标准

1. 三格式带图请求 → 上游只见 `<vision_img_{id12}>`，tools 按格式注入，图片落盘
2. 模型调用工具 → 按 `md5(id|prompt|v3)` 缓存；同图同方向命中；换方向重识别
3. 识别过程按当前响应格式流输出（"图片理解"）；输出流无占位符泄露；工具调用不透传
4. 换模型/换提供商 → 同 id 缓存复用（key 不含模型名）
5. claude/responses 路径完整工作（请求改写 + 工具调用 + 续流）
6. 注册表旧 vision 停用、vision_v2 生效，`go build ./...` 通过
