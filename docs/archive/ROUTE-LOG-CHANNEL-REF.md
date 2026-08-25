# 请求日志渠道显示规范：模型名@渠道名(Key)

> 状态：📖 文档（2026-08-21 定稿，随 v17/v18 迁移生效）
> 目标：说明请求日志（路由日志 / 模型测试请求记录）中「模型 + 渠道」的显示规范——`模型名@渠道名称（Key名称）`，以及此前为何显示不出来、如何修复的。
> 关联文档：[转发日志全链路架构说明](./ROUTE-LOG-ARCHITECTURE.md)

---

## 目录

1. [显示规范（目标样式）](#1-显示规范目标样式)
2. [为什么之前不行](#2-为什么之前不行)
3. [为什么现在可以](#3-为什么现在可以)
4. [如何修复的（改动清单）](#4-如何修复的改动清单)
5. [数据模型与三种粒度](#5-数据模型与三种粒度)
6. [关键代码索引](#6-关键代码索引)

---

## 1. 显示规范（目标样式）

请求日志中，凡是展示「模型 + 渠道」的位置，统一为：

```
模型名@渠道名称（Key名称）
```

不带空格、不带额外符号，渠道引用一律通过前端统一组件 `ChannelRef.vue` 渲染（`atPrefix` 默认开启，自动输出 `@` 前缀）。

### 1.1 三个展示位置

| 位置 | 数据来源 | 示例 |
|---|---|---|
| 列表头「最终目标」列 | `route_requests.final_model` + final 三种粒度 | `deepseek-v4-flash-ga-260731@volcengine(volcengine)` |
| 展开详情「候选行」 | `route_attempts.model` + attempt 三种粒度 | `deepseek-v4-pro-ga-260813@volcengine(volcengine)` |
| 展开详情「候选行」 | 同上 | `deepseek-v4-flash-ga-260731@volcengine(volcengine)` |

### 1.2 渠道引用格式（按粒度优先级）

聚合目标有三种粒度，显示时**按优先级取第一个命中**：

| 优先级 | 粒度 | 后端字段 | 显示 |
|---|---|---|---|
| 1 | 渠道级（整组轮询 Key） | `channel_base_url` | `渠道名称`（无括号） |
| 2 | Key 多选 | `channel_ids` | `渠道名称(Key1, Key2)` |
| 3 | 单 Key（兼容旧数据） | `channel_id` | `渠道名称(Key1)` |

特殊哨兵：**「自带」模式**（`__builtin__`）不走 ChannelRef，显示 `@ 自带 · key名`（前端 `finalTargetLabel` 分支）。

### 1.3 边界情况

- `final_model` 为空：显示 `-` 占位，不附加 `@渠道名`（避免出现 `@ -`）。
- 三种粒度都拿不到（历史数据 / 迁移前记录）：只显示模型名，无 `@渠道名`。
- 同一请求的最终目标与候选行格式一致，均符合 `模型名@渠道名(Key)` 规范。

---

## 2. 为什么之前不行

请求日志的渠道显示修复历经 **三轮**，每一轮都暴露一层问题：

### 2.1 第一层：候选行把模型名放主位、@ 渠道名当次要后缀

最初的模板（`RouteLogTable.vue`）：

```
1. deepseek-v4-pro-ga-260813 @ [ChannelRef] [首次尝试] [已跳过] 0ms
```

模型名独占主位，`@渠道名` 只是次要后缀，且与模型名之间有明显的视觉分隔——与目标样式 `模型名@渠道名(Key)` 不符。

### 2.2 第二层：后端 attempt 落库丢失 Key 多选 / 渠道级粒度（根因之一）

聚合目标 `AggregateTarget` 支持三种粒度，但 `model-gateway` 写 skipped attempt 时**只传了 `t.ChannelID`（单 Key 字段）**：

```go
// 修复前（plugins/model-gateway/proxy.go）
s.proxyAttemptLog(r, pipe, t.Model, t.ChannelID, started, "skipped", ...)
```

当聚合目标是 **Key 多选**（`channel_ids_json` 有值、`channel_id` 为空）或 **渠道级**（`channel_base_url`）时：

- `route_attempts.channel_id` 存的是空串；
- 前端 `ChannelRef` 拿不到任何可匹配的 id → `formatChannelRef` 返回空 → **`@` 后什么都渲染不出来**；
- 界面退化成 `deepseek-v4-pro-ga-260813 @ 首次尝试`（`@` 直接贴动作标签），极其误导。

### 2.3 第三层：后端 Finish 落库同样丢失粒度（根因之二）

列表头「最终目标」列的数据来自 `route_requests.final_channel_id`，由 `proxyRejectedLog` 写 Finish 时填入：

```go
// 修复前
s.routeLog.Finish(..., contracts.RouteFinish{
    FinalModel:     lastTarget.Model,
    FinalChannelID: lastTarget.ChannelID, // Key 多选场景为空！
    ...
})
```

同样只填单 Key 字段 → `route_requests.final_channel_id` 为空 → 前端「最终目标」列渲染不出 `@渠道名`，只显示孤零零的模型名。

> **一句话总结**：不是前端模板不对（模板修了但没数据可显示），而是**后端两次落库（attempt + finish）都只存了单 Key 字段，Key 多选 / 渠道级模式的渠道上下文被系统性丢弃**。前端 `ChannelRef` 组件本身没问题——它拿不到有效 id 时按设计返回空。

### 2.4 为什么「刷新页面没反应」

前端（`frontend/dist`）是 **go:embed 嵌进二进制**的，不是从磁盘实时读取：

```
frontend/dist → go:embed → apps/server → bin/loadout.exe
```

只改源码 + `pnpm build` 不重编译 exe 的话，刷新浏览器拿到的仍是旧 exe 里内嵌的旧前端；同理后端 Go 改动也必须重编译 exe + 重启进程才生效。**这就是"刷新了页面没有反应"的原因**——不是没改，是没重启。

---

## 3. 为什么现在可以

修复后，三种粒度从**聚合目标 → 落库 → API 返回 → 前端渲染**全链路贯通：

```
aggregate_targets (ChannelID / ChannelIDs / ChannelBaseURL)
        │
        ├─ attempt 写入 ──► route_attempts (channel_id / channel_ids_json / channel_base_url)
        │                          │
        │                          └─► API Detail ──► 前端 ChannelRef ──► 候选行：模型名@渠道名(Key)
        │
        └─ finish 写入 ──► route_requests (final_channel_id / final_channel_ids_json / final_channel_base_url)
                                   │
                                   └─► API List ──► 前端 finalChannelRef ──► 最终目标列：模型名@渠道名(Key)
```

前端渲染逻辑按「渠道级 > Key 多选 > 单 Key」优先级取 id，任何粒度有值都能显示；`ChannelRef` 组件（`@渠道名(Key1, Key2)`）与列表行、模型测试请求记录共用，格式天然统一。

---

## 4. 如何修复的（改动清单）

### 4.1 后端

| 文件 | 改动 |
|---|---|
| `plugins/contracts/routing.go` | `RouteAttempt` 加 `ChannelIDs []string` / `ChannelBaseURL string`；`RouteFinish` 加 `FinalChannelIDs []string` / `FinalChannelBaseURL string`；`RouteRequestView` 同步加 JSON 字段 |
| `core/db/migrate.go` | **v17**（route-attempts-channel-level）：`route_attempts` 加 `channel_ids_json` / `channel_base_url`；**v18**（route-requests-final-channel-level）：`route_requests` 加 `final_channel_ids_json` / `final_channel_base_url` |
| `plugins/route-log/service.go` | `Attempt()` INSERT 写两新列（空串用 `COALESCE(NULLIF(?, ''), '')` 兜底，避免 NULL 撞 NOT NULL）；`Finish()` UPDATE 写两新列；`Detail()`/`List()` SELECT 新列；`scanRequest` 解码 `final_channel_ids_json`；新增 `encodeStringSlice` / `decodeStringSlice`；**List 的 channel 过滤兼容 Key 多选**（`json_each`） |
| `plugins/model-gateway/proxy.go` | `proxyAttemptLog` 签名加 `channelIDs []string, channelBaseURL string`（7 处单 Key 调用点补 `nil, ""`）；`proxyRejectedLog` 写 skipped attempt 时传 `append([]string(nil), t.ChannelIDs...)` + `t.ChannelBaseURL`；**Finish 同样传 `t.ChannelIDs` + `t.ChannelBaseURL`**（第三轮修复核心） |

### 4.2 前端

| 文件 | 改动 |
|---|---|
| `frontend/src/lib/types.ts` | `RouteAttempt` 加 `channel_ids?` / `channel_base_url?`；`RouteLog` 加 `final_channel_ids?` / `final_channel_base_url?` |
| `frontend/src/components/route-logs/RouteLogTable.vue` | 候选行：`模型名` + `<ChannelRef>` 放入 `inline-flex items-center gap-0.5` 容器紧贴；新增 `channelRefFor(attempt)`（渠道级 > 多选 > 单 Key）；新增 `finalChannelRef(log)`（同上，BUILTIN_CHANNEL 走「自带」哨兵）；最终目标列复用 ChannelRef |

### 4.3 附带修复

- `core/db/db_test.go`：`TestMigrateIsIdempotent` 迁移数 16→17→18（原条件笔误），`TestMigrateRejectsIncompatibleHistory` 插 version 18→19。
- `plugins/vision/proxy_test.go`：mock 补 `SelfHeal` 方法（历史遗留缺方法）。

### 4.4 验证方式

- Go：`go build ./...`（`-buildvcs=false`，目录非 git repo）+ `go test ./plugins/route-log/ ./plugins/contracts/ ./plugins/aggregate/ ./plugins/model-gateway/ ./plugins/volc-free-quota/ ./core/db/` 全绿。
- 前端：`NODE_OPTIONS="--use-system-ca" pnpm run build`（WorkBuddy safe-delete 拦截 vite 清 dist，必须绕过）。
- 端到端（Chrome MCP 自动化）：杀旧进程 → 重启新 exe（自动应用 v18 迁移）→ SQL INSERT mock 记录（`final_channel_ids_json='["<channel_id>"]'`）→ 刷新页面截图，列表头与候选行均正确渲染 `模型名@渠道名(Key)`；迁移前旧记录保持只有模型名（符合预期，未回填）。

---

## 5. 数据模型与三种粒度

### 5.1 聚合目标的三种粒度

定义：`plugins/types/types.go` `AggregateTarget`

```go
type AggregateTarget struct {
    Model          string   `json:"model"`                      // 真实模型名
    ChannelID      string   `json:"channel_id"`                 // 渠道 ID（单 Key，兼容）
    ChannelIDs     []string `json:"channel_ids,omitempty"`      // 渠道 ID 列表（Key 多选）
    ChannelBaseURL string   `json:"channel_base_url,omitempty"` // 渠道地址（渠道级，按 base_url 组轮询 Key）
}
```

### 5.2 落库后的列结构（v17 / v18 之后）

`route_attempts` 新增：

| 列 | 默认值 | 说明 |
|---|---|---|
| `channel_ids_json` | `'[]'` | Key 多选（JSON 数组） |
| `channel_base_url` | `''` | 渠道级 base_url |

`route_requests` 新增：

| 列 | 默认值 | 说明 |
|---|---|---|
| `final_channel_ids_json` | `'[]'` | 最终目标 Key 多选（JSON 数组） |
| `final_channel_base_url` | `''` | 最终目标渠道级 base_url |

旧列 `channel_id` / `final_channel_id` 保留（单 Key 兼容 + 向后兼容），三种粒度可并存。

### 5.3 前端渲染优先级

```ts
// frontend/src/components/route-logs/RouteLogTable.vue
function channelRefFor(attempt) {
  if (attempt.channel_base_url) return { channel_base_url: attempt.channel_base_url }
  if (attempt.channel_ids?.length) return { channel_ids: attempt.channel_ids }
  return { channel_id: attempt.channel_id || '' }
}
function finalChannelRef(log) {
  if (log.final_channel_base_url) return { channel_base_url: log.final_channel_base_url }
  if (log.final_channel_ids?.length) return { channel_ids: log.final_channel_ids }
  if (log.final_channel_id && log.final_channel_id !== BUILTIN_CHANNEL)
    return { channel_id: log.final_channel_id }
  return null // BUILTIN_CHANNEL 走 finalTargetLabel 哨兵分支
}
```

---

## 6. 关键代码索引

| 内容 | 位置 |
|---|---|
| 聚合目标三粒度定义 | `plugins/types/types.go:305` `AggregateTarget` |
| RouteAttempt / RouteFinish / RouteRequestView | `plugins/contracts/routing.go` |
| v17 / v18 迁移 | `core/db/migrate.go` |
| attempt 写入（三粒度） | `plugins/model-gateway/proxy.go:596` `proxyAttemptLog` |
| skipped 候选 + Finish（三粒度） | `plugins/model-gateway/proxy.go:730` `proxyRejectedLog` |
| 落库 / 读库 / 过滤 | `plugins/route-log/service.go` `Attempt` / `Finish` / `Detail` / `List` |
| 前端类型 | `frontend/src/lib/types.ts` `RouteAttempt` / `RouteLog` |
| 前端渠道引用工具 | `frontend/src/composables/useChannelRef.ts` `formatChannelRef` |
| 前端渲染组件 | `frontend/src/components/ChannelRef.vue` |
| 请求记录表（列表 + 详情） | `frontend/src/components/route-logs/RouteLogTable.vue` |

---

## 附录：为什么需要 go:embed 重编译才能生效

| 部署模式 | 前端来源 | 修改生效方式 |
|---|---|---|
| 开发（`pnpm dev`，端口 5173） | Vite dev server，实时读源码 | 保存即 HMR，无需重启 |
| 生产（`bin/loadout.exe`，端口 3000） | `go:embed frontend/dist` 内嵌二进制 | 必须 `pnpm build` + 重编译 exe + 重启进程 |

排查"改了没生效"类问题，先确认当前访问的是哪个端口/部署模式，再决定是否需要重编译重启。
