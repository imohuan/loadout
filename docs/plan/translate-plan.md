# 翻译功能实施计划（translate-plan）

> 状态：✅ 已执行完成（后端 5 步 + 前端 3 步全部落地）
> 进度：步骤1-8 已完成，详见各步骤下方标记

## 一、任务概述

为 Loadout 管理后台添加"翻译"能力：把英文的 MCP 工具描述、skill 描述等文本，通过大模型 API 批量翻译成目标语言，翻译结果持久化到数据库（SQLite）作为缓存。前端提供"翻译"设置页、可复用 `<TranslateText>` 组件，并通过一个 pinia store 集中管理翻译状态与 SSE 进度。

**核心诉求**：
- 翻译目标语言、目标模型、提示词、显示模式均可配置
- 翻译粒度按"段落切句"，用内容 hash 做缓存 key，改动最小化重翻
- 批量翻译用 SSE 流式推进度，前端显示进度条
- 后端做成独立插件，主动调大模型 API（走 model-gateway 的 ForwardSubRequest）
- 文本来源（skill 描述、MCP 工具描述）后端统一收集，带来源定位
- 为后续"技能 hover 总结"扩展预留 type 字段

## 二、涉及文件清单

### 后端（新增插件 `plugins/translate/`）
| 文件 | 说明 |
|---|---|
| `plugins/translate/plugin.go` | 实现 Plugin 接口：注册服务、挂路由、订阅事件、注册自检 |
| `plugins/translate/service.go` | 翻译服务：切块、hash 缓存、调大模型、SSE 批量 |
| `plugins/translate/store.go` | SQLite `translations` 表的读写（db repository） |
| `plugins/translate/types.go` | 翻译请求/响应/来源等类型定义 |
| `plugins/translate/collect.go` | 文本收集：skill 描述 + MCP 工具描述 |
| `plugins/registry.go` | 登记 translate 插件（改 1 处） |

### 前端
| 文件 | 说明 |
|---|---|
| `frontend/src/stores/translate.ts` | pinia store：配置、内存缓存、SSE 管理、全局进度 |
| `frontend/src/views/TranslateView.vue` | "翻译"设置页（Table 页 + 底部提示词配置） |
| `frontend/src/components/TranslateText.vue` | 可复用翻译组件（自动翻译） |
| `frontend/src/router/index.ts` | 新增 `/translations` 路由（改 1 处） |
| `frontend/src/lib/api.ts` | 新增翻译相关 API 函数（SSE 用 fetch + ReadableStream） |
| `frontend/src/lib/types.ts` | 新增翻译相关类型（改 1 处） |

## 三、实施步骤（每步独立可验证，完成后 commit）

### 步骤 1：创建后端插件骨架
- 新建 `plugins/translate/` 目录，实现 `Plugin` 接口（Manifest: name=translate, Provide=translate）
- 在 `plugins/registry.go` 登记
- **验证**：`go build -o loadout ./apps/server` 通过，启动日志出现 translate 插件自检项

### 步骤 2：translations 表结构 + 存储层
- **表迁移放插件内**（参照 mcp-hub `stats.go:54` 的 `migrate(db)` 幂等建表），**不要**加进 `core/db/migrate.go`（那是核心路由/配置库，最新 v28，业务表不占核心版本号）。新建 `translations` 表：
  ```
  id | hash | source_text | translated_text | source_type | source_id
  | key | target_lang | model | type | created_at | updated_at
  ```
- 实现 store.go：按 hash 查询、写入、更新。用 `db.NewRepository(database)` + `WithTx`，自写 SQL（仿 `repository.go` 的 `ListChannelModels`，Repository 无通用表方法）
- **验证**：编译通过 + 单元测试覆盖存取

### 步骤 3：翻译核心（切块 + hash 缓存 + 调大模型）
- 实现切块逻辑：空行分大段 → 段内按句切小块，每块算 hash
- 翻译走 model-gateway 的 `ForwardSubRequest`（不自建 http.Client），指定 model
  - 真实签名：`ForwardSubRequest(ctx, pipe *ProxyPipeline, streamWriter func(line []byte) error) (*ProxyPipeline, []byte, error)`（`proxy.go:661`），第三个参数是逐行回调
  - 服务名 `"model-gateway"`，`ctx.Get("model-gateway").(*modelgateway.Service)`（仿 vision_v2 `tool_loop.go:464`）
  - **必须设置渠道上下文 Metadata**（`__channel_candidates` / `__current_channel_base_url`），否则无渠道可路由（仿 vision_v2 识别场景）
- 源语言自适应：规则先过滤代码/URL/数字/已是目标语言
- **验证**：单元测试（切块正确性、hash 缓存命中/失效、同一批合并请求）

### 步骤 4：翻译接口 + SSE 批量
- `POST /api/translate` — 单条/多条翻译
- `POST /api/translate/batch` — 批量，SSE 流式推送 `{done, total, item}` 事件
- **验证**：curl 调用单条 + 批量（观察 SSE 逐条输出）

