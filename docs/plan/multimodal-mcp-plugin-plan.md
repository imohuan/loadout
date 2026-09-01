# 多模态 MCP 插件计划（multimodal-mcp）

任务概述：新增一个**内置的独立 MCP 端点** `multimodal`（走标准 MCP 协议，与外部 MCP 一致，只是内置在我们服务里），在设置界面新建一个 tab 做配置页。端点导出 **3 个工具**：图片理解、视频理解、音频理解。每种资源的灵活参数**直接写进对应工具的 input schema**，由调用方（模型）按需传。

> 本计划由用户提出的「多模态 MCP 插件方案」整理而来，已对照真实代码与三份火山方舟文档修正（`docs/archive/火山 图片理解.md` / `火山 视觉理解.md` / `火山 音频理解.md`）。

---

## 一、现状核查（先对齐真实代码与方舟 API）

### 1.1 插件与 MCP 端点机制（已确认）
- 插件机制：`Manifest.Inject/Provide`、`Context.Get/Set/RegisterRoute` 真实存在。
- MCP 端点：`mcpkit.NewServer(name, tools)` 构建标准 MCP server，每个 `ServerTool` 含 `Name/Description/InputSchema/Handler`。**本插件自建一个 mcp server 挂到路由，不依赖 mcp-hub。**

### 1.2 用户方案的两处接口假设已修正
- **`mcp-hub.RegisterSystemTool(...)` 不存在**。mcp-hub 是 MCP 聚合网关，没有"系统工具注册中心"。→ 本插件**不依赖 mcp-hub**，自建端点。
- **"视觉候选组件"实为"能力路由"**。→ 本插件**不沿用**，按决策新建独立配置页。

### 1.3 关键结论：三种资源是三种不同的能力范式（三份文档）
| 资源 | 关键参数 | 能力子场景 | 请求格式 |
|---|---|---|---|
| 图片 | `detail`(low/high/xhigh)、`image_pixel_limit`、像素区间 | 精细度控制、图文混排、视觉定位、GUI | chat/completions 或 responses |
| 视频 | `fps`(0.2~5)、Files API、时序 | 抽帧、时序感知、流式 | chat/completions（video_url 块）|
| 音频 | `task`(asr/timed/diarize/translate/caption)、语种、时间戳 | ASR、时间戳、多说话人、日志、翻译、Caption | responses（input_audio 块）|

**不能把三种资源塞进一个对话。** 参数维度（detail vs fps vs task/模板）和能力子场景（尤其音频是语义级不同任务）完全不同，没有统一配置面。→ **每个资源一个工具**，各自参数写在对应工具 schema 里。

### 1.4 资源传入方式（三份文档结论，见 1.5 对比表）
三种资源都支持 **Files API 上传（推荐）+ base64 + URL**，大小限制不同。**Files API 上传只需 API key**（`POST /v3/files` + `Authorization: Bearer $key` + `purpose=user_data` + `file=@路径`），**不需要额外 AK/SK、不需要配 TOS**——不传 TOS 参数时文件存方舟默认托管存储（上限 512MB）；只有超大视频（>512MB，最多 2GB）才需要 TOS Bucket，首版不做。

### 1.5 资源大小限制对比表
| 资源 | Files API 上传 | Base64 | URL |
|---|---|---|---|
| 图片 | `file://` 路径自动传，≤ 512MB | < 10MB，请求体<64MB | < 10MB |
| 视频 | 上传拿 `file_id`，≤ 512MB（TOS ≤ 2GB）| < 50MB，请求体<64MB | < 50MB |
| 音频 | 上传拿 `file_id`，≤ 512MB | < 25MB，时长≤120min | < 25MB，时长≤120min |

**上传后流程**：拿 `file_id` → 轮询文件状态变 `active` → 用 `file_id` 作资源引用。文件默认存 7 天（可配 1~30 天），可多次复用。
**决策**：资源输入支持 `url` / `base64` / `file_path` 三种。**小文件 → base64 内联（快、免上传）；大文件 → Files API 上传拿 `file_id`（需上传+轮询 active，走渠道 API key）；url → 公网直传。** 由文件大小自动决定走哪条路。

## 二、设计思路

**一个内置 MCP 端点 + 3 个工具。** 每个工具 = 一种资源，工具的 input schema 定义该资源的所有可调参数。识别调用走 model-gateway 子请求通道。

### 2.1 工具定义（3 个，参数写在 schema 里）

| 工具 | 资源 | input schema 参数 | 说明 |
|---|---|---|---|
| `understand_image` | 图片 | `image`(url/base64/本地路径)、`detail`(low/high/xhigh)、`prompt` | 通用图片理解 |
| `understand_video` | 视频 | `video`(url/base64/本地路径)、`fps`(0.2~5，默认1)、`prompt` | 通用视频理解（抽帧+时序）|
| `understand_audio` | 音频 | `audio`(url/base64/本地路径)、`task`(asr/timed/diarize/translate/caption)、`language`、`source_lang`、`target_lang`、`prompt` | 音频理解，task 决定识别模式 |

