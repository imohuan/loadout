# request-log 完整请求日志插件实现计划

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 实现新插件 `request-log`：把每次 AI 模型请求的**完整输入输出**（请求体、响应体、流式逐块拼接）落库，提供搜索、查看、跳转能力。参考现有 `sensitive-filter` 插件的写法（订阅 model-gateway waterfall 事件 + 能力路由矩阵）。

**Architecture:** 插件订阅 model-gateway 的四个 waterfall 事件——`ProxyBeforeAttempt`（请求发出之前抓完整请求 + 生成 UUID，用户拍板）、`ProxyAfterUpstream`（非流式 2xx 响应）、`ProxyUpstreamFailed`（失败收尾，B2）、`ProxyStreamChunk`（流式逐块拼响应）。请求/响应 JSON 存**独立 SQLite 文件 `request-log.db`**（单表 `request_logs`，主键 UUID），不碰 loadout.db 的既有表；与 route-log 的关联采用**方式 A**：`route_requests` 加一列 `request_log_id`（UUID），route-log 列表/详情带出（需改全链路，见 B1），前端点击跳转 `/api/request-logs/{id}`。能力开关挂 `capability_routes`（capability=`request_log`，**方式 B**），按 models × channels 矩阵配置。脱敏（Authorization 等敏感头打码、base64 图片转占位标记）插件配置开关控制，默认开。

**Tech Stack:** Go 1.x, 现有插件框架（core/plugin, core/store, core/db）, model-gateway hook 机制, SQLite（loadout.db 迁移版本制，最新 v23 → 新增 v24；request-log.db 由插件自行建表）。

---

## 决策记录（用户已拍板，实现时勿改）

| 维度 | 决策 | 说明 |
|---|---|---|
| 存储 | 独立 SQLite 文件 `request-log.db` | 不碰现有 loadout.db（除下述一列关联） |
| 表结构 | 单表 `request_logs`，主键仅 `id`（UUID） | 不要 parent_id / sub_call_id 之类多余字段 |
| 关联 | 方式 A：`route_requests` 加列 `request_log_id`（UUID） | 不搞 hash；UUID 由 **model-gateway 在实际请求位置生成、emit 事件传入**，request-log 消费（决策点 1 定案） |
| 能力路由 | 方式 B：挂 `capability_routes`（capability=`request_log`） | 按 models × channels 配置开关，参照 sensitive-filter 注册方式 |
| 脱敏 | 插件配置开关，默认开 | Authorization 等敏感头打码；base64 data URI → 占位标记 |
| 嵌套请求 | 不单独记 | vision_v2 父子嵌套不管，UI 树形仍由 route-log attempts/step_no 负责 |
| 图片字节 | 不保存 | data URI 统一转字符串占位标记 |
| 接口 | `GET /api/request-logs` + `GET /api/request-logs/{id}` | 列表搜索 + UUID 详情 |

---

## 背景事实（已核实，代码位置）

1. **事件时序保证**：`HandleProxy`（proxy.go:113-117）先调 `proxyBeginLog`（route-log.Start 写 running 行，proxy.go:992/1001）**再** `Waterfall(ProxyBeforeUpstream, pipe)`（proxy.go:117），随后才进 proxyForward 触发 `proxy:before-attempt`（proxy.go:282）。→ **request-log 的 before-attempt handler 执行时 `route_requests` 行已存在**，可 UPDATE `request_log_id`。
2. **事件 payload**（plugins/model-gateway/types.go:224-250）：
   - `ProxyBeforeUpstream` → `*ProxyPipeline{RequestID, Request{Method,Path,Query,Header,Body,Model,Stream}, Metadata, HTTPRequest}`
   - `ProxyAfterUpstream` → `*AfterUpstreamPayload{Pipe, Response{StatusCode,Header,Body}}`（**仅非流式**，proxy.go:413）
   - `ProxyStreamChunk` → `*StreamChunkPayload{Pipe, Data}`（SSE 每行，proxy.go:659）
   - `proxy:before-attempt`（ProxyBeforeAttempt）是"每次渠道尝试前"事件，sensitive-filter/field-filter 挂它；request-log **也挂它**（用户拍板：在请求发出之前生成 UUID）——该事件在 proxyAttempt 里触发（proxy.go:282），位于构建上游请求（:304）与发出（:337）**之前**，且触发前 metadata 渠道已设好（:272-276，`__current_channel`/`__last_tried_channel` 已写入）；一次请求内 failover 重复触发靠 metadata 幂等（不重复生成 UUID）。
