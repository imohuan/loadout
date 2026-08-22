# 视觉模型执行日志落库实施计划

> 状态：✅ 已实施（2026-08-20）
> 目标：把「视觉模型（图片识别）执行」也写入 route-log，使前端路由日志页能看到一次含图请求的完整时间线（视觉识别 → 主模型转发），而不再只有主模型的日志。

---

## 0. 实施记录（2026-08-20）

三阶段已全部落地，测试全绿：

- **阶段一（request_id 统一）**：`core/servercore/server.go` 中间件优先客户端 `X-Request-Id`、空则生成并写回请求头；`plugins/model-gateway/proxy.go` 兜底生成提前到 before-upstream hook 之前，保证 hook 内 `pipe.RequestID` 恒非空。
- **阶段二（视觉日志落库）**：
  - `plugins/contracts/routing.go` 新增 `VisionAttemptLog` 与 `MetadataKeyVisionAttempt` 键。
  - `plugins/vision/proxy.go` 视觉识别成功/失败后把结果暂存到 `pipe.Metadata`（纯旧图场景不记录）。
  - `plugins/vision/service.go` `Describe`/`callVision` 返回值增加成功渠道 id（缓存命中/失败为空）。
  - `plugins/model-gateway/proxy.go` 新增 `flushVisionAttempt`，在 `proxyBeginLog`（成功路径）与 `proxyRejectedLog`（视觉失败路径）的 Start 之后统一落库，`step_no=-1`、`action=视觉识别`，不与主链路 1..N 冲突。
- **阶段三（前端）**：`frontend/src/components/route-logs/RouteLogTable.vue` 的 `ACTION_LABELS` 增加「视觉识别」（teal 色）。

新增测试：`plugins/vision/proxy_test.go` `TestHandleProxyBeforeUpstreamStoresVisionLog` / `TestAppendVisionLogNilMetadata`；`plugins/vision/proxy_vision_e2e_test.go` `TestVisionE2EFlushOnSuccess` / `TestVisionE2EFlushOnFail`；`plugins/model-gateway/proxy_vision_log_test.go` `TestHandleProxyFlushVisionAttempts`（成功）/ `TestHandleProxyFlushVisionAttemptsFailed`（失败拒绝路径）。

**实现说明（与计划的差异）**：
1. vision 插件无需注入 `route-log` 服务——落库统一由 model-gateway 从 `pipe.Metadata` flush，vision 侧只暂存，插件零新增依赖。
2. 视觉 attempt 的 `StartedAt` 取该候选尝试的单独起点（每次循环开始），`Duration` 为该候选耗时。

**v2 增补（2026-08-20，生产反馈后）**：
- 原实现只记最后一条视觉 attempt，多 via_option 时失败的中间尝试被吞掉（用户日志：doubao 失败被吞，只看到 qwen3 成功）。改为**切片暂存**：每次失败/成功都 append 一条。
- 契约：`MetadataKeyVisionAttempt` 改名 `MetadataKeyVisionAttempts`（复数），载荷类型改为 `[]VisionAttemptLog`。
- step_no 公式：切片位置 idx ∈ [0, n)，`step_no = -(n - idx)`。先尝试的候选 step_no 更小（如 n=2 时 -2, -1），`ORDER BY step_no ASC` 时按尝试顺序在前，主链路 1..N 在后。

**v3 重构（2026-08-20，用户反馈日志时机）**：
- 用户反馈：视觉识别完成（阻塞数十秒）后日志才出现，识别期间 UI 看不到。要求**访问时写占位、响应结束后更新状态**。
- 重构为**两阶段直写**（废弃 v2 的 Metadata 暂存 + model-gateway flush 机制）：
  - `route-log.Start` 的 UPSERT 增加 `requested_model`/`virtual_model` 更新，支持「hook 前占位 + hook 后补全虚拟模型」两次调用合并为一条。
  - model-gateway `HandleProxy` 在 before-upstream hook **之前**调 `proxyBeginLog` 写占位 running（UI 识别期间即可轮询到）；删除 `flushVisionAttempts`。
  - vision 插件**注入 route-log**（`SetRouteLog`），`HandleProxyBeforeUpstream` 识别开始写 `running` 占位 attempt（step_no=-(n-idx)），识别结束以同一 step_no 更新 `success`/`failed`。
  - contracts 删除 `VisionAttemptLog` / `MetadataKeyVisionAttempts`（v2 机制废弃）。

**v4 改造（2026-08-20，用户反馈序号 & 详情刷新）**：
- 序号：v3 用负数 step_no 是为了"独立空间"防混淆，但用户视角应该是 1, 2, 3, 4, 5, 6, 7 连续递增。改为**单调递增正数空间**：
  - 视觉 attempt `step_no = idx + 1`，循环结束把视觉最后 step 写入 `pipe.Metadata["__route_step"]`。
  - 主链路 `proxyAttemptLog` 从 `__route_step + 1` 续接，**action 判断改用独立的 `__main_route_step` 计数器**（避免视觉 step 占用导致首次尝试被误判为「切换渠道」）。
