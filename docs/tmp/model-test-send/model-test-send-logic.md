# 模型测试页「发送」按钮后台实现逻辑调查

调查对象：测试页（ModelTestView）发送按钮 → 后端完整链路。
用途：为多模态 MCP 插件的识别调用（打自家网关 + 上传取 key）提供参考。

## 一、前端链路

### 1. 发送按钮 → send()
`frontend/src/views/ModelTestView.vue` `send()`：
- 校验：选渠道或填 Base URL；自带模式要选 SK key；填模型名；有消息或附件。
- 图片附件转 base64 data URL（`blobUrlToDataUrl`，后台代理不支持 blob URL）。
- 构造 `requestMessages`：左侧已编辑消息 + 本次输入（含 `image_url` 图片块）。
- 调 `modelTest.chat(buildTarget(), config.model, requestMessages, {...})`，`stream: true`。

### 2. buildTarget() 决定目标来源（三种模式）
```
channel_id === BUILTIN_CHANNEL  → { suffix_mode, sk_key_hash, base_url }
channel_id 非空（选渠道）        → { suffix_mode, channel_id, api_key? }
否则（直接填 Base URL）         → { suffix_mode, base_url, api_key }
```
- `BUILTIN_CHANNEL` = "Loadout 自带"模式：传 `sk_key_hash`（自建 SK key 的哈希），后端解析明文调自家网关。
- `suffix_mode`：chat / gpt / claude → 决定上游路径后缀 `/chat/completions` / `/responses` / `/messages`。

### 3. chat() → fetch('/api/test/chat')
`frontend/src/composables/useModelTest.ts`：
- `POST /api/test/chat`，body `{...target, model, messages, stream:true}`。
- SSE 逐块解析增量文本（`extractTestDelta` 按 suffix_mode 处理三种协议）。
- 访问摘要回带：响应头 `X-Test-Log`（非流式/错误）+ SSE 末尾 `route_log` 事件（流式成功），`decodeTestLogSummary` 解析后前端「请求记录」面板直显。

## 二、后端链路

### 路由注册
`plugins/admin-api/service.go`：
- `POST /api/test/models` → `handleTestModels`（AuthSession）
- `POST /api/test/chat` → `handleTestChat`（AuthSession）

### testTarget / testChatRequest
`plugins/admin-api/test_proxy.go`：
- `testTarget`：`channel_id` / `base_url` / `api_key` / `sk_key_hash` / `suffix_mode`。
- `testChatRequest`：testTarget + `model` / `messages` / `stream` / `temperature` / `max_tokens`。

### resolveTestTarget 解析 base_url + 明文 key（核心）
优先级：
1. `sk_key_hash` 非空 → 「Loadout 自带」：
   - base_url 相对路径（/v1 或空）→ 用请求 Host 补全 `scheme://Host/v1`（对齐自家网关挂载路径）；
   - 完整 URL（http(s)://...）→ 直接用；
   - `s.keys.ResolveAPIKey(hash)` 解析出明文 key（**明文不出服务端**）。
2. `channel_id` 非空 → 渠道记录为准取 base_url；key 优先级：请求自定义 key > 渠道存储 key（`st.Decrypt(APIKeyCipher)`）。
3. 否则 → 临时 `base_url` + `api_key`。

### handleTestChat 流程
1. 解析目标 → baseURL + apiKey。
2. 校验 model / messages 非空；自带模式 channelID 标记为 `__builtin__`。
3. `buildTestPayload` 按 suffix_mode 转换 payload：
   - chat → `buildChatPayload`（原样透传，含 image_url 图片块，stream 加 stream_options）。
   - gpt → `buildResponsesPayload`（messages→input，image_url→input_image）。
   - claude → `buildClaudePayload`（system 抽到顶层，image→image+source）。
4. `POST baseURL + testSuffixPath(suffix_mode)`，`Authorization: Bearer apiKey`（有 key 才带）。
5. 非流式：读 body，写访问摘要到响应头 `X-Test-Log`。
6. 流式：`streamTestUpstream` 逐行透传 SSE，末尾追加 `route_log` 事件（含最终 tokens）。
7. 测试请求**不写转发日志**，访问摘要随响应回带；仅当上游是 Loadout 自身导出服务时由 router 内部写日志。

## 三、对多模态插件的关键启发

1. **「Loadout 自带」模式就是参考**：`sk_key_hash` 按哈希解析明文 key，调自家网关。多模态插件若内部直接调网关，可绕过这层（不需外部 HTTP 认证）。
2. **suffix_mode 三协议转换已有完整实现**（buildChatPayload/buildResponsesPayload/buildClaudePayload），多模态插件可复用或参考，尤其是图片块的三种协议转换。
3. **上传取 key**：`channel_id` 模式后端解密渠道 key 的逻辑（`st.Decrypt(APIKeyCipher)`）就是多模态大文件上传要复用的——按 Base URL 识别方舟渠道、取 key、调 `/v3/files`。
4. **base_url 相对路径补全规则**：`/v1` → `scheme://Host/v1`，可复用。