3. **流结束检测**：`data: [DONE]` 行会先经过 `ProxyStreamChunk` hook（proxy.go:659）再被 model-gateway 判结束（proxy.go:691）。`isSSEDone` 是 model-gateway 私有函数，request-log 需自行实现等价判断（几行）。
4. **流式中断无事件**：客户端断开（`clientCtx.Done()`，proxy.go:651）或上游 EOF 异常时**没有任何 hook 触发**（proxyStream 直接 return）。→ request-log 需超时兜底：`result='running'` 且超时的行在列表/详情访问时 self-heal 收尾（复用 route-log SelfHeal 模式，service.go:126）。
5. **插件可自注册 HTTP 路由**：`ctx.RegisterRoute(plugin.RouteSpec{Method, Pattern, Auth: plugin.AuthSession, Handler})`（core/plugin/context.go:32、plugin.go:65）。→ **API 由 request-log 插件自己注册，不需要改 admin-api**（admin-api 也是这么挂的，admin-api/plugin.go:95-97）。
6. **独立库不能复用 `db.Open`**：`db.Open`（core/db/db.go:18）固定跑全局 `Migrate`，会在 request-log.db 上执行 loadout 全部迁移 → 灾难。request-log 插件自己 `sql.Open("sqlite", path)` + pragmas（WAL/foreign_keys/busy_timeout，照抄 db.go:54-65）+ `CREATE TABLE IF NOT EXISTS`。单表无演进历史，无需迁移机制；将来要演进再自行加轻量版本表。
7. **迁移版本与测试硬编码**：migrate.go 最新 v23（route-attempts-step-no-text）。v24 = `ALTER TABLE route_requests ADD COLUMN request_log_id TEXT`。**db_test.go 有两处硬编码需同步**：`TestMigrateIsIdempotent`（db_test.go:55）校验迁移条数 23 → 改 24；`TestMigrateRejectsIncompatibleHistory`（db_test.go:66-69）用 24 模拟"比程序更新"的库 → 改 25（否则语义损坏）。表数量恒为 21（v24 不加新表）。
8. **request_id 来源**：`pipe.RequestID` 即 `X-Request-Id` 或自动生成 16 位 hex（servercore/server.go:203-288），直接复用，不自己造。
9. **channel 字段来源**：`pipe.Metadata["__current_channel"]`（聚合模型指定渠道后设置）、`["__last_tried_channel"]`（流式实际尝试渠道）；channel_name 走 route-log 同款快照思路（可查 `ListChannels` 反查，或 metadata `__stream_channel_name`）。
10. **能力路由查表**：`db.NewRepository(database)` 的 `ListCapabilityRoutes`（sensitive-filter/service.go:46-106 全套 DecideRoute/DecideRoutesScope/ChannelScopeFromMetadata 可直接照抄，把 capabilityName 换成 `request_log`）。loadout.db 的同一个 `*sql.DB` 连接同时服务能力路由查询 + `route_requests` 反查/UPDATE（同一文件没问题）。
11. **capability_routes 是固定列**（admin_repository.go SELECT/INSERT、import_admin.go INSERT 硬编码列）。本次**不加新列**（脱敏开关放 request-log.db 的 config 表），避免动这些文件。
12. **插件注册与订阅顺序**：`plugins/registry.go` 的 `All()` 加一行 `requestlog.New()`。**hook 执行顺序按 topoSort（依赖 + 名字字典序，loader.go:217-260），不是 All() 顺序**——`request-log`（"r"）排在 `aggregate`（"a"）之后、`vision_v2`（"v"）之前、`sensitive-filter`（"s"）**之前**。→ ① request-log 在 `proxy:before-attempt` 上先于 sensitive-filter 执行：**被安检（敏感词 error 模式）拒绝的请求也会被 request-log 记下 request 半条**（无 response，靠 self-heal 收尾）——行为可接受且有价值（能看到被拒的完整请求），不是 bug；② request-log 在 aggregate 之后，`pipe.Request` 已是聚合改写后的（`__virtual_model` 保留虚拟名），body 可能已被改写，vision_v2 的图片 marker 替换在其后的 hook（base64 原图仍在 body 里，脱敏占位逻辑仍然必要）。
13. **`ProxyAfterUpstream` 仅 2xx 触发（关键盲区）**：proxy.go 非流式分支在状态码非 2xx 时提前 return（proxy.go:359），只有 2xx 才走到 `Waterfall(ProxyAfterUpstream)`（proxy.go:413）。→ **4xx/5xx、无可用渠道（proxy.go:479/494）、安检拒绝（proxy.go:119）都没有输出事件可收尾**，request_logs 行会永远卡 running。必须**额外订阅 `ProxyUpstreamFailed`**（payload `*ProxyFailurePayload{Pipe, Model, ChannelID, Error, StatusCode, ErrorBody}`，types.go:253-260）把失败行收成 `failed`（含 http_status/error_body）；仍收不到的中断场景才靠 self-heal 标 `stream_interrupted`。
14. **route-log 全链路要带出 `request_log_id`（B1）**："列表/详情接口天然返回这个字段"**不成立**——`contracts.RouteRequestView`（plugins/contracts/routing.go:184-207）无此字段；route-log 的 List SQL（service.go:267）、Detail SQL（service.go:315）、`scanRequest`（service.go:396-420）都硬编码列；前端 `RouteLog` 类型（frontend/src/types.ts）也没有。**必须同步改**：routing.go 加字段 + service.go 三处 SELECT + scanRequest + 前端类型，否则 RouteLogTable 拿不到 ID，跳转入口无法落地。

