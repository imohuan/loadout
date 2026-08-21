# 转发日志（route-log）全链路架构说明

> 状态：📖 文档（2026-08-21 整理）
> 目标：完整说明一次模型请求（普通模型 / 虚拟模型聚合 / 视觉能力）如何被写入转发日志，前端「路由日志」页如何展示，以及「卡死 running」自愈机制的设计现状与演进方向。

---

## 目录

1. [核心结论：贯通参数是 request_id](#1-核心结论贯通参数是-request_id)
2. [数据模型：两张表](#2-数据模型两张表)
3. [统一入口：HandleProxy](#3-统一入口handleproxy)
4. [三条请求链路](#4-三条请求链路)
   - 4.1 普通模型
   - 4.2 虚拟模型（聚合路由）
   - 4.3 视觉能力
5. [step_no 共享空间与 action 判定](#5-step_no-共享空间与-action-判定)
6. [状态流转（result 枚举）](#6-状态流转result-枚举)
7. [前端展示与刷新](#7-前端展示与刷新)
8. [卡死 running 自愈：现状与演进](#8-卡死-running-自愈现状与演进)
9. [关键代码索引](#9-关键代码索引)

---

## 1. 核心结论：贯通参数是 request_id

**三类请求（普通模型 / 虚拟模型 / 视觉能力）全部通过 `request_id`（HTTP 头 `X-Request-Id`）聚到同一组日志。**

- 一次 `/v1/chat/completions` 请求 = **一行** `route_requests` + **多行** `route_attempts`；
- 视觉识别的 attempt 与主链路切换渠道/模型的 attempt **共用同一个 `request_id`**；
- `step_no` 共享单调递增空间，保证时间线连续（视觉在前、主链路续接）。

> ⚠️ 注意：`virtual_model` **不是**分组键。它只是 `route_requests` 上的一个冗余列，用于标记「聚合模型的虚拟名」（如 `auto`），便于按虚拟名检索。真正把视觉 + 主链路 + 切换过程绑在一起的是 `request_id`。

### request_id 的生成与贯通（三层保险）

| 层级 | 位置 | 逻辑 |
|---|---|---|
| 1. 中间件 | `core/servercore/server.go` | 优先沿用客户端 `X-Request-Id`，为空则生成并写回请求头 |
| 2. 主入口兜底 | `plugins/model-gateway/proxy.go:55` | 无中间件环境（测试/直连）下 `newRequestID()` 兜底，保证 before-upstream hook 执行时恒非空 |
| 3. hook 重建保护 | `proxy.go:97` | hook 若重建了 pipe 且丢失 request_id，沿用 hook 前的 `originalRequestID`，不生成新 id 破坏一致性 |

---

## 2. 数据模型：两张表

定义位置：`core/db/migrate.go`（v1 建表 + v3/v4 增补 usage 字段）。

### 2.1 `route_requests`（每请求一行）

| 列 | 类型 | 说明 |
|---|---|---|
| `request_id` | TEXT PK | 贯通键（来自 X-Request-Id） |
| `requested_model` | TEXT | 请求模型（聚合场景下被改写为虚拟名） |
| `virtual_model` | TEXT | 虚拟模型名，仅聚合模型非空 |
| `started_at` | TEXT | RFC3339Nano，UTC |
| `finished_at` | TEXT NULL | 收尾时间，running 时为空 |
| `result` | TEXT | running / success / failed / stream_interrupted |
| `final_model` | TEXT NULL | 最终真实模型名 |
| `final_channel_id` | TEXT NULL | 最终渠道 |
| `http_status` | INTEGER | 最终 HTTP 状态码 |
| `duration_ms` | INTEGER | 总耗时 |
| `error_message` | TEXT | 最终错误 |
| `stream` / `prompt_tokens` / `completion_tokens` / `cached_tokens` | INTEGER | usage 汇总（v4 增补） |

### 2.2 `route_attempts`（同请求内每次尝试一行）

| 列 | 类型 | 说明 |
|---|---|---|
| `id` | INTEGER PK | 自增 |
| `request_id` | TEXT FK | 关联 requests，`ON DELETE CASCADE` |
| `previous_attempt_id` | INTEGER FK | 前一次尝试（切换链） |
| `step_no` | INTEGER | 共享递增空间，`UNIQUE(request_id, step_no)` |
| `action` | TEXT | 首次尝试 / 切换渠道 / 切换模型 / 视觉识别 |
| `model` | TEXT | 本次尝试的真实模型名 |
| `channel_id` | TEXT | 本次尝试的渠道 |
| `started_at` / `finished_at` | TEXT | 本次尝试起止 |
| `result` | TEXT | 本次尝试结果 |
| `failure_class` / `status_code` / `error_message` | TEXT/INT | 失败信息 |
| `duration_ms` | INTEGER | 本次尝试耗时 |
| `metadata_json` | TEXT | 附加信息（如视觉的 `{"capability":"vision","image_count":N}`） |
| `stream` / tokens | INTEGER | usage（v3 增补） |

### 2.3 索引

```sql
idx_route_requests_started_at            (started_at DESC)
idx_route_requests_requested_model_started_at (requested_model, started_at DESC)
idx_route_attempts_request_step          (request_id, step_no)
idx_route_attempts_channel_started_at    (channel_id, started_at DESC)
idx_route_attempts_model_started_at      (model, started_at DESC)
idx_route_attempts_result_started_at     (result, started_at DESC)
```

---

## 3. 统一入口：HandleProxy

所有 `/v1/{path...}` 请求（任意路径、任意方法）进入：

```
plugins/model-gateway/proxy.go:26  HandleProxy
```

主流程（`proxy.go:26-110`）：

```
1. io.ReadAll(r.Body)                    读原始 body（不解析不清洗）
2. subPath = TrimPrefix(path, "/v1/")    提取剩余路径
3. sniffRequest(body, r)                 轻量提取 model/stream（不解析结构）
4. 构造 ProxyPipeline{ RequestID: X-Request-Id, ... }
5. RequestID 为空 → newRequestID() 兜底
6. setRequestIDHeader(w)                 响应头回写同一 id
7. stream 时注入 StreamWriter（视觉流输出通道）
8. key 白名单校验（model 非空才校验）
9. proxyBeginLog(r, pipe)                【占位日志】写 running（hook 前）
10. Waterfall(ProxyBeforeUpstream)       【输入 hook】各插件依次改写
11. proxyBeginLog(r, pipe)               【补全日志】二次调用 UPSERT 虚拟模型名
12. proxyForward(w, r, pipe, model, started)  【底层中转】路由渠道并转发
```

关键点：

- **占位日志先写**：`proxyBeginLog` 在 before-upstream hook **之前**执行，视觉识别（可能耗时数十秒）期间 UI 已能看到该请求。
- **二次 Begin 是 UPSERT**：`route-log.Start` 用 `INSERT ... ON CONFLICT(request_id) DO UPDATE`，同一 request_id 合并为一行，hook 后仅补全 `requested_model` / `virtual_model`。
- **hook 失败路径**：`Waterfall` 返回 error 时走 `proxyRejectedLog`（写失败收尾）并返回错误，不再转发。

---

## 4. 三条请求链路

### 4.1 普通模型（直接请求）

```
/v1/chat/completions
  └─ HandleProxy
       ├─ proxyBeginLog            → route_requests 写 running 占位
       ├─ Waterfall(ProxyBeforeUpstream)
       │    └─ （无命中能力插件，原样通过）
       ├─ proxyForward             → 按 model 路由候选渠道，逐个尝试
       │    ├─ 成功 → proxyAttemptLog(request_id, step, "首次尝试", result=success)
       │    └─ 失败 → proxyAttemptLog(... result=failed) → 尝试下一个候选
       │         └─ 渠道内多 Key → 切换渠道（action="切换渠道"）
       └─ proxyFinishLog           → route_requests 收尾（result/final_model/tokens/耗时）
```

记录形态（一个请求）：

```
route_requests: request_id=R1, requested_model=gpt-4o, result=success
route_attempts: (R1, step1, 首次尝试, gpt-4o, chA, success)
```

### 4.2 虚拟模型（聚合路由）

前置：用户在后台配置了聚合模型（如 `auto`），包含多个真实模型目标。

```
/v1/chat/completions  { model: "auto" }
  └─ HandleProxy
       ├─ proxyBeginLog            → 占位（requested_model=auto，virtual_model 未知）
       ├─ Waterfall(ProxyBeforeUpstream)
       │    └─ aggregate.HandleProxyBeforeUpstream (plugins/aggregate/proxy.go:33)
       │         ├─ findAggregate("auto") 命中
       │         ├─ Metadata["__virtual_model"] = "auto"      ← 聚合分组标记
       │         ├─ Metadata["__aggregate_targets"] = targets
       │         ├─ selectAvailableTarget → 选中第一个可用真实模型
       │         ├─ rewriteBodyModel(target.Model)            ← 改写请求体 model
       │         └─ 返回改写后的 pipe（Model 已是真实模型）
       ├─ proxyBeginLog（二次）    → UPSERT 补全 virtual_model="auto"
       ├─ proxyForward             → 用真实模型路由渠道
       │    ├─ 成功 → attempt(action="首次尝试")
       │    └─ 失败 → tryProxyAggregateFailover (proxy.go:458)
       │         ├─ 从 __aggregate_targets 选下一个候选
       │         ├─ 改写 model → 重试 proxyForward
       │         └─ attempt(action="切换模型")     ← 虚拟模型特有
       └─ proxyFinishLog           → 收尾（final_model=实际成功的模型）
```

记录形态（一个请求，两次模型尝试）：

```
route_requests: request_id=R2, requested_model=auto, virtual_model=auto, final_model=deepseek-v4, result=success
route_attempts: (R2, step1, 首次尝试, deepseek-v4,   chA, failed)
                (R2, step2, 切换模型, glm-4.5,       chB, success)
```

> 与普通模型共用同一个 `proxyForward` 底层，只是多了一个改写 hook 和失败切换逻辑。

### 4.3 视觉能力（图片识别）

前置：请求带图片，且命中能力路由表的 vision 能力（模型 + 渠道约束）。

**关键事实：视觉请求不走 proxyForward，它是独立直连！**

```
/v1/chat/completions  { messages: [..., {image_url}] }
  └─ HandleProxy
       ├─ proxyBeginLog            → 占位 running
       ├─ Waterfall(ProxyBeforeUpstream)
       │    └─ vision.HandleProxyBeforeUpstream (plugins/vision/proxy.go)
       │         ├─ DetectImages → 有图
       │         ├─ DecideRouteScope → 命中 vision 路由（按模型 + 渠道约束）
       │         ├─ 每个 via_option：
       │         │    └─ callVision (plugins/vision/service.go:476)
       │         │         ├─ 独立 http.Client{Timeout: VisionTimeout}
       │         │         ├─ 直连 ch.BaseURL + "/chat/completions"   ← 不走 proxyForward！
       │         │         ├─ visionAttempt(request_id, step, "视觉识别", running)  ← 开始占位
       │         │         └─ visionAttempt(... success/failed)                    ← 结束更新
       │         ├─ 成功 → RewriteMessages(把识别文本写回 messages)
       │         └─ 失败 → visionError（整请求失败）
       ├─ proxyForward             → 用改写后的 messages 走主链路
       └─ proxyFinishLog           → 收尾
```

记录形态（一个含图请求）：

```
route_requests: request_id=R3, requested_model=auto, result=success
route_attempts: (R3, step1, 视觉识别, qwen-vl-max, chA, success, metadata={capability:vision, image_count:2})
                (R3, step2, 首次尝试, deepseek-v4, chB, success)
```

**视觉 attempt 的写入实现**（`plugins/vision/service.go:75` `visionAttempt`）：

- 直接调 `routeLog.Attempt`（vision 插件通过 `SetRouteLog` 注入了 route-log 服务）；
- `Action` 固定 `"视觉识别"`；
- `Metadata` 带 `{"capability": "vision", "image_count": N}`；
- 两阶段：开始写 running 占位、结束以同一 step_no 更新 success/failed。

---

## 5. step_no 共享空间与 action 判定

### 5.1 共享递增

视觉与主链路在**同一个 request_id 下共享 step_no 递增空间**（v4 改造后的方案，详见 `docs/VISION-ROUTE-LOG-PLAN.md`）：

- 视觉 attempt：`step_no = idx + 1`（idx 为 via_option 下标，从 0 起）；
- 视觉循环结束：`pipe.Metadata["__route_step"] = 视觉最后 step`（`vision/proxy.go:391`）；
- 主链路 `proxyAttemptLog`：从 `__route_step + 1` 续接（`model-gateway/proxy.go:594-596`）。

### 5.2 action 判定（防误判）

`model-gateway/proxy.go:597-606`：

```go
mainStep, _ := pipe.Metadata["__main_route_step"].(int)   // 独立计数器，不计视觉
action := "首次尝试"
if mainStep > 0 {
    action = "切换渠道"
    if pipe.Metadata["__virtual_model"] != nil {
        action = "切换模型"       // 聚合模型才会出现
    }
}
```

> 为什么用独立的 `__main_route_step` 而不是直接复用 step：视觉已占用 step1，若直接用 step 数判断，主链路第一次尝试会被误判为「切换渠道」。

### 5.3 前端 action 展示

`frontend/src/components/route-logs/RouteLogTable.vue` `ACTION_LABELS`：

| action | 中文 | 颜色 |
|---|---|---|
| 首次尝试 | 首次尝试 | 蓝 |
| 切换渠道 | 切换渠道 | 琥珀 |
| 切换模型 | 切换模型 | 紫 |
| 视觉识别 | 视觉识别 | teal |

（兼容旧英文枚举：initial / switch_channel / switch_model）

---

## 6. 状态流转（result 枚举）

### 6.1 请求级（route_requests.result）

| 值 | 含义 | 写入点 |
|---|---|---|
| `running` | 进行中 | `route-log.Start`（占位） |
| `success` | 成功 | `proxyFinishLog` |
| `failed` | 失败 | `proxyFinishLog` / `proxyRejectedLog` |
| `stream_interrupted` | 流中断 | 后端自愈 / 异常收尾 |

### 6.2 尝试级（route_attempts.result）

| 值 | 含义 |
|---|---|
| `running` | 该次尝试进行中 |
| `success` | 该次尝试成功 |
| `failed` | 该次尝试失败 |
| `skipped` | 候选被跳过 |

### 6.3 正常生命周期

```
Start(running) → Attempt × N（每次渠道/模型尝试）→ Finish(success | failed)
                        ↑
            视觉识别在 hook 阶段先写 attempt（running→success/failed）
```

---

## 7. 前端展示与刷新

### 7.1 页面

- `frontend/src/views/RouteLogsView.vue` ——「转发日志」tab 主视图
- `frontend/src/components/route-logs/RouteLogTable.vue` —— 表格（列表 + 展开详情）
- `frontend/src/composables/useRouteLogs.ts` —— API 封装

### 7.2 数据流

```
后端 /api/route-logs          → useListLoader.refresh()（3s 定时）
后端 /api/route-logs/{id}     → expand() / refreshActiveDetails()（展开时 / 定时）
detailsMap (Map<request_id, RouteLog>)  → displayLogs 合并 attempts/error_message
```

### 7.3 3 秒自动刷新链（`RouteLogsView.vue:110-121`）

```ts
setInterval(async () => {
  await refresh({ silentError: true })   // 拉列表（错误静默）
  await refreshActiveDetails()           // 刷新已展开的详情（running 必刷，终态按重试策略）
  await selfHealStuckLogs()              // 自愈触发（见 §8）
}, 3000)
```

### 7.4 详情重试策略（`RouteLogsView.vue:36-51`）

- running 的展开行：每次必刷；
- 终态但 attempts 为空：`detailRetryCount` 最多重试 `DETAIL_RETRY_MAX=5` 次；
- 达上限：收起该行，避免无限轮询一个本无 attempts 的请求。

---

## 8. 卡死 running 自愈：现状与演进

### 8.1 问题背景

后端 `Start` 写 running 占位后，若 `Finish` 因进程崩溃 / 异常中断 / 写入失败而未执行，该请求会**永久卡在 running**，UI 显示「进行中 0 ms」。

### 8.2 已实现（当前代码，登记表 + 时间兜底）

后端配置：
- `core/config/config.go`：
  - `RouteLogSelfHealTimeout`（默认 60s，`LOADOUT_ROUTE_LOG_SELF_HEAL_TIMEOUT`，<=0 禁用）——时间兜底阈值；
  - `RouteLogSelfHealMaxAlive`（默认 10 分钟，`LOADOUT_ROUTE_LOG_SELF_HEAL_MAX_ALIVE`）——活跃登记表最大存活时间。

活跃登记表（`plugins/route-log/service.go`）：
- `Service` 新增 `activeAt map[request_id]registeredAt` + mutex；
- `Start` 写入登记（UPSERT 幂等，重复 Start 刷新登记时刻）；
- `Finish` 删除登记（转发结束无论成败）；
- `IsActive(requestID, maxAge)`：表里有且未超 maxAge → true；表里没有 → false（转发已结束/进程已崩溃）；超时 → false（死锁兜底）。

自愈判定（`SelfHeal`，两层）：
```
1. IsActive false → 事实判死（转发已结束/进程崩溃/超时兜底）
2. 时间兜底：result='running' && finished_at 为空 && age >= threshold
两层都认为「还活着」才 no-op；修复动作 UPDATE 带 WHERE 防并发。
```

HTTP 接入（`plugins/admin-api/routing.go` `handleRouteLogDetail`）：
- 仅 `?repair=1` 时调用 `SelfHeal`（普通查看详情不触发修复，行为可观察）。

前端触发（`RouteLogsView.vue`）：
- `selfHealStuckLogs()`：发现 `result='running' && age>60s` 的行，调 `service.detail(id, { repair: true })`（60s 去重）；
- `useRouteLogs.ts` `detail` 支持 `{ repair?: boolean }` → 拼 `?repair=1`；
- 后端修复后列表靠 3s 刷新自然覆盖。

### 8.3 ~~演进方向（组级状态机方案）~~ 已否决

> ⚠️ **已否决（2026-08-21）**：该方案在评审中被废弃。原因：逻辑复杂（组内推断依赖跨请求状态），且服务崩溃时无法预料——进程一死所有内存状态消失，组内推断失去意义。最终采用 §8.2 的**活跃登记表**方案（只判断单条请求的转发是否还活着，进程崩溃自动判死）。以下保留仅作历史记录，勿再实现。

用户曾要求按「组」判断，而非单条超时：

```
前端：检测 running > 60s 的行 → 调详情接口（带修复参数）
后台收到修复参数后：
  1. 按分组参数拉取该组所有详情记录
  2. 看组内最后一条的结局：
     ├─ 成功 → 把卡住的那条直接标记成功/完成
     └─ 失败 → 查组内是否还有「后续模型请求」在 running：
                ├─ 无 → 组整体已结束，标记卡住那条失败
                └─ 有 → 组还在继续，先不管
后台在请求发生时创建一个「状态器」，跟踪这个模型任务请求是否还在继续
```

**当时的待确认分组键**：

| 候选 | 优点 | 缺点 |
|---|---|---|
| `request_id` | 天然贯通三类请求，视觉 + 主链路 + 切换全在一组；改动最小 | 一组只有一次请求，「后续模型请求」需跨 request 定义 |
| `virtual_model` | 聚合场景下把多次「运行」聚为一组（如连续多次请求 auto） | 普通模型无 virtual_model 值；跨请求语义需补充 |
| `requested_model` | 普通 + 聚合都非空 | 聚合请求的 requested_model 已被改写为虚拟名，与真实模型混层 |

**当时的调研结论**（仍有参考价值）：

- 视觉请求**不走 proxyForward**，走独立 `callVision`（`vision/service.go:517` 自己 `new http.Client`）；
- 真正三类都经过的共同点是 **route-log 服务的 `Start/Attempt/Finish`**。

---

## 9. 关键代码索引

| 模块 | 文件 | 关键符号 |
|---|---|---|
| 统一入口 | `plugins/model-gateway/proxy.go` | `HandleProxy`(26), `proxyForward`(161), `proxyBeginLog`(571), `proxyAttemptLog`(590), `proxyFinishLog`(634), `proxyRejectedLog`(667), `tryProxyAggregateFailover`(458), `sniffRequest`(114) |
| 聚合改写 | `plugins/aggregate/proxy.go` | `HandleProxyBeforeUpstream`(33), `HandleProxyUpstreamFailed`(96) |
| 视觉链路 | `plugins/vision/service.go` | `HandleBeforeUpstream`(287), `callVision`(476), `visionAttempt`(75), `DecideRouteScope`(131) |
| 视觉 step | `plugins/vision/proxy.go` | 视觉 attempt 写入（322-399 区段） |
| 日志存储 | `plugins/route-log/service.go` | `Start`(29), `Attempt`(38), `Finish`(62), `List`(67), `Detail`(113), `SelfHeal`(新增) |
| 契约 | `plugins/contracts/routing.go` | `RouteRequest`(92), `RouteAttempt`(99), `RouteFinish`(129), `RouteRequestView`(153), `RouteLog` 接口(172) |
| 表结构 | `core/db/migrate.go` | `route_requests`(80), `route_attempts`(93), 索引(111-116) |
| 配置 | `core/config/config.go` | `RouteLogSelfHealTimeout` |
| 后端路由 | `plugins/admin-api/routing.go` | `handleRouteLogDetail`(852), `handleRouteLogsList`(829) |
| 前端视图 | `frontend/src/views/RouteLogsView.vue` | 自动刷新(110), `refreshActiveDetails`(68), `selfHealStuckLogs`, `shouldSelfHeal`, `displayLogs`(53) |
| 前端表格 | `frontend/src/components/route-logs/RouteLogTable.vue` | `ACTION_LABELS`(60), `RESULT_LABELS`(111), `streamTps`(123) |
| 前端 API | `frontend/src/composables/useRouteLogs.ts` | `list`(19), `detail`(28) |
| 前端类型 | `frontend/src/lib/types.ts` | `RouteLog`(82) |
| 历史方案 | `docs/VISION-ROUTE-LOG-PLAN.md` | 视觉日志落库演进（v1-v4） |

---

## 附：一次含图 + 聚合 + 切换的完整日志示例

请求：`POST /v1/chat/completions`，`model=auto`，`messages` 含 2 张图，聚合目标 [deepseek-v4, glm-4.5]，deepseek 首次渠道失败后切换模型成功：

```json
{
  "request_id": "req_9f3a...",
  "requested_model": "auto",
  "virtual_model": "auto",
  "final_model": "glm-4.5",
  "started_at": "2026-08-21T07:00:00.123Z",
  "finished_at": "2026-08-21T07:00:12.456Z",
  "result": "success",
  "duration_ms": 12333,
  "prompt_tokens": 1500,
  "completion_tokens": 320,
  "attempts": [
    {
      "step_no": 1,
      "action": "视觉识别",
      "model": "qwen-vl-max",
      "channel_id": "chA",
      "result": "success",
      "duration_ms": 2100,
      "metadata": { "capability": "vision", "image_count": 2 }
    },
    {
      "step_no": 2,
      "action": "首次尝试",
      "model": "deepseek-v4",
      "channel_id": "chA",
      "result": "failed",
      "error_message": "upstream 502",
      "duration_ms": 3500
    },
    {
      "step_no": 3,
      "action": "切换模型",
      "model": "glm-4.5",
      "channel_id": "chB",
      "result": "success",
      "duration_ms": 6700
    }
  ]
}
```

时间线（UI 展开详情视图）：

```
1. 视觉识别  qwen-vl-max @ 渠道A  已成功  2.1 s
2. 首次尝试  deepseek-v4 @ 渠道A  失败    3.5 s   ← 502
3. 切换模型  glm-4.5      @ 渠道B  已成功  6.7 s
```
