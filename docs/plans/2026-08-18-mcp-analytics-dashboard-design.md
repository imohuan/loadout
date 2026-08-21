# MCP + 模型 统计分析面板 — 设计文档

> 日期: 2026-08-18（v2: 纳入模型面板）
> 范围: 改造 OverviewView，新增两块统计面板
> 1. MCP 调用统计（趋势图 + 聚合服务 Top5 + 工具 Top5）
> 2. 模型使用情况（指标卡 + 命中率 + 次级指标 + 成功率条 + 趋势 + 分布 + 日历热力图）
> 积分数据本地缺失 → 用 Token 聚合代替

## 1. 背景

`mcp-hub` 插件是 Loadout 的 MCP 聚合网关，对外只暴露 `status / get / invoke` 三个工具，支持单 MCP / 分组 / `$smart` 三种路由方式。但**完全没有调用埋点**。

`route-log` 现有 SQLite 数据库覆盖 `model-gateway`（模型转发）请求，字段包含 `requested_model / final_model / final_channel_id / prompt_tokens / completion_tokens / cached_tokens / result / duration_ms / started_at / http_status` —— **模型面板的数据源现成，无需埋点**。

本次设计新增：

- **后端**: `mcp-hub` 内嵌埋点 → 新增 `mcp_invocations` 表（MCP 维度）
- **后端**: `admin-api` 新增 `GET /api/stats/mcp`（MCP 聚合）+ `GET /api/stats/models`（模型聚合，查 route-log）
- **前端**: 改造 `OverviewView`，嵌入 `McpStatsPanel`（MCP 三件套）与 `ModelStatsPanel`（模型使用情况）

## 2. 数据流

```
┌──────────────────────────┐   ┌──────────────────────────┐
│ mcp-hub 插件 (改造)       │   │ model-gateway (已有)      │
│ • Invoke 路径末尾写一行   │   │ route-log 落库(已有)      │
│ • 异步/失败不阻断          │   │                          │
└───────────┬──────────────┘   └───────────┬──────────────┘
            │ INSERT mcp_invocations       │ route_requests 表
            ▼                              ▼
┌──────────────────────────────────────────────────────┐
│ SQLite (core/db 共享 *sql.DB)                         │
│ mcp_invocations 表 (新) + route_requests 表 (已有)     │
└───────────┬──────────────────────────────┬───────────┘
            │                              │
admin-api 注入 mcp-hub / route-log         │
            ▼                              ▼
GET /api/stats/mcp                 GET /api/stats/models
(MCP 三件套)                       (模型使用情况全套)
            │                              │
            └──────────────┬───────────────┘
                           ▼
        OverviewView
         ├── McpStatsPanel    (趋势图 + 双 Top5)
         └── ModelStatsPanel  (全套模型使用情况)
```

## 3. 数据库 schema（MCP 维度，新）

复用以 `core/db` 注入的 `*sql.DB`，migration 写在 `mcp-hub` 初始化里：

```sql
CREATE TABLE IF NOT EXISTS mcp_invocations (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  started_at        TEXT    NOT NULL,  -- RFC3339Nano UTC
  finished_at       TEXT,
  aggregate_kind    TEXT    NOT NULL,  -- 'single' | 'group' | '$smart'
  aggregate_target  TEXT,              -- 单 MCP 名 / 分组名 / NULL(smart)
  tool_name         TEXT    NOT NULL,
  server_name       TEXT,
  result            TEXT    NOT NULL,  -- 'success' | 'error' | 'not_found' | 'timeout' | 'denied'
  http_status       INTEGER,
  duration_ms       INTEGER NOT NULL,
  error_message     TEXT
);
CREATE INDEX IF NOT EXISTS idx_mcp_started   ON mcp_invocations(started_at);
CREATE INDEX IF NOT EXISTS idx_mcp_tool      ON mcp_invocations(tool_name, started_at);
CREATE INDEX IF NOT EXISTS idx_mcp_aggregate ON mcp_invocations(aggregate_target, started_at);
```

**取舍**：
- ~~`request_id`~~ — 不保留，避免与 route-log 强行 JOIN
- `result` 枚举 5 种状态，异常维度有数
- 一条 `/mcp/invoke` = 一行，不展开路由尝试（路由细节留给 route-log）

## 4. 模型维度数据（route-log 已有，直接聚合）

`route_requests` 表（model-gateway 落库）字段够用：