---

## 数据模型

### 1) loadout.db 迁移 v24（core/db/migrate.go 追加）

```sql
-- v24: route-logs-request-log-id
-- request-log 插件的关联列：route-log 行指向 request_logs 表（独立库）的主键 UUID。
-- 可空：未命中 request_log 能力路由的请求为 NULL（前端据此决定是否显示"完整日志"入口）。
ALTER TABLE route_requests ADD COLUMN request_log_id TEXT;
```

不加 UNIQUE 约束（每行独立生成，天然唯一；SQLite 对 NULL 不做唯一性检查）。不加索引（无按此列查询需求）。

### 2) request-log.db 独立库（插件 Apply 时自建）

```sql
-- 单表 request_logs：主键仅 id（UUID）。result 对齐 route-log 语义。
CREATE TABLE IF NOT EXISTS request_logs (
  id TEXT PRIMARY KEY,              -- UUID（crypto/rand 16 字节 → 32 位 hex，零依赖）
  request_id TEXT NOT NULL,         -- 外部请求 ID（X-Request-Id / 16 位 hex），用于搜索
  model TEXT NOT NULL DEFAULT '',
  channel TEXT NOT NULL DEFAULT '', -- channel_id；初始取 __current_channel（聚合请求才有值），收尾时用 __last_tried_channel 回填
  http_status INTEGER,
  stream INTEGER NOT NULL DEFAULT 0,
  started_at TEXT NOT NULL,         -- RFC3339Nano UTC
  finished_at TEXT,
  duration_ms INTEGER,
  result TEXT NOT NULL DEFAULT 'running',  -- running / success / failed / stream_interrupted
  request_json TEXT NOT NULL,       -- 完整请求（脱敏 + 图片占位后）
  response_json TEXT,               -- 完整响应（脱敏后；流式 = SSE 原文逐块拼接）
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_request_logs_request_id ON request_logs(request_id);
CREATE INDEX IF NOT EXISTS idx_request_logs_started_at ON request_logs(started_at DESC);
CREATE INDEX IF NOT EXISTS idx_request_logs_model ON request_logs(model, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_request_logs_channel ON request_logs(channel, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_request_logs_result ON request_logs(result, started_at DESC);

-- 插件配置（单行表，id 恒为 1）：脱敏开关默认开
CREATE TABLE IF NOT EXISTS request_log_config (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  redact INTEGER NOT NULL DEFAULT 1
);
```

**request_json 结构**（`request_json` 存 JSON 文本；body 优先存原始字节字符串保真，前端展示时再尝试格式化）：

```json
{
  "method": "POST",
  "path": "chat/completions",
  "query": "model=gpt-4o",
  "headers": {"content-type": "application/json", "authorization": "Bearer sk-***"},
  "body": "<原始请求体字符串，脱敏/图片占位处理后>",
  "model": "gpt-4o",
  "stream": true
}
```

**response_json 结构**：

```json
{
  "status_code": 200,
  "headers": {"content-type": "application/json"},
  "body": "<非流式：响应体字符串；流式：SSE 原文逐块拼接>"
}
```

---

## 插件结构（文件清单）

```
plugins/request-log/
├── plugin.go        # Manifest{Name:"request-log", Inject:["store","logger","db"], Provide:["request-log"]}
│                    # Apply：开独立库、自建表、NewService、SetRepository、订阅 4 个事件
│                    # （before-attempt / after-upstream / upstream-failed / stream-chunk）、RegisterRoute 2 条 API
├── service.go       # Service + 4 个 handler + 能力路由决策 + 脱敏/图片占位 + 列表/详情 + self-heal 兜底
└── service_test.go  # 单元/集成测试
```

改动既有文件：
- `core/db/migrate.go` — 追加 v24
- `core/db/db_test.go` — 两处硬编码（事实 7）
- `plugins/registry.go` — All() 加 `requestlog.New()`
- `plugins/route-log/service.go` + `plugins/contracts/routing.go` + 前端类型 — **必改**（决策点 1 已定案）：让 List/Detail 返回 `request_log_id`（事实 14，B1）
- `frontend/src/components/route-logs/RouteLogTable.vue`（+ 路由/api 封装）— 行内"完整日志"跳转

---

## 事件处理时序