- 前端详情重试：`RouteLogsView.refreshActiveDetails` 之前只刷 running，终态不再刷详情——导致详情接口首次延迟/失败时 UI 长期不完整。增加 `shouldRefreshDetail` + `detailRetryCount` Map：终态 + attempts 空 → 最多重试 5 次，成功后清零。

---

## 1. 问题与根因

### 1.1 现象

用户访问 OpenAI 兼容接口（`/v1/chat/completions` 等），一次请求里带图片时，后端会先调视觉模型识别图片、再请求主模型。但 route-log（前端「路由日志」页数据源）里**只有主模型的记录**，视觉模型的执行完全缺失。

### 1.2 根因

视觉识别请求**完全绕过了 model-gateway 主代理链路，也绕过了 route-log**：

```
HandleProxy（model-gateway/proxy.go:26）
  ├─ sniffRequest 提取 model/stream
  ├─ Waterfall(ProxyBeforeUpstream)  ← vision 插件在这里跑
  │    └─ vision.HandleProxyBeforeUpstream（vision/proxy.go:200）
  │         ├─ detectProxyImages 检出图片
  │         ├─ DecideRoute 查能力路由
  │         └─ Describe → callVision（vision/service.go:317）
  │              └─ 直接 http.Client.Do 调渠道 /chat/completions  ← 不走 proxyForward
  ├─ proxyBeginLog → routeLog.Start（写 route_requests，此时才建 request_id 记录）
  └─ proxyForward → proxyAttemptLog / proxyFinishLog（写 route_attempts）
```

三个导致缺失的直接原因：

1. **视觉调用不走 `proxyForward`**：`callVision` 是独立 HTTP 请求（`service.go:358` 的 `http.Client`），不触发 `proxyAttemptLog` / `proxyFinishLog`，route-log 自然无记录。
2. **vision 插件没注入 route-log**：`vision/plugin.go:36` 的 `Manifest.Inject` 只有 `store/db/logger`，对比 `model-gateway/plugin.go:32` 注入了 `route-log`。视觉插件根本拿不到日志服务。
3. **现有视觉日志全是 slog 进程日志**：`"视觉请求发出"`、`"视觉渠道成功"`、`"视觉描述完成"`（`service.go:363/407/142`）只进进程日志，不进 SQLite 的 route-log 表，前端看不到。

---

## 2. 方案审核：落地前的 4 个坑

在直接「给 vision 注入 route-log、写一条 Attempt」之前，有 4 个问题必须一并解决，否则要么做不成、要么数据会乱。

### 坑 1：request_id 存在两套（必须先统一）

当前 request_id 有两套、不一致：

| 来源 | 代码 | 用途 |
|---|---|---|
| 中间件生成 | `core/servercore/server.go:203` `id := newRequestID()`，**只写响应头** | 访问日志（slog "请求"/"请求结束"） |
| model-gateway 生成 | `model-gateway/proxy.go:39` 读 `r.Header.Get("X-Request-Id")`，空则 `proxy.go:81` 兜底 `newRequestID()` | route-log 数据库 |

**后果**：同一次请求，进程访问日志的 `request_id` 与 route-log 表的 `request_id` 对不上。且 vision 在 hook 内（`proxy.go:66` Waterfall）执行时，`pipe.RequestID` 还是客户端传的原始值，**可能为空**——视觉日志拿不到稳定 id 来关联。

**结论**：request_id 统一是视觉日志落库的前置条件（见 3.1）。

### 坑 2：视觉 attempt 的写入时机（route_requests 尚未 Start）

关键执行顺序（`proxy.go:26-92`）：

```
1. sniffRequest → pipe
2. Waterfall(ProxyBeforeUpstream) → vision 识别图片（此时 route_requests 还没 Start）
3. pipe = rewritten
4. proxyBeginLog → routeLog.Start（这才建立 route_requests 记录）
5. proxyForward → attempt / finish
```

视觉识别在**步骤 2**，而 `route_requests` 的记录在**步骤 4** 才建立。视觉 attempt 如果直接在 hook 内写 `route_attempts`，会先于父记录 `route_requests` 出现（语义上先有子再有父）。

**结论**：视觉执行结果需在 hook 内**暂存**，待 `routeLog.Start` 之后由 model-gateway 统一 flush（见 3.2），不能直接在 hook 里写库。

### 坑 3：step_no 唯一性与 action 判断冲突

`route_attempts` 表约束 `ON CONFLICT(request_id, step_no)`（`route-log/service.go:53`），主链路用 `pipe.Metadata["__route_step"]` 从 1 递增（`proxy.go:520-522`）。且 `action` 判断依赖 step 计数（`proxy.go:523-529`）：