### 步骤 5：文本收集（skill + MCP 描述）
- 调 mcp-hub 的 `BuildIndex(ctx)`（`service.go:104`）一次拿全：它已通过 `collectSkills()` 同时收集技能描述 + MCP 工具描述（`ToolEntry.Description`），无需两条路径。服务名 `"mcp-hub"`
- **skill 描述无需自己解析 frontmatter**：`parseSkillDescription` 在 mcp-hub 里未导出（`service.go:1132`），跨包拿不到；直接读 `ToolEntry.Description` 即可
- translate 插件**自挂** `GET /api/translate/sources` 返回可翻译来源清单（无现成 HTTP 接口复用）
- **验证**：接口返回 skill 和 MCP 工具描述列表

### 步骤 6：前端 pinia store
- `useTranslateStore`：配置（语言/模型/提示词/显示模式）、内存缓存 map、translateBatch(SSE)、全局进度 state、cancel
- **SSE 需新写 POST + ReadableStream 流式读取器**：`api.ts` 现只有 JSON 版 `api<T>`（无流式 fetch），GET EventSource 先例（`stores/processes.ts:56`）不能用于 POST
- **parseSSE.ts 的 `parseStreamBody` 只解析 OpenAI/Claude/Responses 三协议**，翻译自定义 `{done,total,item}` 事件只会落进 rawJsonString 不做字段提取——需在 store 里新增自定义 JSON 事件提取逻辑（或手写轻量 `data:` 行解析）
- **验证**：store 单元/手动验证（发起批量、进度更新、取消）

### 步骤 7：前端翻译设置页
- 新增路由 `/translations`（Table 页面）
- 配置区：目标语言、目标模型、翻译提示词（底部自定义）、显示模式
- **模型下拉合并两个接口**：`useChannels().list()`（`/api/channels` 渠道模型）+ `useAggregates().list()`（`/api/aggregates` 虚拟聚合模型），合并后再选（仿 ModelTestView 下拉，`ModelTestView.vue:229`）
- 来源清单区：调 `/api/translate/sources` 列出所有 skill + MCP 工具，勾选批量翻译，显示 SSE 进度条
- **验证**：页面渲染、配置保存、批量翻译进度

### 步骤 8：前端 `<TranslateText>` 组件
- 可复用组件：props 传 source text + source_type/source_id，自动判断有无译文 → 调 store 翻译
- 单条/批量；批量时读 store 全局进度显示进度条
- 按显示模式渲染原文/译文/双显
- **验证**：在某个页面接入组件，观察自动翻译与显示切换

## 四、测试方案

- 后端：`go test ./plugins/translate/...` 覆盖切块、hash 缓存、存储、接口
- 前端：`pnpm build`（vue-tsc 类型检查 + 构建）通过
- 手动：curl 翻译接口；页面批量翻译进度条；组件自动翻译
- 全量：`go test ./...` + 前端构建

## 五、潜在风险与注意事项

1. **hash 缓存 key**：不能只用原文当 key，必须用内容 hash，否则改一个空格触发整段重翻。
2. **禁止自建 http.Client**：翻译必须走 model-gateway 的 ForwardSubRequest，否则丢失日志/额度/failover。
3. **ForwardSubRequest 需设置渠道上下文**：自动路由到目标模型必须设 `__channel_candidates`/`__current_channel_base_url` 等 Metadata，否则无渠道可路由（仿 vision_v2 识别场景）。
4. **SSE 用 POST + 新写流式读取器**：不能用 GET EventSource，`api.ts` 无流式 fetch，需自建 fetch+ReadableStream；翻译自定义事件要在 store 里新增提取逻辑（parseStreamBody 只认 OpenAI/Claude/Responses 三协议）。
5. **模型下拉合并两接口**：渠道模型（`/api/channels`）+ 虚拟聚合模型（`/api/aggregates`）分开请求合并。
6. **MCP 描述获取**：调 mcp-hub `BuildIndex` 一次拿全（含 skill），依赖 mcp server enabled；前端工具描述接口需 translate 自挂（无现成 HTTP 接口）。
7. **skill 描述复用注意**：`parseSkillDescription` 未导出，跨包拿不到，直接读 `ToolEntry.Description` 即可，无需自己解析 frontmatter。
8. **表迁移放插件内**：translations 是独立业务表，参照 mcp-hub 插件内 `migrate(db)` 幂等自建，不进核心 `core/db/migrate.go`。
9. **插件装配顺序**：translate 依赖 model-gateway 和 mcp-hub，Manifest.Inject 需声明 `"model-gateway"` `"mcp-hub"`，框架自动拓扑排序。
10. **构建命令**：`go build -o loadout ./apps/server`（入口目录是 `apps/server`，不是 `./apps/server`）。

## 六、后续扩展（预留位）

- `translations.type` 字段区分 `translate`（翻译）与 `summary`（总结），为"技能 hover 显示 AI 总结"预留
- 同一插件、同一表、同一 pinia store，各自配提示词即可扩展