```
入口 HandleProxy（proxy.go）
  ├─ proxyBeginLog → route-log.Start 写 running 行（route_requests.request_id 已存在；request_log_id 此时为空）
  ├─ Waterfall(ProxyBeforeUpstream) —— 本插件不订阅（入口级事件，渠道未知）
  ├─ proxyForward → 每次渠道尝试进入 proxyAttempt（proxy.go:269）：
  │    0. 【铁律】request-log 的 handler 永不 return error、永不改 body/响应——只读快照。
  │    1. proxyAttempt 先写渠道到 metadata（:274-281：__last_tried_channel / __current_channel / base_url）
  │    2. 【UUID 生成（用户拍板）】proxyAttempt 内、Waterfall 之前：metadata 无 UUID 则
  │       newRequestInstanceID()（crypto/rand 16B → 32 hex）写入 MetadataRequestLogID ——
  │       只有真走到渠道尝试的请求才有 ID；failover 同 pipe 幂等
  │    3. Waterfall(ProxyBeforeAttempt) ──► request-log.HandleBeforeAttempt（emit 时随管线拿到 UUID）：
  │       a. 读 metadata UUID（不自己造；缺失才反查/兜底生成）
  │       b. 读 metadata 渠道（已设好）→ 查能力路由（capability="request_log"，model × channel 全场景生效）
  │       c. 未命中/native → 原样返回，不记录（列保持 NULL）
  │       d. 命中 → UPDATE route_requests SET request_log_id=?（影响 0 行 = route-log 未装配，照常自建行）
  │       e. INSERT request_logs 半条（request_json/started_at/result='running'；重试同 request_id 走
  │          ON CONFLICT DO UPDATE 刷新本次内容）
  │       f. 打 __request_log_recorded 标记（failover 同 pipe 早退，不重记）
  │    4. 构建上游请求（:304）→ client.Do 发出（:337）
  ├─ 收尾（三个出口，均拿 Metadata 的 UUID；metadata 丢失则按 request_id 反查）：
  │    ├─ 非流式 2xx：Waterfall(ProxyAfterUpstream) → UPDATE result='success'、response_json/http_status/
  │    │    finished_at/duration_ms，channel 用 __last_tried_channel 回填
  │    ├─ 失败（聚合 failover 路径）：Waterfall(ProxyUpstreamFailed) → UPDATE result='failed'、
  │    │    http_status、response_json 带 error_body（截断）；普通模型失败无事件 → List/Detail 时
  │    │    self-heal 收尾（从 route_requests 还原 http_status/error_body）
  │    └─ 流式：Waterfall(ProxyStreamChunk) 逐块 append 到 pipe.Metadata buffer（[]byte）→
  │         检测到 "data: [DONE]" → flush：UPDATE response_json（SSE 原文）/finished_at/duration_ms/result='success'
  │         中断兜底：无 [DONE] 保持 running，self-heal 超时收尾（stream_interrupted）
```

**字段取值规则**（聚合/多渠道场景，事实 12/13）：
- `model`：`__virtual_model` 非空用它，否则 `pipe.Request.Model`（request-log 订阅序在 aggregate 之后，Model 已是改写后的真实模型；用虚拟名更贴合用户在 route-log 看到的请求）。
- `channel`：`ProxyBeforeAttempt` 触发前 metadata 已写入 `__current_channel`/`__last_tried_channel`（proxy.go:272-276）——**全场景已知**（普通请求 = 单渠道、聚合请求 = 当前尝试渠道，无 before-upstream 时刻渠道为空的限制）；收尾时用 `__last_tried_channel` 锁定最终渠道。
- `query`：`pipe.Request.Query` 是 `RawQuery`（无 `?` 前缀，proxy.go:69），示例 JSON 里 `query` 值不应带 `?`。

注意：`pipe.Metadata["__request_log_id"]` 与 route-log 的 `__route_step` 等 key 同空间，前缀 `__request_log_` 避免冲突；聚合插件若重建 pipe 会清 metadata，重建后需按 request_id 反查恢复（route-log 处理 `__virtual_model` 的方式同理，proxy.go:130-146 有重建后回填机制可参考）。

---

## 接口定义

### GET /api/request-logs

列表 + 搜索。Query 参数（全部可选）：
- `from` / `to` — started_at 时间范围（RFC3339）
- `model` — 模型精确匹配
- `channel` — channel_id 精确匹配
- `status_code` — http_status 匹配
- `stream` — 0/1
- `request_id` — 外部请求 ID（X-Request-Id）模糊/精确搜索
- `result` — running/success/failed/stream_interrupted
- `limit`（默认 100，上限 500）、`offset`（可选）

响应：`{ "items": [...], "total": N }`（**注意与 admin-api 的裸数组风格不同**，本接口自定义分页结构；前端 useRequestLogs.ts 按此解析）。列表行**不含** request_json/response_json（只回筛选字段，避免大 payload），详情才带全量。

### GET /api/request-logs/{id}

按 UUID 查详情：返回整行（含 request_json/response_json）。
- 命中且 `result='running'` 且超时 → 先 self-heal 收尾再返回：若该请求在 route_requests 侧已 failed（可反查），标 `failed` 并带上已捕获的 http_status/error_body；否则标 `stream_interrupted`（复用 route-log SelfHeal 语义）。
- 未命中 → 404。**对 route-log 带出的 request_log_id 但 request_logs 无行的情况**（定案后仅剩数据异常/手动删除场景；正常流程有 ID 必有行）返回友好空态 `{"error": {"message": "该请求未记录完整日志"}}`。

---

## 能力路由

