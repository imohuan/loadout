# 图片识别插件 → 追加视频理解：代码调研笔记

调查人: code-developer 子代理（主进程委托）
日期: 2026-09-01
代码索引: codegraph status 正常（450 文件 / 7506 节点）

## 一、现状：哪个插件在跑

- `plugins/registry.go`：vision（旧版）已停用（import 注释掉），**当前启用的是 `vision_v2`**。
- vision_v2 插件提供能力 `vision`（capabilityName = "vision"）。

## 二、vision_v2 插件完整工作链路

### 1. 注册（plugin.go）
- 注入: store/db/logger/route-log，Provide: vision_v2
- 注册 3 个 model-gateway 事件：
  - `ProxyBeforeAttempt` → `HandleProxyBeforeUpstream`（请求改写）
  - `ProxyStreamChunk` → `HandleProxyStreamChunk`（流式工具循环）
  - `ProxyAfterUpstream` → `HandleProxyAfterUpstream`（非流式工具循环）
- 用 ProxyBeforeAttempt 而非 ProxyBeforeUpstream：因为渠道上下文 `__current_channel` 在 Attempt 阶段才写好。

### 2. 能力路由（routes.go）
- `DecideRouteScope(model, virtualModel, scope)` 查 capability_routes 表（SQLite，store JSON 兜底），
  capability = "vision"，命中 RouteProxy 才接管；RouteNative 直接透传。
- 命中后从 `route.ViaOptions` 拿视觉候选模型 + 渠道。

### 3. 请求改写（rewrite.go → HandleProxyBeforeUpstream）
- 三种格式 chat/completions、/v1/messages、/v1/responses。
- `rewriteImagesToPlaceholders`：遍历消息 content，把图片块替换成 `<vision_img_{id}>` 文本占位符，
  图片字节落盘到 `VisionCacheDir/files/{id}.bin`（id = md5 截断 12 位）。
  - chat: image_url；claude: image/source；responses: input_image。
  - 支持 data URI / http(s) URL 两种来源。
- `ensureLookAtImageTool`：注入 `look_at_image` 工具（工具名/image_id/prompt/image_ids）。
- 写 metadata `__vision_v2_active / __vision_v2_format / __vision_v2_route`。

### 4. 工具循环（流式 tool_loop.go / 非流式 after.go）
- 主模型看到 `<vision_img_{id}>` 占位符会调用 `look_at_image` 工具。
- `HandleProxyStreamChunk` 拦截流：检测到工具调用 chunk 置 nil（不转发给客户端），
  流结束时同步执行 `executeToolLoop`。
- `executeToolLoop`（最多 5 轮）：
  1. 解析工具调用 → 对每个 image_id 执行 `describeWithFailover`
  2. 构造 assistant 工具消息 + tool_result（三格式）追加进 messages
  3. 续流（continuationViaGateway，走 model-gateway 子请求通道，复用主渠道）→ 检测新工具调用
- `toolStreamWriter`：识别过程的 SSE delta 实时输出到客户端思考区（前缀「图片理解：」）。

### 5. 视觉识别（describe.go）
- `describeWithFailover`：按 via_options 依次尝试（failover），每个 option 展开候选渠道。
- 读本地图片 → `buildDataURI`（data URI，可选压缩 CompressDataURI）→
  `callVisionViaGateway` 经 model-gateway 子请求通道调视觉模型（chat/completions 格式）。
- 请求 payload：`messages[0].content = [image_url(dataURI), text("识别方向: " + prompt + "\n\n" + 内置prompt)]`
- 缓存：`visionCacheKey(id, prompt)`，VisionCacheEnabled 时命中直接返回。
- 内置 prompt（prompt.go）：6 板块（摘要/文字/布局/语义/视觉/不确定），结构化输出。

### 6. 图片存储（image_store.go）
- SaveImageDataURI / SaveImageURL → saveBytes → files/{id}.bin
- cleanupStaleFiles 懒清理孤儿文件。

