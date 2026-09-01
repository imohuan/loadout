# 视频理解扩展计划（vision_v2 → 支持视频块）

任务概述：在 `vision_v2` 插件既有「占位符 + 工具调用式多模态识别」管线基础上，把能力从「图片」扩展到「视频」。视频走火山方舟多模态 API（chat/completions 格式，`video_url` content 块），复用路由 / 工具循环 / 缓存 / 流式链路，改动最小化。

---

## 一、现状（调研结论，见 docs/tmp/video-understanding/research.md）

- `vision_v2` 是当前启用的视觉插件（旧 `vision` 已停用）。
- 工作链路：`rewrite.go` 把图片块替换成 `<vision_img_{id}>` 占位符并落盘 → 注入 `look_at_image` 工具 → 模型调用工具 → `tool_loop.go` 收齐后 `describeWithFailover` 调视觉模型 → 续流 → 客户端看到识别文本。
- 消息格式三套：chat / claude / responses。
- 识别走 model-gateway 子请求通道（`callVisionViaGateway`），已有 failover、缓存、流式、request-log/route-log 联动。
- 火山方舟视频 API（已查证）：chat/completions 里视频块用 `{"type":"video_url","video_url":{"url":...} }`，支持三种传法：公网 URL(<50MB) / Base64(`data:video/mp4;base64,...`，<50MB) / Files API file_id(512MB~2GB，推荐大文件)。抽帧 `fps` 参数默认 1，范围 [0.2, 5]。

## 二、设计思路

**视频 = 图片的自然延伸。** 不做新插件，在 vision_v2 内扩展：

1. **识别视频块**：`rewrite.go` 遍历消息时，遇到 `video_url`(chat) / `input_video`(responses) 块 → 替换为 `<vision_vid_{id}>` 占位符。claude 格式暂无标准视频块，先不处理（记录，不扩展）。
2. **占位符落盘**：复用 `image_store.go` 的落盘机制（同 id 哈希 + `files/{id}.bin`），但视频体积大：
   - <50MB → 直接 base64 落盘（复用 SaveImageDataURI 思路，新加视频版）。
   - 公网 URL → 下载落盘（复用 SaveImageURL，注意 50MB 上限）。
   - **大视频（>50MB）不落盘、不下发**：占位符里只记 `url` 或直接透传，识别时用 URL 直传（火山支持 URL）。本版本先支持小视频 + URL，**Files API 上传（file_id）留作后续**（涉及文件状态轮询、异步任务，复杂度高，单列）。
3. **工具扩展**：新增 `look_at_video` 工具（复用 look_at_image 结构，参数加 `fps`），或把 look_at_image 泛化为 look_at_media。**决策：新增独立 `look_at_video` 工具**，避免破坏现有图片工具的解析/测试，识别路径走独立函数。占位符过滤器需支持剔除 `<vision_vid_...>`。
4. **识别**：`describe.go` 新增 `describeVideoWithFailover`（镜像 `describeWithFailover`），构造 chat 请求 content = `video_url(dataURI 或 url)` + 文本；缓存 key 用 `video:{id}:{fps}:{prompt}`。
5. **工具循环**：`tool_loop.go` 的 executeToolLoop 已按工具名分发，加一个 `look_at_video` 分支，识别走视频函数。`stream.go` 的 accumulator 和 `tools.go` 的 schema 同步扩展。
6. **内置 prompt**：`prompt.go` 增加视频时序理解模板（描述动作 / 时间点 / 事件 / 危险判定，对应火山官方示例的输出 JSON 结构）。
7. **route-log**：`visionAttempt` 复用，capability 标 `vision`，extra 加 `media_type=video`。

## 三、改动文件清单

| 文件 | 改动 |
|---|---|
| `plugins/vision_v2/rewrite.go` | `rewriteVideosToPlaceholders`：识别 video_url/input_video 块→占位符；按体积决定 base64/url |
| `plugins/vision_v2/tools.go` | 新增 `look_at_video` 工具 schema + `ensureLookAtVideoTool` |
| `plugins/vision_v2/stream.go` | accumulator 识别 `look_at_video`（video_id/video_fps/prompt 解析）；PlaceholderFilter 支持剔除 `vision_vid_`；ToolCall 增加 VideoID/FPS 字段 |
| `plugins/vision_v2/describe.go` | `describeVideoWithFailover` + `buildVideoDataURI` + `callVisionVideoViaGateway`（video_url content）|
| `plugins/vision_v2/tool_loop.go` | executeToolLoop 增加 look_at_video 分支 |
| `plugins/vision_v2/prompt.go` | 视频时序理解内置 prompt |
| `plugins/vision_v2/image_store.go` | 复用 saveBytes；新增视频体积判断 + URL 落盘（可复用现有）|
| `plugins/vision_v2/service.go` | （如需要）VideoCacheDir / 状态字段 |
| 测试文件 | rewrite / tools / stream / describe / tool_loop 的 *_test.go 补视频用例 |

## 四、实施步骤（每步独立可验证 + commit）

1. **数据模型**：`ToolCall` 加 `VideoID`/`FPS`；`stream.go` 解析 `look_at_video` 参数；占位符常量加 `videoPlaceholderPrefix`。→ 单测 + commit。
2. **重写层**：`rewriteVideosToPlaceholders` 识别视频块（chat/responses），小视频 base64 落盘、URL 下载、大视频透传占位符带 url。注入 `look_at_video` 工具。→ 单测 + commit。
3. **识别层**：`describeVideoWithFailover` + video payload 构造（video_url content，含 fps）；缓存 key 含 fps。→ 单测 + commit。
4. **工具循环**：`executeToolLoop` 分发 look_at_video → 视频识别。→ 单测 + commit。
5. **prompt + route-log**：视频内置 prompt；visionAttempt extra 标 media_type。→ commit。
6. **全量测试**：`go build ./... && go test ./plugins/vision_v2/...`，跑全部用例。

## 五、测试方案

- 单元测试：重写识别（chat/responses 视频块→占位符）、工具注入、accumulator 解析 video 调用、视频 payload 构造（video_url + fps）、缓存 key、工具循环分发。
- 回归：现有图片用例全部保持通过（改动不得破坏）。
- 构建：`go build ./...` 通过。

## 六、风险与注意

- **大视频 Files API（file_id）本轮不做**：涉及文件上传 + 状态轮询 + 异步任务，复杂度高，单列为后续迭代。本轮支持 <50MB 和 URL。
- claude 格式视频块标准未定，先跳过（records，不扩展）。
- 视频识别耗时长，同步在 ProxyStreamChunk hook 里会阻塞转发热路径——本版沿用图片的同步方式（与现状一致），异步化单列。
- 占位符过滤器必须新增 `vision_vid_` 剔除，否则客户端看到占位符原文。
- 改动全部收敛在 vision_v2 包内，不碰其他插件/前端。

## 七、验证基线

- 开始前：`go build ./plugins/vision_v2/` 通过（已确认）。
- 每步提交后：对应包测试绿。
- 完成后：全量 `go build ./...` + `go test ./plugins/vision_v2/...` 绿。