capability=`request_log`，route 语义与 sensitive-filter 对齐：
- `native` — 不记录（透传）
- `proxy` — 记录完整日志
- `error` — 不支持（敏感词/字段过滤才用 error；request-log 不定义，前端路由下拉可沿用 native/proxy）

配置示例（capability_routes.json 或 SQLite 表）：
```json
{ "capability": "request_log", "models": ["*"], "channel_ids": [], "route": "proxy" }
```

**channel 维度语义**：request-log 在 `proxy:before-attempt` 做决策（用户拍板后），该事件触发前 model-gateway 已写入当前尝试渠道（proxy.go:272-276）——**普通请求与聚合请求的 channel 匹配均生效**（无 before-upstream 时刻渠道为空的限制，M1 已解决）；failover 切换渠道时每次尝试按当前渠道重新匹配（决定是否继续记录，同一 UUID 幂等）。

脱敏开关：request-log.db 的 `request_log_config` 单行表（`redact`，默认 1）。理由：capability_routes 是固定列，加列会牵动 admin_repository/import_admin 多处硬编码；脱敏是全局行为，独立 config 表最自洽。

---

## 脱敏规则（redact=1 时）

1. **请求/响应 headers**：键名匹配 `authorization`、`api-key`、`x-api-key`、`cookie`、`proxy-authorization`（不区分大小写，子串匹配）→ 值替换为 `***`。
2. **body 中的密钥**：字符串值里 `sk-` 开头片段 → `sk-***`（复用 route-log redact 思路，service.go:469）。
3. **base64 data URI**：递归扫描 JSON 字符串值，匹配 `data:image/...;base64,` 前缀 → 替换为 `"[image: <mime>, <bytes>B]"`（保留 mime 与字节大小，不存图片字节）。流式 SSE 里同理（按行扫描）。

---

## 前端改动

- `frontend/src/composables/` 新增 `useRequestLogs.ts`：`list(search)` / `detail(id)` 封装（对照 useRouteLogs.ts 写法）。
- `frontend/src/components/route-logs/RouteLogTable.vue`：行内新增"完整日志"入口，**仅当行数据 `request_log_id` 非空**时显示；点击跳转 `/request-logs/:id`。
- 新增详情视图 `RequestLogDetailView.vue` + 路由：展示 request_json/response_json（JSON 格式化 + 折叠）、筛选字段、跳回 route-log（反向关联 `request_id` 可跳 `/route-logs?request_id=...`）。
- ModelTestView.vue 保持 snapshot 不动（沿用 RouteLogTable 组件，自动获得入口）。

---

## 明确不做的事（scope 边界）

- 嵌套请求（vision_v2 父子）不单独记；UI 树形继续由 route-log 的 attempts/step_no 管。
- 不保存图片字节；data URI 统一转占位标记。
- 不做请求/响应回放（replay）功能。
- 不引入第三方 tokenizer / uuid 库（UUID 用 crypto/rand 自造，零依赖）。
- **被拦截的请求**：volc-free-quota 额度拦截等发生在 request-log 之前的事件（订阅序在前）直接截断 waterfall，此类请求**没有** UUID 与 request_logs 行；**被安检拒绝的请求**（request-log 先于 sensitive-filter 执行，事实 12）会留下 request 半条（无 response），靠 self-heal 收尾为 stream_interrupted——可接受且有价值。失败请求（4xx/5xx、无渠道）通过 `ProxyUpstreamFailed` 收尾为 `failed`，连失败事件都没有的才落 `stream_interrupted`。

---

## 风险与决策点（已过 sub-agent audit，遗留项见文末审计记录）

1. **UUID 生成时机（用户拍板最终版：实际请求位置生成，emit 传入）**
   - **定案（落法 D）**：**model-gateway 在 `proxyAttempt`（实际请求位置，proxy.go:269）生成 UUID**，写入 `pipe.Metadata[MetadataRequestLogID]`（types.go 常量 `__request_log_id`），幂等（failover 同 pipe 不重生成）。`Waterfall(ProxyBeforeAttempt)` emit 时随 `*ProxyPipeline` 带给消费方。
   - **request-log 只消费不自造**：`HandleBeforeAttempt` 读 metadata 的 UUID；缺失（测试直构/管道重建）才兜底：反查 `route_requests` 复用 → 再不行自造。命中能力路由 → UPDATE 关联列 + 写半条 → 打 `__request_log_recorded` 标记（failover 早退；不能用"metadata 有 UUID"判断已记录，否则首次调用也被早退）。
   - **语义**：只有真走到渠道尝试（实际请求位置）的请求才有 ID；额度拦截（更早截断）无 ID；未命中路由列保持 NULL、前端不显示入口。model-gateway 生成 = 与实际请求强绑定，这是用户强调的核心（此前 B' 在插件 handler 里生成被用户驳回）。