| 图2/图3 组件 | 数据来源（route_requests） |
|---|---|
| 消耗积分卡 | **无积分字段** → 用 `total_tokens` 代替 |
| 输入卡 | `SUM(prompt_tokens)` |
| 输出卡 | `SUM(completion_tokens)` |
| 总 Token 卡 | `SUM(prompt_tokens + completion_tokens)` |
| 请求数量卡 | `COUNT(*)` |
| 命中率统计（环形图） | `cached_tokens / (prompt_tokens + completion_tokens)` → 总命中率；输入命中率 `cached_tokens / prompt_tokens`；输出命中率 0（无缓存语义） |
| 次级卡：总请求数 | `COUNT(*)` |
| 次级卡：总 Token | `SUM(prompt + completion)` |
| 次级卡：总消耗 | 无积分 → 同总 Token |
| 次级卡：成功率 | `result='success' / COUNT(*)` |
| 次级卡：平均耗时 | `AVG(duration_ms)` |
| 成功/失败进度条 | `COUNT(*) GROUP BY result`（success vs 非 success） |
| 积分消耗日历（热力图） | 无积分 → **Token 消耗日历热力图**（按天 SUM tokens） |
| 每日消耗趋势（折线） | 按天 `SUM(prompt+completion)` + `COUNT(*)` |
| 模型消耗分布（环形+列表） | `GROUP BY final_model`：`SUM tokens` + `COUNT(*)` + `SUM(prompt)` + `SUM(completion)` + `SUM(cached)` |

**注意**：`route_log` 的 `Clear()` 是全量清空（before 参数废弃），所以 model 面板统计会随清空日志归零——这是可接受行为（与模型日志页面一致）。

## 5. stats 接口

### 5.1 GET /api/stats/mcp（MCP 维度）

```
GET /api/stats/mcp?days=30&top=5
```

```json
{
  "trend": [ { "date": "2026-07-20", "count": 18 } ],
  "rank_aggregates": [ { "kind": "single", "target": "DEMO", "calls": 268 } ],
  "rank_tools": [ { "tool_name": "read_file", "server_name": "fs", "calls": 82 } ]
}
```

- `trend[]` 30 天整天返回，0 天 `count:0`
- `$smart` → `target=null`，前端按 `kind==='$smart'` 判断
- `rank_tools[].server_name` 同名工具多 server 时选 calls 最大的（GROUP BY tool_name, server_name 后取首条）

### 5.2 GET /api/stats/models（模型维度，查 route-log）

```
GET /api/stats/models?days=30
```

```json
{
  "summary": {
    "requests": 47008,
    "prompt_tokens": 12345678,
    "completion_tokens": 2345678,
    "cached_tokens": 5123456,
    "total_tokens": 14691356,
    "success_rate": 0.997,
    "avg_duration_ms": 234.5,
    "failed": 124
  },
  "hit_rate": {
    "input": 0.415,
    "output": 0,
    "total": 0.349
  },
  "trend": [
    { "date": "2026-07-20", "requests": 120, "prompt_tokens": 300000, "completion_tokens": 50000, "cached_tokens": 120000, "total_tokens": 350000 }
  ],
  "calendar": [
    { "date": "2026-07-20", "tokens": 350000 }
  ],
  "model_dist": [
    { "model": "deepseek-v4-pro", "calls": 32000, "tokens": 8000000, "prompt_tokens": 6000000, "completion_tokens": 2000000, "cached_tokens": 3000000 }
  ]
}
```

**说明**：
- `summary.avg_duration_ms` 只统计非 running 的完成请求
- `hit_rate.total = cached / (prompt+completion)`；`hit_rate.input = cached / prompt`；`output = 0`
- `trend[]` 与 `calendar[]` 数据同源（按天聚合 tokens），前端一处渲染折线、一处渲染热力图
- 查询失败（route-log 未装配）返回 503 + 前端 `<EmptyState>`

## 6. 前端契约 (TypeScript)