```go
action := "首次尝试"
if step > 1 { action = "切换渠道" }
if pipe.Metadata["__virtual_model"] != nil && step > 1 { action = "切换模型" }
```

若视觉识别与主链路**共用同一计数器**，视觉先占 step=1，主链路首次尝试变 step=2，`action` 会误显示为「切换渠道」。

**结论**：视觉 attempt 必须用**独立于主链路的 step 空间**（推荐负数，见 3.2），且 `action` 固定为「视觉识别」，不参与主链路的首次/切换判断。

### 坑 4：前端 action 标签缺失

`frontend/src/components/route-logs/RouteLogTable.vue:31` 的 `ACTION_LABELS` 只定义了「首次尝试 / 切换渠道 / 切换模型」。不加的话，`actionLabel` 会 fallback 显示原文（`action || '-'`），能显示但无颜色 badge。

**结论**：前端需补一个「视觉识别」的 label + tone（见 3.3）。

---

## 3. 实施方案

### 3.1 阶段一：request_id 统一（前置，独立可交付）

**目标**：访问日志、route-log、视觉日志、响应头使用同一个 request_id，且 hook 内 `pipe.RequestID` 恒非空。

**改动 1：中间件贯通 id**（`core/servercore/server.go:201` `requestIDMiddleware`）

```go
// 现状
id := newRequestID()
w.Header().Set("X-Request-Id", id)
// 改为：客户端传了就用客户端的（保留重试合并语义），否则生成并写回请求头
id := r.Header.Get("X-Request-Id")
if id == "" {
    id = newRequestID()
    r.Header.Set("X-Request-Id", id)
}
w.Header().Set("X-Request-Id", id)
```

**改动 2：model-gateway 删除兜底生成**（`model-gateway/proxy.go:81-82`）

```go
// 删掉这两行；pipe.RequestID 已从 header 读到非空值
if pipe.RequestID == "" { pipe.RequestID = newRequestID() }
```

**必须保留**：客户端 `X-Request-Id` 优先语义——`route-log/service.go:29` 的 `Start` 是 `ON CONFLICT(request_id)` UPSERT，客户端 SDK 重试复用同一 `X-Request-Id` 时会把一次业务请求合并成一条日志，这是有意设计（`service_test.go:56` `TestRouteLogRetryMergesSameRequestID` 盯着）。所以优先级是「客户端传了用客户端 → 没传用中间件生成」。

**收益**：视觉识别在 hook 内即可拿到稳定非空的 `pipe.RequestID` 用于关联。

### 3.2 阶段二：视觉日志落库（核心）

**改动 1：vision 插件注入 route-log**（`vision/plugin.go`）

```go
// Manifest.Inject 增加 "route-log"
Inject: []string{"store", "db", "logger", "route-log"},
// Apply 中：
routeLog, ok := ctx.Get("route-log").(contracts.RouteLog)
if !ok { return fmt.Errorf("vision: missing route-log service") }
svc.SetRouteLog(routeLog)
```

`Service` 增加 `routeLog contracts.RouteLog` 字段。

**改动 2：视觉执行结果暂存到 pipe.Metadata**（`vision/proxy.go` `HandleProxyBeforeUpstream`）

由于视觉识别在 hook 内、`route_requests` 尚未 Start（坑 2），视觉执行结果**不直接写库**，而是暂存到 `pipe.Metadata`，由 model-gateway 在 `Start` 之后统一 flush：

```go
// 视觉识别成功/失败后，暂存结果（在 proxy.go 的 options 循环结束后）
pipe.Metadata["__vision_attempt"] = map[string]any{
    "via_model":    viaModel,       // 实际使用的视觉模型
    "channel_id":   ch.ID,          // 实际成功的渠道（失败为空）
    "duration_ms":  ...,
    "result":       "success"/"failed",
    "error":        "...",          // 失败时
    "image_count":  len(images),
}
```

> 需要把 `Describe`/`callVision` 的签名补上 `requestID string`，并在 `callVision` 内部把「实际命中的 channel」透传出来（当前 `callVision` 只返回 text，成功渠道 id 在函数内部没往外传）。

**改动 3：model-gateway 在 Start 之后 flush 视觉 attempt**（`model-gateway/proxy.go` `proxyBeginLog`）

在 `proxyBeginLog` 里 `routeLog.Start(...)` 成功后，检测 `pipe.Metadata["__vision_attempt"]`，存在则写一条 route-log Attempt：