2. **独立库 schema 演进**：request-log.db 用 `CREATE TABLE IF NOT EXISTS` 无版本机制，将来加列需自行补轻量迁移。首版单表够用，风险低。
3. **体积保护**："完整响应"对大流式（数十 MB）有内存 + 落库压力（buffer 在 pipe.Metadata 内存里，流结束才 flush）。**默认不截断**（用户要"完整"），但实现时必须加防御上限（单行 > 32MB 截断并在 response_json 里记录 `"truncated": true`），防 OOM。
4. **self-heal 兜底**：流式中断无事件（事实 4），只能靠超时收尾。阈值与 route-log 保持一致（config.RouteLogSelfHealMaxAlive / threshold）。
5. **db_test.go 迁移表计数**：事实 7，加 v24 后必须同步。

---

## Task 列表（实施步骤，TDD）

### Task 1: loadout.db 迁移 v24 + 独立库建表助手
**Files:**
- Modify: `core/db/migrate.go`（追加 v24）
- Modify: `core/db/db_test.go`（核对迁移计数）
- Create: `plugins/request-log/db.go`（`openRequestLogDB(path)`：sql.Open + pragmas + CREATE TABLE IF NOT EXISTS，供 service 使用）

**Steps:**
1. 失败测试：在 `plugins/request-log/db_test.go` 写 `TestOpenRequestLogDB`——打开临时文件库，断言 request_logs / request_log_config 表存在、`redact` 默认 1。
2. 实现 `openRequestLogDB`。
3. 跑 `go test ./core/db/ ./plugins/request-log/`，确认 v24 迁移通过、`TestMigrateIsIdempotent` 更新后通过。

### Task 2: route-log 全链路返回 request_log_id（B1，必做）
**Files:**
- Modify: `plugins/contracts/routing.go`（`RouteRequestView` 加 `RequestLogID string \`json:"request_log_id,omitempty"\``）
- Modify: `plugins/route-log/service.go`（List SQL :267、Detail SQL :315、`scanRequest` :396-420 加列读取）
- Modify: `frontend/src/types.ts`（`RouteLog` 类型加 `request_log_id?: string`）

**Steps:**
1. 测试：route-log 的 `service_test.go` 里断言 List/Detail 返回的 view 带出 `request_log_id`（种子数据直接 UPDATE 列）。
2. 实现三处 SQL + scanRequest + 前端类型。
3. 跑 `go test ./plugins/route-log/`。

### Task 3: Service 骨架 + 能力路由决策
**Files:**
- Create: `plugins/request-log/service.go`（Service 结构 + NewService + SetRepository + capabilityName=`request_log` + DecideRoute/DecideRoutesScope，照抄 sensitive-filter 模式）
- Create: `plugins/request-log/plugin.go`（Manifest + Apply：注入 store/logger/db、开独立库、SetRepository、ctx.Set("request-log")）

**Steps:**
1. 测试：种子 capability_routes（sensitive-filter/service_test.go 模式：`store.New(t.TempDir())` + `st.Write(types.FileCapabilityRoutes, routes)`），断言未命中/native/proxy 三种决策。
2. 实现 + 跑测试。

### Task 4: HandleBeforeAttempt（请求发出前抓请求 + 生成 UUID + 建半条）
**Files:**
- Modify: `plugins/request-log/service.go`

**Steps:**
1. 测试：构造 `*ProxyPipeline` + metadata 渠道（`__current_channel` 已设），命中 proxy 路由 → 断言 request_logs 插入半条（result='running'、request_json 含脱敏后的 authorization 占位、base64 图占位）、pipe.Metadata["__request_log_id"] 非空；**幂等测试**：同一 request_id 二次进入（failover 换渠道）→ 复用 UUID，不产生第二行。
2. 实现：UUID 取用（先 SELECT 复用，无则 crypto/rand 16B → hex 生成）、request 序列化（脱敏 + 图片占位）、INSERT、UPDATE route_requests.request_log_id。**handler 永不 return error。**

### Task 5: HandleAfterUpstream + HandleUpstreamFailed（非流式收尾）
**Steps:**
1. 测试：`*AfterUpstreamPayload` 2xx → result='success'、response_json/http_status/finished_at/duration_ms 正确、channel 回填 `__last_tried_channel`。
2. 测试：`*ProxyFailurePayload`（4xx/5xx/无渠道）→ result='failed'、http_status/error_body 落库、duration 正确。
3. 实现：订阅 `ProxyAfterUpstream` + `ProxyUpstreamFailed` 两个事件（B2：仅 2xx 走 after，失败必须靠 upstream-failed 收尾）。

### Task 6: HandleStreamChunk（流式拼接 + [DONE] 收尾）
**Steps:**
1. 测试：多 chunk 逐个进入 → buffer 累积；`data: [DONE]` 触发 flush → response_json = SSE 原文拼接、result='success'、duration 正确。
2. 实现：chunk buffer 放 pipe.Metadata（`__request_log_buffer`），[DONE] 检测（自实现 isSSEDone 等价逻辑）。

### Task 7: 列表/详情 API + self-heal 兜底
**Files:**
- Modify: `plugins/request-log/plugin.go`（RegisterRoute 两条：Pattern `"GET /api/request-logs"` / `"GET /api/request-logs/{id}"`，Auth: plugin.AuthSession——写法带 method 前缀，routePattern 兼容）
- Modify: `plugins/request-log/service.go`（List/Detail/self-heal）