**资源输入三态**：每个工具的 `image/video/audio` 参数接受 `url`、`data:{mime};base64,...` 或 `file://本地路径`。后端按**文件大小自动选择**：小文件 base64 内联，大文件走 Files API 上传拿 `file_id`（上传 + 轮询 active）。上传与识别共用渠道 API key。

**音频的 `task` 是核心参数**：模型选 `asr`(普通转写) / `timed`(带时间戳) / `diarize`(多说话人+日志) / `translate`(翻译) / `caption`(分析)。后端按 task 从内置模板库里选对应 instructions 拼进请求。**灵活体现在一个工具的参数上，不拆多个工具。**

### 2.2 端点与路由
- 端点路径：`POST /mcp/multimodal`（AuthMCPHeader），MCP 协议（`mcpkit.NewServer`），支持流式透传。
- 配置页路由：`GET/PUT /api/multimodal/config`（AuthSession）。

### 2.3 模型与密钥（MCP 工具零配置 + 网关自动 failover）
**核心决策：MCP 工具不暴露任何 API Key / 模型参数。** 客户端连这个 MCP 端点时，只需要一个 SSE URL（`POST /mcp/multimodal` + MCP key），不传 key、不传模型。

**架构：识别请求直接打 Loadout 本地网关（3000），渠道切换由网关负责；API key 仅用于大文件上传。**

- **识别请求 → Loadout 本地网关**：多模态插件把请求打到 Loadout 自己的 `/v1/chat/completions`（`ServerAddr=:3000`，model-gateway 透明代理主链路 `HandleProxy`），传内置模型名 + 资源块（image_url / video_url / input_audio / file_id）。**渠道选择与 failover 全由网关按模型名自动做**——插件不参与渠道匹配、不做 failover。
- **忽略 `resolveTestTarget`**：多模态识别**不**复用测试页那套 `resolveTestTarget`（sk_key_hash 解析 / channel_id 解密渠道 key）。识别请求的目标就是 Loadout 自己，不解析任何渠道 key。
- **调用方式：内部走 `model-gateway.Service.ForwardSubRequest`（已定）**：多模态插件作为 Loadout 内部组件，复用 vision_v2 实际在用的子请求通道 `ForwardSubRequest(ctx, pipe, streamWriter)` 走主链路。它自带 `__sub_request` / `__sub_request_skip_security` 标记（安检 hook 早退、防递归）、封装 recorder / 非流式 4xx 转 error / 流式 streamWriter / 子请求日志，无需任何 sk-key，不环回端口。网关按内置模型名自动 failover。**不裸调 `HandleProxy`**（审核结论：裸调需自行补子请求标记与流式处理，属重复造轮子）。
- **API key 的用途 = 仅大文件上传**：只有需要 Files API 上传（视频/音频 > base64 阈值）时，才从渠道列表按 **Base URL 识别方舟平台**（`ark.cn-beijing.volces.com`，归一化比较）取 key，直接调火山 `POST /v3/files` 上传拿 `file_id`；识别时把 `file_id` 作为资源块传给网关。小文件走 base64 内联，**无需 key**。
- **模型名内置配置**：方舟请求体必须传真实 model id（如 `doubao-seed-2-1-pro-260628`），由多模态设置页为每个工具配一个内置模型名。它只决定"请求体里的 model 字段"，渠道切换由网关按该模型名 failover。

### 2.4 前端现有配置体系（多模态复用的数据源）
多模态插件不自己存 key，**只读取项目已有的渠道配置（用于上传取 key）**：

| 前端配置页 | 后端数据 | 多模态如何用 |
|---|---|---|
| 设置页「模型密钥」tab | `api_keys.json`（gateway-keys，sk- 密钥）| 管理后台登录认证；多模态走内部 `HandleProxy`，无需 sk-key |
| 渠道页（ChannelsView）| Channels（base_url + AES 加密 API key + models 列表）| **大文件上传取 key**：按 base_url 识别方舟平台自动取 |
| 能力路由（CapabilityRoutesView）| `CapabilityRoute`（capability×model×channel 矩阵）| 不用（网关自动 failover，插件不选渠道）|
| 聚合模型（AggregatesView）| `AggregateModel`（虚拟名→targets 顺序 failover）| 不用（网关主链路已处理；内置模型名可直接指向聚合模型名）|

### 2.5 配置 UI（设置界面新 tab）
- 在 `ManagementView.vue` 的 `<Tabs>` 加一个 tab（如「多模态」）。
- 配置内容：端点开关、3 个工具是否启用、每个工具的**内置模型名**、每个工具的**默认参数**（图片默认 detail、视频默认 fps、音频默认 task/language 等）。
- **不含 API Key / Base URL 配置**——这些在渠道页管，多模态插件只选内置模型名。
- 默认参数只做兜底；调用方显式传的参数优先。
- 新增 `frontend/src/views/MultimodalView.vue`，复用现有 Card/Table/表单范式。

## 三、改动文件清单（草案）