```go
if v, ok := pipe.Metadata["__vision_attempt"].(map[string]any); ok {
    s.routeLog.Attempt(ctx, contracts.RouteAttempt{
        RequestID:   pipe.RequestID,
        StepNo:      -1,              // 独立负数空间，避开主链路 1..N（坑 3）
        Action:      "视觉识别",
        Model:       v["via_model"].(string),
        ChannelID:   v["channel_id"].(string),
        Result:      v["result"].(string),
        ErrorMessage: v["error"].(string),
        Duration:    ..., 
    })
}
```

**关于 step_no 负数**：主链路 step 从 1 递增，视觉用 `-1`（若视觉 failover 要逐条记录，用 `-1,-2,...`）。后端 `Detail/List` 按 `step_no ASC` 排序时，视觉识别自然排在时间线最前，符合真实执行顺序；且不影响 `proxyAttemptLog` 的 `action` 判断（主链路 step 仍从 1 开始）。

### 3.3 阶段三：前端展示

**改动**（`RouteLogTable.vue:31` `ACTION_LABELS` 增加一项）：

```ts
视觉识别: {
  label: '视觉识别',
  tone: 'bg-teal-500/15 text-teal-700 dark:text-teal-300 border-teal-500/20',
},
```

前端对负数 `step_no` 的展示：时间线里 `attempt.step_no` 会显示 `-1.`，可接受（action badge 已说明是「视觉识别」），如需优化可对「视觉识别」action 隐藏 step 数字，作为可选项。

---

## 4. 改动文件清单

| 文件 | 改动 |
|---|---|
| `core/servercore/server.go` | 中间件：优先客户端 `X-Request-Id`，空则生成并写回 `r.Header` + 响应头 |
| `plugins/model-gateway/proxy.go` | 删除兜底 `newRequestID`；`proxyBeginLog` 增加视觉 attempt flush |
| `plugins/vision/plugin.go` | `Manifest.Inject` 加 `route-log`；`Apply` 取 `contracts.RouteLog` |
| `plugins/vision/service.go` | `Service` 加 `routeLog` 字段；`Describe`/`callVision` 透传 `requestID` 与成功渠道 id |
| `plugins/vision/proxy.go` | `HandleProxyBeforeUpstream` 暂存视觉执行结果到 `pipe.Metadata` |
| `frontend/src/components/route-logs/RouteLogTable.vue` | `ACTION_LABELS` 加「视觉识别」 |

---

## 5. 决策点（待用户确认）

| # | 决策点 | 推荐 | 说明 |
|---|---|---|---|
| D1 | 视觉 failover 记录粒度 | **整段一条**（记最终成功渠道 + 失败次数） | via_options 通常只有 1 个候选；候选间 failover 细节已有 slog 覆盖，不必淹没主时间线 |
| D2 | 视觉 attempt 的 step_no | **负数空间（-1 起）** | 避免与主链路 1..N 冲突，排序自然靠前，不影响 action 判断 |
| D3 | 视觉缓存命中是否记 attempt | **记**（`result=success`，channel 空，可带 metadata 标记缓存命中） | 用户能看到「本次视觉走了缓存」，否则缓存命中时时间线又变空白 |
| D4 | 视觉请求的 token 统计 | **暂不补**（留空，前端显示 `-`） | `callVision` 当前不解析 usage，补统计属独立增强，不在本期范围 |

---

## 6. 测试计划

1. **request_id 统一**
   - 客户端不带 `X-Request-Id`：访问日志 `request_id` == route-log `request_id` == 响应头 `X-Request-Id`。
   - 客户端带 `X-Request-Id`：全程复用客户端值，重试两次合并为一条 route-log（回归 `TestRouteLogRetryMergesSameRequestID`）。

2. **视觉日志落库**
   - 含图请求命中 vision proxy 路由：`route_attempts` 出现一条 `action=视觉识别`、`model=viaModel`、`step_no=-1` 的记录，且 `request_id` 与主链路一致。
   - 视觉识别失败换候选：最终记录成功候选；全部失败时 `result=failed`，主链路仍正常（视觉失败不阻塞）。
   - 视觉缓存命中：仍记一条 attempt（按 D3）。

3. **主链路不受干扰**
   - 主链路 `route_attempts` 的 step_no 仍从 1 开始、`action` 仍为「首次尝试/切换渠道/切换模型」，不被视觉记录污染。

4. **前端**
   - 路由日志页展开含图请求，时间线第一条为「视觉识别」badge，其后是主模型尝试。

---

## 7. 风险与备注

- **耦合**：`__vision_attempt` metadata 键是 vision 与 model-gateway 的约定，需在代码注释中明确，避免后续插件误用。
- **旧数据兼容**：历史 route-log 无视觉记录，属预期，不迁移。
- **边界**：`vision/proxy.go:28` `visionFormatByPath` 只认 `chat/completions`、`messages`、`responses`。若用户实际访问的是老式 `/v1/completions`（无 chat），vision 不检测图片——需确认用户真实路径，必要时补 path 支持（独立于本计划）。