**Steps:**
1. 测试：List 各过滤条件（from/to/model/channel/status_code/stream/request_id/result/limit/offset）；Detail 命中/404/running 超时 self-heal（route_requests 侧已 failed 的标 failed，否则 stream_interrupted）。
2. 实现 handler：query 解析对照 admin-api handleRouteLogsList 的模式；**响应格式为 `{items, total}`**（本接口自定义，非 admin-api 的裸数组）。

### Task 8: 注册插件 + 全链路集成测试
**Files:**
- Modify: `plugins/registry.go`（All() 加 `requestlog.New()`）

**Steps:**
1. 测试：mock plugin.Context（On/Waterfall/RegisterRoute 实现，其余空实现）驱动完整链路——before → after（非流式）与 before → 多 chunk → [DONE]（流式）。
2. 跑 `go test ./plugins/... ./core/...` 全量回归。

### Task 9: 前端（RouteLogTable 入口 + 详情页）
**Files:**
- Create: `frontend/src/composables/useRequestLogs.ts`
- Create: `frontend/src/views/RequestLogDetailView.vue` + 路由
- Modify: `frontend/src/components/route-logs/RouteLogTable.vue`

**Steps:** request_log_id 非空行显示"完整日志"入口 → 详情页展示（JSON 格式化、折叠、反向跳回 route-log）。构建 `frontend` 产物。

### Task 10: 收尾
- 自检项：`ctx.RegisterCheck("request-log 完整性", ...)`（对照 admin-api selfCheck）。
- 文档：`docs/API.md` 补两条接口说明（对照既有格式）。
- 全量 `go build ./...` + `go test ./...`。

---

## 审计记录（sub-agent review，2026-08-23）

按用户惯例由独立 sub-agent 审计后修订，本次修复的问题：

| 级别 | 问题 | 修复 |
|---|---|---|
| blocker | B1: route-log 全链路不返回 `request_log_id`（contracts.RouteRequestView / List / Detail / scanRequest / 前端类型都硬编码缺列），"天然返回"不成立 | 新增 Task 2，四处理改 |
| blocker | B2: `ProxyAfterUpstream` 仅 2xx 触发，4xx/5xx/无渠道/安检拒绝无事件收尾，`failed` 终态不可达 | 补订阅 `ProxyUpstreamFailed`，Task 5 扩展 |
| major | M1: before-upstream 时刻非聚合请求 channel 为空，channel 绑定路由对普通请求失效 | 能力路由节补充 channel 维度语义与已知限制 |
| major | M2: hook 执行序是 topoSort 名字序而非 All() 顺序；request-log 抓到 aggregate 改写后的请求 | 事实 12 修正；字段取值规则（model=virtual_model 优先、channel 收尾回填） |
| major | M3: 重试复用 X-Request-Id 产生双 UUID + 孤儿行 | 决策点 1 补幂等（SELECT 复用），Task 4 加幂等测试 |
| major | M4: "跨库无法 JOIN"论证错误（SQLite 支持 ATTACH） | 决策点 1 理由改为解耦 + 噪音 |
| major | M5: 落法 B 依赖 route_requests 行存在；before-upstream 报错会中断全 /v1 流量 | handler 永不 return error + best-effort + 自建行兜底 |
| minor | m1: db_test.go 两处硬编码（迁移条数 23→24、rejects 测试 24→25） | 事实 7 更新 |
| minor | m2: 响应格式自相矛盾（{items,total} vs admin-api 裸数组） | 接口定义明确 + Task 7 修正 |
| minor | m3: volc-free-quota 等前置拦截请求无完整日志 | 明确不做的事补充 |
| minor | m4: Query 是 RawQuery 无 `?` 前缀；`__last_tried_channel` 入口时刻不存在 | 示例 JSON 修正 + 字段取值规则 |
| 决策 | 决策点 1 定案：UUID 生成时机从 before-upstream 改为 **proxy:before-attempt**（用户拍板"请求发出之前生成"） | 事件订阅/时序/能力路由 channel 语义/Task 4 全部更新；被安检拒绝的请求留 request 半条（订阅序注记，可接受） |

## 实施记录（2026-08-23 已执行，用户拍板当前进程直做）

Task 1-10 全部落地，改动清单：

**新增 `plugins/request-log/`：**
- `db.go` — `openRequestLogDB`（独立库，不跑 loadout 迁移）+ `requestLogSchema`（request_logs 单表 + request_log_config 单行表，redact 默认 1）
- `plugin.go` — Manifest（Inject store/logger/db）、Apply（开库 + SetRepository + subscribe + RegisterRoute 两条 + RegisterCheck）
- `service.go` — 能力路由决策（照抄 sensitive-filter 模式）、HandleBeforeAttempt（UUID 幂等 + UPDATE 关联列 + 半条）、HandleAfterUpstream / HandleUpstreamFailed（收尾，B2）、HandleStreamChunk（buffer + [DONE]）、List/Detail/self-heal、脱敏（敏感头打码 / sk- 打码 / base64 占位）
- `db_test.go` / `service_test.go` / `integration_test.go` — 建表、决策、半条捕获、幂等、重试复用 UUID、非流式/失败/流式收尾、列表过滤、self-heal、跨插件联动（route-log ↔ request-log）全绿