## 三、火山方舟视频理解 API（docs/82379/1895586，见 api-doc.md）

- **接口**：Responses API + Chat API 都支持视频输入。
- **三种视频传入方式**：
  1. Files API 上传（推荐）→ `file_id`，512MB(默认存储)/2GB(TOS)，文件 7 天有效
  2. Base64 编码 → `data:video/mp4;base64,...`，<50MB，请求体<64MB
  3. 公网 URL → 直接填 url，<50MB
- **消息格式**：
  - Chat API: `{"type":"video_url","video_url":{"file_id":"..."}}` 或 `{"url":"...","fps":1}`
  - Responses API: `{"type":"input_video","file_id":"..."}` 或 `{"video_url":"...","fps":1}`
- **模型**：doubao-seed-2-1-pro-260628 等视觉大模型
- **fps 精细度**：默认 1，范围 [0.2, 5]；画面剧烈→调高，静态→调低省 token
- **抽帧策略**：单视频 max 80k token；抽帧数 [16, 640](1.8前)/[16,1280](1.8+)
- **上传预处理参数**：preprocess_configs[video]: fps / max_video_tokens / min_frames / max_frame_tokens / min_frame_tokens
- **格式**：MP4/AVI/MOV，不支持 TS，需小写
- **流式输出**：支持 stream=True
- **工作原理**：时间戳+图像拼接，等效于多图请求
- **超时风险**：视频识别耗时长，文档明确建议用流式/File ID 规避客户端超时

## 四、追加视频理解的改造点（头脑风暴结论见 plan）

### 核心思路
视频理解 = 图片识别的自然延伸。vision_v2 已经是"占位符 + 工具调用 + 视觉模型描述"的通用多模态管线，
只需把"图片块"的概念扩展成"视频块"，其余（工具循环/路由/缓存/流式）全部复用。

### 关键差异点（视频 vs 图片）
1. **体积**：视频最大 2GB，不能像图片那样读进内存 base64。3 种策略：
   - 复用现有落盘机制，但 base64 只适合 <50MB 小视频
   - 大视频应走「公网 URL 直传」或「Files API file_id」——需要新增上传逻辑或透传 file_id
2. **识别耗时**：视频抽帧+理解远慢于图片，强烈建议流式/异步，文档也推荐。
3. **消息格式不同**：video_url(chat)/input_video(responses)，与 image_url 不同 type。
4. **识别 prompt**：视频要加时序理解，内置 prompt 需扩展（描述动作/时间点/事件）。
5. **精细度**：视频有 fps 参数，图片没有。

### 改造方案 A（最小侵入，推荐先做）
- 在 rewrite.go 增加 `rewriteVideosToPlaceholders`，识别 video_url/input_video 块 → `<vision_vid_{id}>` 占位符，落盘（或记 URL/file_id）。
- 扩展 look_at_image 工具为 look_at_media（或新增 look_at_video 工具），支持 video_id + fps。
- describe.go 增加 `DescribeVideo` / `buildVideoDataURI`，按视频体积决定 base64 / url 直传。
- 内置 prompt 扩展视频时序模板。
- 路由/工具循环/缓存/流式全部复用。

### 改造方案 B（大视频 file_id 方案）
- 新增 Files API 上传逻辑，把大视频上传拿到 file_id，识别时用 file_id 直传。
- 好处：支持 512MB-2GB，避免重复上传，识别更快。
- 成本：要处理 file 状态轮询（active），新增异步任务机制。

### 需要考虑的问题
- vision_v2 是同步在 ProxyStreamChunk hook 里做工具循环，视频识别耗时长的会阻塞转发热路径，需要评估是否要异步化。
- 缓存 key 策略：视频识别结果缓存（md5(id|prompt|fps)）。
- 前端 VisionLookTool 是否需要支持视频展示。