```ts
// ---- MCP 维度 ----
export interface McpTrendPoint { date: string; count: number }
export interface McpAggregateRank { kind: 'single' | 'group' | '$smart'; target: string | null; calls: number }
export interface McpToolRank { tool_name: string; server_name: string; calls: number }
export interface McpStats {
  trend: McpTrendPoint[]
  rank_aggregates: McpAggregateRank[]
  rank_tools: McpToolRank[]
}

// ---- 模型维度 ----
export interface ModelSummary {
  requests: number
  prompt_tokens: number
  completion_tokens: number
  cached_tokens: number
  total_tokens: number
  success_rate: number
  avg_duration_ms: number
  failed: number
}
export interface ModelHitRate { input: number; output: number; total: number }
export interface ModelTrendPoint {
  date: string
  requests: number
  prompt_tokens: number
  completion_tokens: number
  cached_tokens: number
  total_tokens: number
}
export interface ModelCalendarPoint { date: string; tokens: number }
export interface ModelDistPoint {
  model: string
  calls: number
  tokens: number
  prompt_tokens: number
  completion_tokens: number
  cached_tokens: number
}
export interface ModelStats {
  summary: ModelSummary
  hit_rate: ModelHitRate
  trend: ModelTrendPoint[]
  calendar: ModelCalendarPoint[]
  model_dist: ModelDistPoint[]
}
```

### 组件树

```
OverviewView.vue (改造)
├── PageHeader (已有)
├── StatCardsRow (4 张已有卡片保留)
├── McpStatsPanel.vue (新)
│   ├── TrendChart.vue (ECharts 面积图 - MCP 调用趋势)
│   ├── AggregateRank.vue (Top 5)
│   └── ToolRank.vue (Top 5)
├── ModelStatsPanel.vue (新)
│   ├── ModelSummaryCards.vue (5 卡)
│   ├── ModelHitRateCard.vue (环形图 + 明细)
│   ├── ModelSecondaryCards.vue (5 次级卡)
│   ├── ModelSuccessBar.vue (成功率进度条)
│   ├── ModelTrendChart.vue (折线)
│   ├── ModelCalendar.vue (热力图)
│   └── ModelDistChart.vue (环形 + 列表)
└── 原有 2 列"开始配置" + "运行说明" (保留)
```

### 依赖

```bash
pnpm add echarts vue-echarts
```

### Remixicon 复用

- `RiLineChartLine` — MCP 趋势 / 模型趋势标题
- `RiFlowChart` — 聚合排行标题
- `RiToolsLine` — 工具排行标题
- `RiCpuLine` / `RiRobot2Line` — 模型面板标题

## 7. 实施步骤 (顺序敏感)

1. **后端 schema 迁移 + 埋点**：`mcp-hub` 注入 db，写 migration，Invoke 加挂载点
2. **后端 stats-mcp 路由**：`admin-api` 新增 `/api/stats/mcp`，3 个并发查询
3. **后端 stats-models 路由**：`admin-api` 新增 `/api/stats/models`，查 route-log（5 个聚合查询）
4. **后端本地验证**：curl 打几次 `/mcp/invoke` + 一次模型转发，确认两接口返回正确
5. **前端装包**：echarts + vue-echarts 按需引入
6. **前端 MCP 面板**：McpStatsPanel → TrendChart → AggregateRank → ToolRank
7. **前端模型面板**：ModelStatsPanel 及 7 个子组件
8. **前端集成**：OverviewView 嵌入两个 Panel，失败态用 `<EmptyState>`
9. **联调**：Go 启动 + 前端 pnpm dev，造流量对账

工作量估算: **16-20 小时**（含模型面板全套）。

## 8. 二期保留 (不进 MVP)

- 积分/费用真实数据（需 NewAPI 同步或本地单价配置）
- 时间范围切换器（今日/7/30/总计）
- 自动刷新
- "$smart 排行展开 / 查看全部"
- MCP 面板的 5 张静态指标卡（今日调用 / 异常 / 聚合服务数 / Key 数 / 上游服务数）

## 9. 已确认决策

| 决策 | 选择 |
|---|---|
| 面板入口 | 改造现有 OverviewView |
| 第一版范围 | MCP 三件套 + 模型面板全套（图2+图3） |
| MCP 数据来源 | 后端新埋点 + mcp-hub 内嵌 stats |
| 模型数据来源 | route-log 已有数据，直接聚合（无新埋点） |
| 后端路径 | mcp-hub 内嵌埋点 + admin-api 两个 stats 路由 |
| 存储介质 | 复用 SQLite：mcp_invocations 新表 + route_requests 已有表 |
| request_id 字段 | 不保留 |
| result 枚举 | 5 种状态 (success/error/not_found/timeout/denied) |
| 默认时间窗口 | 30 天 |
| $smart 表示 | NULL 行 |
| 排行上限 | MVP 只看 Top 5 |
| 积分字段 | 本地无积分 → 用 Token 聚合代替；积分卡/日历改为 Token 语义 |
| 执行模式 | Subagent-Driven（当前会话） |