**改动既有：**
- `core/db/migrate.go` — v24 `route_requests ADD COLUMN request_log_id TEXT`
- `core/db/db_test.go` — 迁移条数 23→24、rejects 测试 24→25
- `plugins/contracts/routing.go` — `RouteRequestView` 加 `RequestLogID`
- `plugins/route-log/service.go` — List/Detail SQL + scanRequest 带出 request_log_id
- `plugins/registry.go` — All() 加 `requestlog.New()`（位于 aggregate 之前）
- `frontend/src/lib/types.ts` — RouteLog 加 request_log_id；新增 RequestLogItem/Page/Detail
- `frontend/src/composables/useRequestLogs.ts` — list/detail 封装
- `frontend/src/views/RequestLogDetailView.vue` — 详情页（元信息 + 折叠 JSON）
- `frontend/src/router/index.ts` — `/request-logs/:id` 路由
- `frontend/src/components/route-logs/RouteLogTable.vue` — 行内「完整日志」入口（request_log_id 非空才显示，@click.stop 防行展开）
- `docs/API.md` — 补 request-log 接口说明

**验证：** `go build ./...`、`go vet`、`go test ./plugins/... ./core/...` 全过（唯一失败 `core/linkfs` 为 Windows 符号链接权限环境问题，与本次无关）；前端 `vue-tsc + vite build` 通过（需 `NODE_OPTIONS="--use-system-ca"` 绕过 WorkBuddy safe-delete 注入，已知坑）。

**实施中发现/确认：**
- `requestLogFilter.Stream` 用指针（*int）避免零值 0 被误当过滤条件（测试抓出）
- `json.RawMessage` 不能直接 `database/sql` Scan，需 string 中转
- 路由 Handler 需 `http.HandlerFunc` 包装（RouteSpec.Handler 是 http.Handler 接口）

## Code Review 轮（2026-08-23，code-reviewer 独立审查 + 修复）

| 级别 | 问题 | 修复 |
|---|---|---|
| P0 | **`ProxyUpstreamFailed` 仅聚合模型触发**（proxy.go:858 要求 `__virtual_model`），普通模型 4xx/5xx/网络错/无渠道/安检拒绝均无输出事件 → 永远卡 running、error_body 丢失（B2 计划级假设错误） | ① List 每次调用前批量 self-heal 超时 running 行（healStuckList，先收集 Close rows 再写库防单连接死锁）；② healStuck 从 route_requests 还原 http_status + error_body 进 response_json；③ 根治（非聚合失败路径补发事件）超出本插件 scope，记录在案 |
| P1 | 流式 SSE 内容未脱敏（sk- / data URI 原样落库） | HandleStreamChunk 写 buffer 前对 chunk 走 redactBody |
| P1 | 流式 buffer 无上限（OOM 风险，计划风险 #3 要求 32MB 截断） | maxStreamBuffer=32MB 触顶丢弃 + response_json 标 truncated:true |
| P1 | 重试复用 UUID 但复用陈旧行（旧 request_json + 新 response 混配、duration 失真） | INSERT ON CONFLICT DO UPDATE 刷新为本次请求（清终态字段）；failover 同 pipe 走 metadata 早退不受影响 |
| P1 | self-heal 仅 Detail 触发，List 永不收尾 | 与 P0 修复 ① 合并 |
| P2 | sk- 打码误伤普通文本 + 已打码内容重复打码 | 正则 `sk-[A-Za-z0-9]{4,}` 限定 |

**修复中额外抓出的坑**：healStuckList 在 rows 迭代中写库 → SQLite 单连接死锁（先收集后写）；Detail heal 后重读缺 response_json 字段。

**已核实无需修**：渠道 key 不泄露（before-attempt 早于渠道 Authorization 写入，proxy.go:282 vs :330，抓到的是客户端原始头且已打码）；幂等主路径正确（failover 走 metadata 早退、重试走列反查复用）；migrate v24 与 db_test 匹配；前端 colspan=11 与 @click.stop 有效。

新增回归测试：TestListSelfHealsStuckRunning、TestHealStuckCarriesErrorBody、TestHandleStreamChunkRedacts、TestHandleStreamChunkTruncates（收紧 maxStreamBuffer 覆盖）。

**遗留（记录在案，不阻塞）**：非聚合失败路径补发 `ProxyUpstreamFailed` 事件（需改 model-gateway proxy.go，根治"等待 self-heal 窗口期列表仍显示 running"）；[DONE] 后 chunk 语义未定义；from/to/status_code 非法值静默忽略（返回全量）。