| 文件 | 改动 |
|---|---|
| `plugins/multimodal-mcp/plugin.go` | 插件骨架：Manifest + Apply 装配 |
| `plugins/multimodal-mcp/plugin.yaml` | 清单（name/version/inject/provide）|
| `plugins/multimodal-mcp/server.go` | 构建 mcp server + 端点路由注册（POST /mcp/multimodal）|
| `plugins/multimodal-mcp/tools.go` | 3 个工具 schema + Handler 分发 |
| `plugins/multimodal-mcp/image.go` | understand_image（detail 参数 + 资源三态处理）|
| `plugins/multimodal-mcp/video.go` | understand_video（fps 参数 + 资源三态处理）|
| `plugins/multimodal-mcp/audio.go` | understand_audio（task → 模板选择 + 各任务参数）|
| `plugins/multimodal-mcp/upload.go` | 按 Base URL 识别方舟渠道取 key → Files API 上传 + 状态轮询 active（返回 file_id）|
| `plugins/multimodal-mcp/call.go` | 识别请求内部直接调 model-gateway `HandleProxy` 走主链路（内置模型名 + 资源块）|
| `plugins/multimodal-mcp/prompt.go` | 各资源/任务的 instructions 模板库（取自三份文档）|
| `plugins/multimodal-mcp/config.go` | 配置读写（store JSON）+ GET/PUT /api/multimodal/config |
| `plugins/multimodal-mcp/*_test.go` | 单测 |
| `frontend/src/views/MultimodalView.vue` | 配置页（新 tab）|
| `frontend/src/views/ManagementView.vue` | 增加「多模态」TabsTrigger + TabsContent |

> 不依赖 mcp-hub、不新增密钥体系、不碰 vision_v2 现有逻辑。

## 四、实施步骤（每步独立可验证 + commit）

1. **工具与接口定义**：3 个工具 schema + mcp server 骨架 + 端点路由。→ 单测 + commit。
2. **配置模型**：config.go 读写作废/启用/默认参数/内置模型名；注册配置路由。→ 单测 + commit。
3. **上传取 key**：upload.go 按 Base URL 识别方舟渠道取 key → Files API 上传（multipart `purpose=user_data`）+ 状态轮询 active；大小阈值（图片10MB/视频50MB/音频25MB）自动分流 base64 或上传。→ 单测 + commit。
4. **识别调用网关**：call.go 构造请求内部直接调 `HandleProxy`（内置模型名 + 资源块），网关自动 failover。→ 单测 + commit。
5. **图片识别**：understand_image（detail + 资源三态）。→ 单测 + commit。
6. **视频识别**：understand_video（fps + 资源三态）。→ 单测 + commit。
7. **音频识别**：understand_audio（task 分发 + 各任务参数）。→ 单测 + commit。
8. **前端配置页**：ManagementView 加 tab + MultimodalView。→ 构建验证。
9. **全量测试**：`go build ./... && go test ./plugins/multimodal-mcp/...`。

## 五、测试方案

- 单测：3 个工具 schema、mcp server 构建、各资源 payload 构造（image detail / video fps / audio task 模板选择）、参数默认值兜底、配置读写、模型路由。
- 回归：现有 vision_v2 / mcp-hub / 前端构建不破坏。
- 构建：`go build ./...` + 前端构建。

## 六、风险与注意

- **接口假设已排除**：不依赖 mcp-hub 的 RegisterSystemTool（不存在）；不新增并行密钥体系（复用渠道）。
- **Files API 上传**：只需渠道 API key，无需 TOS/额外 AK-SK。上传后需轮询 `active` 状态才能用 `file_id`；文件默认存 7 天。需处理上传失败/超时/文件已删除（file_id 失效）场景。视频可选带 `preprocess_configs[video][fps]` 预抽帧。
- **音频 task 模板敏感**：ASR/时间戳/多说话人/翻译/Caption 靠 instructions 区分，内置模板需严格照方舟文档写，测试重点验证各 task 输出结构。
- **大文件**：图片/视频/音频 > 各 base64 阈值时走 Files API；超大视频 >512MB 需 TOS，首版不做（URL/默认存储 512MB 已覆盖绝大多数场景）。
- **模型差异**：图片/视频/音频可能需不同模型，配置页按工具绑定；不假设单一模型全能。
- 全部改动收敛在 `plugins/multimodal-mcp/` 与前端多模态 tab。

## 七、待确认决策点

1. **上传取 key 的方式**：从渠道列表按 Base URL 识别方舟平台取 key 上传。确认方舟 Base URL 匹配规则（如 `ark.cn-beijing.volces.com` 域名即可，还是要精确匹配完整 path）。
2. **工具命名**：`understand_image` / `understand_video` / `understand_audio` 是否合适？
3. **音频 task 首版范围**：5 个 task（asr/timed/diarize/translate/caption）全做，还是先做 3 个（asr/timed/translate）？
4. **Files API 大文件**：首版先 URL/base64，后续再上 Files API，是否同意？

> 调用方式已定：识别内部直接调 `HandleProxy`（方案 B）。

确认后进入实施。
