# MCP 工具调用日志（落库 + 查询接口 + 前端 Tab）设计与实施计划

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 让「MCP 管理 → 工具调用」Tab 显示**每一次工具调用**的完整记录（工具名、输入/输出 JSON、时间、所属分组（单 MCP / 分组 / 聚合）、认证方式、耗时），数据源从「解析磁盘 SSE 日志文件」改为「查 `mcp_invocations` 数据库表」——该表已有自动写入，本次补齐字段、查询接口与前端展示。

**Architecture:** 后端在 `plugins/mcp-hub/stats.go` 的既有 `mcp_invocations` 表上 `ALTER TABLE` 追加 `input_json` / `output_json` / `auth_kind` 三列（幂等迁移，老数据保留、新列 NULL）；写入侧在统一埋点 `callEntry`/`recordInvocation` 处把入参、出参、认证方式一并落库；认证方式由 `/mcp/*` 的 `MCPKeyMiddleware` 和 admin 测试调用 `handleMCPToolCall` 注入到 request context，mcp-hub 从 ctx 读取。查询侧新增 `GET /api/mcp-invocations`（分页 + 多条件过滤）。前端把现有「日志」Tab 改名为「原始日志」，新增「工具调用」Tab（新组件 `McpInvocationsTab.vue`，查库渲染表格 + 输入/输出 JSON 展开）。

**Tech Stack:** Go（mcp-hub / gateway-keys / admin-api 现有分层）、SQLite（`mcp_invocations` 现有表）、Vue 3 SFC + shadcn-vue（Table/Badge/Select/Pagination）+ Tailwind 工具类 + `@remixicon/vue`。

---

## 决策记录（用户已拍板，实现时勿改）

| 维度 | 决策 | 说明 |
|---|---|---|
| 输入/输出存储 | **完整 JSON 入库** | 用户选「完整存 (Recommended)」。前端渲染时再按需截断显示；排查问题可看全量 |
| 认证字段 | **新增 `auth_kind` 列 + 写库** | 用户选「加 auth_kind 列 + 写库」。取值：`session`（管理后台测试调用）/ `mcp-key`（`/mcp/*` 带 key）/ `public`（`/mcp/*` 无 key 记录放行） |
| 表形态 | **改造现有 `mcp_invocations` 表** | 用户选「A: 改造 mcp_invocations」。表本身已是工具调用日志表，只缺字段 + 查询。老数据保留（新 3 列 NULL），`Stats()` 趋势/排行零影响 |
| 前端布局 | **原「日志」→ 改名「原始日志」；新增「工具调用」Tab** | 用户选「原为『原始日志』 + 新增『工具调用』」。磁盘 SSE 文件日志保留在「原始日志」；新 Tab 查库 |
| 索引 | **不新增** | 输入/输出列可能很大，只用于单条查看不参与聚合；现有 `idx_mcp_started` 已覆盖分页排序 |
| 写入位置 | 继续在统一埋点 `callEntry`（`service.go:425`） | 单 MCP / 分组 / `$smart` 聚合 / 技能调用全部经此，成功失败都记，无需改三类调用路径 |

### 边界写死（不做）

- **不做** 输入/输出内容截断入库（用户明确完整存；截断只在前端渲染层做）
- **不做** SSE 推送/WebSocket 实时刷新（沿用列表页惯例：分页 + 手动刷新；如需可后续加轮询）
- **不做** 磁盘原始日志删除/归档按钮（沿用现有行为，删 server 即清）
- **不做** 新增索引、全文搜索（分页 + 简单过滤已够）
- **不做** 改 `Stats()` 三个聚合查询（加列不破坏 SELECT 列表，`mcp_invocations` 行数不变）

---

## 背景事实（已核实，代码位置）

1. **表已存在 + 自动写入已生效**：`plugins/mcp-hub/stats.go:35-51` `mcpSchema` 建 `mcp_invocations`（id/started_at/finished_at/aggregate_kind/aggregate_target/tool_name/server_name/result/http_status/duration_ms/error_message + 3 索引）；`:54-57` `migrate(db)` 幂等建表，`plugin.go:39` Apply 时执行。**所有工具调用已经自动落库**（只缺 input/output/auth 三列）。
2. **统一埋点**：`plugins/mcp-hub/service.go:420-430` `callEntry(ctx, t, args, endpoint)` —— `$smart` invoke（`invokeWith:382`→`callEntry:404`）、单 MCP/分组端点直接调用（`exposedTools` handler → `callEntry`）、技能调用全走这里；`:425` `s.recordInvocation(startAt, startTime, endpoint, err, t.Name, t.Source)` 成功失败都异步写库。`invokeWith` 内还有两处**前置失败埋点**（`:390` 工具不可见、`:394` 同名冲突）不走 `callEntry`，同样需要带 input/output/auth。
3. **记录写入函数**：`stats.go:106-143` `recordInvocation(startAt, startTime, endpoint, err, toolName, serverName)` 异步 goroutine（`context.Background()`）→ `:75-95` `RecordInvocation` 同步 INSERT。**注意 `recordInvocation` 目前不接收 ctx**，读 auth_kind 需要加 ctx 参数或直接加 authKind 参数（推荐后者，改动最小）。
4. **认证现状**：
   - `/mcp/*` 全走 `MCPKeyMiddleware`（`core/servercore/server.go:123` `mux.Handle("/mcp/", keys.MCPKeyMiddleware(mcpHandler))`；实现 `plugins/gateway-keys/manager.go:302-334`）：该 endpoint 无 key 记录 → 直接放行（`public`）；有记录 → 校验 header（`mcp-key`）。**当前不注入任何 ctx 标记**，需加。
   - admin 测试调用 `handleMCPToolCall`（`plugins/admin-api/service.go:1571-1597`）走 `AuthSession`（`:136` 注册 `POST /api/mcp-tools/call`），复用生产路由 `s.hub.InvokeTool(ctx, ...)`（`:1591`），认证方式应为 `session`。
5. **InvokeTool 入口**：`plugins/mcp-hub/service.go:1288-1312` `InvokeTool(ctx, serverID, toolName, args)` → `:1311` `callEntry(ctx, ...)` 透传 ctx。admin 测试调用与网关调用共用。
6. **ToolResult 结构**：`core/mcpkit/mcpkit.go:48-53` `ToolResult{Content []ContentPart, IsError bool}`；`ContentPart` 含 `Type/Text`（`:45-47` 附近）。**output_json 存 `marshalJSON(res.Content)`**（截断逻辑在 `callEntryInner:449` 已按 `config.MaxToolResultChars` 截断，落库内容与业务返回一致）。input_json 存 `marshalJSON(args)`（`callEntry` 的 `args map[string]any`）。
7. **现有 JSON helper**：`plugins/mcp-hub/service.go:1178` `marshalJSON(v any) (string, error)`，可直接复用。
8. **路由注册点**：`plugins/admin-api/service.go:125-136` MCP 区块，`AuthSession` 鉴权。新端点 `GET /api/mcp-invocations` 加在 `:136` 之后（`/api/mcp-tools/call` 下面）。Go 1.22 `http.NewServeMux` 字面量优先通配符（`core/servercore/server.go:94-111`），无冲突。
9. **前端 tab 结构**：`frontend/src/components/mcp/McpPanel.vue:322-328` `TabsList` 四个 `TabsTrigger`（upstream / groups / endpoints / logs）；`:783-785` `<TabsContent value="logs"><McpLogsTab /></TabsContent>`。改动：`logs` 改名「原始日志」，新增 `invocations` Tab。
10. **现有日志 Tab 数据源（将替换）**：`frontend/src/components/mcp/McpLogsTab.vue:185` `GET /api/mcp-servers/logs` → `:208` `{name}/log/files` → `:232` `{name}/log?offset=` 读磁盘文件，`:88-109` `parseToolCall` 正则解析 SSE 帧，`:497` 空态「暂无工具调用记录」。**此组件改名「原始日志」后保留不动**（它的语义仍是原始文件查看）。
11. **前端类型**：`frontend/src/lib/types.ts` 加 `McpInvocation` 接口；API 封装在 `frontend/src/lib/api.ts`（或 composables 惯例 `useMcpManagement.ts`）。

---

## 数据模型（后端）

### `mcp_invocations` 表变更（`plugins/mcp-hub/stats.go`）

```sql
-- 幂等迁移：列存在则跳过（PRAGMA table_info 检查），不存在则 ALTER
ALTER TABLE mcp_invocations ADD COLUMN input_json  TEXT;  -- 调用参数 JSON（marshalJSON(args)）
ALTER TABLE mcp_invocations ADD COLUMN output_json TEXT;  -- 结果 Content JSON（marshalJSON(res.Content)）
ALTER TABLE mcp_invocations ADD COLUMN auth_kind   TEXT;  -- 'session' | 'mcp-key' | 'public'
```

- 迁移实现：`migrate(db)` 里在 `db.Exec(mcpSchema)` 之后加 `ensureColumns(db)` —— 对每列 `PRAGMA table_info(mcp_invocations)` 查 `name` 列表，缺失才 `ALTER TABLE`。老数据三列 NULL。
- **不**改 `mcpSchema` 常量里的 CREATE TABLE（老库不会重建；新库建表后再 ensureColumns 补列，两条路径都收敛）。

### Go 结构体扩展（`stats.go`）

```go
type InvocationRecord struct {
    // ...现有字段不变...
    InputJSON  string // 调用参数 JSON
    OutputJSON string // 结果 Content JSON
    AuthKind   string // 'session' | 'mcp-key' | 'public'
}
```

`RecordInvocation` 的 INSERT 加 3 列；`recordInvocation` 加 3 个参数（inputJSON, outputJSON, authKind string）。**签名改为显式传参而非读 ctx**（最小改动：调用点就 3 处，都拿得到值；避免给 goroutine 传 ctx 的生命周期问题——`recordInvocation` 内部用 `context.Background()` 写库，authKind 是快照值，无需 ctx）。

---

## 后端组件设计

### 1. `plugins/mcp-hub/stats.go`（schema + 写入）

- `migrate()`：`mcpSchema` 执行后调 `ensureColumns(db)`。
- `InvocationRecord` 加 3 字段；`RecordInvocation` INSERT 语句加 3 列（`NULLIF(?, '')` 语义：空串写 NULL，与现有列一致）。
- `recordInvocation(startAt, startTime, endpoint, err, toolName, serverName, inputJSON, outputJSON, authKind string)`：传给 `InvocationRecord`。

### 2. `plugins/mcp-hub/service.go`（埋点补参）

四个调用点全部补参（审计补充：`Invoke:373` 视图解析失败也是埋点，勿漏）：

| 位置 | inputJSON | outputJSON | authKind |
|---|---|---|---|
| `Invoke:373`（视图解析失败） | `marshalJSON(args)` | 空 | `authKindFrom(ctx)` |
| `invokeWith:390`（工具不可见） | `marshalJSON(args)` | 空 | `authKindFrom(ctx)` |
| `invokeWith:394`（同名冲突） | `marshalJSON(args)` | 空 | `authKindFrom(ctx)` |
| `callEntry:425`（统一埋点） | `marshalJSON(args)`（失败时 args 仍可用） | `marshalJSON(res.Content)` 仅在 `err == nil` 时取（失败时 `res` 为 nil，传空串） | `authKindFrom(ctx)` |

- `marshalJSON` 失败时降级为空串（不阻塞业务：埋点内错误只记日志）。
- 新增 helper：`func authKindFrom(ctx context.Context) string` —— 读 `plugin.AuthKindKey`（见下），缺失返回 `"public"`（默认值；`/mcp/*` 无 key 记录即 public，网关主路径无 key 时最合理）。

### 3. `core/plugin`（auth kind 注入契约）

在 `core/plugin/plugin.go`（已有 `AuthSession`/`AuthSkKey`/`AuthMCPHeader`/`AuthPublic` 常量，`:58-61`；当前仅 import `net/http`，需补 `context` import）加：

```go
// AuthKindKey 是 request context 中标记「本次请求的认证方式」的 key。
// MCPKeyMiddleware / admin 测试调用注入，mcp-hub 读取用于工具调用埋点。
type authKindCtxKey struct{}
var AuthKindKey = authKindCtxKey{}

func WithAuthKind(ctx context.Context, kind string) context.Context {
    return context.WithValue(ctx, AuthKindKey, kind)
}
func AuthKindFrom(ctx context.Context) string {
    if v, ok := ctx.Value(AuthKindKey).(string); ok {
        return v
    }
    return "public"
}
```

（放 `core/plugin` 避免 mcp-hub ↔ gateway-keys 互相依赖；两者都已依赖 core/plugin。）

### 4. `plugins/gateway-keys/manager.go`（middleware 注入）

`MCPKeyMiddleware`（`:302-334`）两处放行点注入：

- `:318-321` 无 key 记录 → `next.ServeHTTP(w, r.WithContext(plugin.WithAuthKind(r.Context(), "public")))`
- `:332` 校验通过 → `next.ServeHTTP(w, r.WithContext(plugin.WithAuthKind(r.Context(), "mcp-key")))`

### 5. `plugins/admin-api/service.go`（测试调用注入 + 新查询端点）

- `handleMCPToolCall:1591` 改为 `s.hub.InvokeTool(plugin.WithAuthKind(ctx, "session"), ...)`。
- 路由表 `:136` 后加：`{Method: http.MethodGet, Pattern: "GET /api/mcp-invocations", Auth: plugin.AuthSession, Handler: s.session(s.handleMCPInvocationsList)}`。
- 新 handler：解析 query 参数 → 调 `s.hub.ListInvocations(ctx, q)` → `writeJSON`。

### 6. `plugins/mcp-hub/stats.go`（查询）

```go
type InvocationQuery struct {
    Kind     string // aggregate_kind: single / group / $smart / 空=全部
    Tool     string // tool_name LIKE %tool%
    Server   string // server_name 精确（或 LIKE）
    Auth     string // auth_kind 精确
    From, To string // started_at 范围（RFC3339）
    Page, Size int   // 分页；Page>=1，Size 默认 20 上限 100
}
type InvocationPage struct {
    Items []InvocationRecord `json:"items"`
    Total int                `json:"total"`
}
func (s *Service) ListInvocations(ctx context.Context, q InvocationQuery) (*InvocationPage, error)
```

- WHERE 动态拼接（仅非空条件），参数占位符 `?` 按序；`ORDER BY started_at DESC, id DESC LIMIT ? OFFSET ?`。
- `Total` 用同 WHERE 的 `COUNT(*)`。
- 排序稳定性：`started_at` 可能同秒，补 `id DESC`。

### 7. admin-api handler `handleMCPInvocationsList`

```go
// GET /api/mcp-invocations?kind=&tool=&server=&auth=&from=&to=&page=&page_size=
// 响应: {"items":[...],"total":N}
```

- query 解析：`page` 默认 1，`page_size` 默认 20 上限 100；非法值回 400（或取默认，按项目惯例 writeError 400）。
- `tool` 支持 LIKE 转义（`%`/`_` 转义，简单处理即可，计划内按「LIKE + 转义」实现）。
- 鉴权沿用 `AuthSession`（session 中间件已挂）。

---

## 前端组件设计

### `McpPanel.vue`（改动，`frontend/src/components/mcp/McpPanel.vue`）

- `:327` `<TabsTrigger value="logs">日志</TabsTrigger>` → `日志` 改名 `原始日志`；其前加 `<TabsTrigger value="invocations">工具调用</TabsTrigger>`。
- `TabsContent` 加：`<TabsContent value="invocations" class="space-y-4"><McpInvocationsTab /></TabsContent>`（`McpLogsTab` 保持 `logs` 不动；`McpInvocationsTab` 需在 `<script setup>` 显式 import，与 `McpLogsTab` 的现有 import 方式一致）。

### `McpInvocationsTab.vue`（新组件，`frontend/src/components/mcp/`）

- **顶部工具栏**：筛选行 —— `Select`（类型：全部/单 MCP/分组/聚合）+ `Input`（工具名关键字）+ `Select`（认证：全部/session/mcp-key/public）+ `Button`（刷新）+ 分页信息。
- **表格**（`colgroup + table-fixed` 定宽模式，参考 `ChannelTable.vue`）：

| 列 | 宽度 | 内容 |
|---|---|---|
| 时间 | w-[150px] | `started_at` 本地化（`toLocaleString`） |
| 类型 | w-[90px] | Badge tint：single=「单 MCP」蓝 / group=「分组」紫 / $smart=「聚合」橙；值来自 `aggregate_kind` |
| 名称 | w-[120px] | `aggregate_target`（端点名：MCP 名 / 分组名 / `$smart`），NULL 显示 `—` |
| 工具 | flex | `tool_name` + 次级 `server_name`（`text-muted-foreground text-xs`） |
| 认证 | w-[90px] | Badge：`auth_kind`（session/mcp-key/public），NULL 显示 `—` |
| 耗时 | w-[90px] | `duration_ms` + `ms`，右对齐 |
| 结果 | w-[90px] | Badge：success=绿 / error=红 / timeout=橙 / not_found=灰 / denied=红（tint 配色沿用项目规范 `bg-{c}-500/15 text-{c}-700 dark:text-{c}-300 border-{c}-500/20`） |

- **展开行**：点击行展开（`RowExpand` 或手动 `<tr>` 切换），显示 `input_json` / `output_json`（`<pre class="whitespace-pre-wrap break-all text-xs">`），超长内容 CSS 限高 + `overflow-auto`（`max-h-64`），不做截断（用户选完整存）。展开态用 chevron 图标（`RiArrowDownSLine`/`RiArrowRightSLine`）。
- **分页**：`Pagination`（shadcn 或复用现有分页组件模式，参考 `RouteLogsView.vue` 的翻页交互），`page`/`page_size`/`total` 驱动。
- **数据获取**：`onMounted` + 筛选变更/翻页时 `GET /api/mcp-invocations?...`，走 `@/lib/api` 的 `api<T>` / `request`（`credentials: 'same-origin'` 带会话 Cookie）。
- **空态**：`total === 0` → 居中「暂无工具调用记录」（原 497 行文案搬来）。
- **类型定义**（`frontend/src/lib/types.ts`）：

```ts
export interface McpInvocation {
  id: number
  started_at: string
  finished_at?: string | null
  aggregate_kind: string // 'single' | 'group' | '$smart'
  aggregate_target?: string | null
  tool_name: string
  server_name?: string | null
  result: string // 'success' | 'error' | 'not_found' | 'timeout' | 'denied'
  http_status?: number | null
  duration_ms: number
  error_message?: string | null
  input_json?: string | null
  output_json?: string | null
  auth_kind?: string | null // 'session' | 'mcp-key' | 'public'
}
```

---

## 实施步骤（按 commit 拆分，每步可独立回溯）

### Step 1 — core/plugin auth kind 契约

**Files:**
- Modify: `core/plugin/plugin.go`

- 加 `authKindCtxKey` / `AuthKindKey` / `WithAuthKind` / `AuthKindFrom`（见「3. core/plugin」代码）。
- 验证：`go build ./core/plugin/`。

### Step 2 — mcp-hub schema + 写入（`plugins/mcp-hub/stats.go`）

- `migrate()` 加 `ensureColumns(db)`（PRAGMA 检查 + ALTER）。
- `InvocationRecord` 加 `InputJSON/OutputJSON/AuthKind`；`RecordInvocation` INSERT 加 3 列。
- `recordInvocation` 签名加 3 参数。
- 测试：`stats_test.go`（如已有）或新增 —— 建临时 SQLite（`sql.Open("sqlite", ":memory:")` 或 t.TempDir 文件），`migrate` 后 `PRAGMA table_info` 断言 3 列存在；`RecordInvocation` 写入后 `SELECT` 回读断言字段完整；幂等：`migrate` 调两次不报错。
- 验证：`go test ./plugins/mcp-hub/ -run 'Migrate|RecordInvocation' -v` + `go build ./...`。

### Step 3 — mcp-hub 埋点补参 + 查询（`plugins/mcp-hub/service.go` + `stats.go`）

- `service.go` 三处埋点补 `marshalJSON(args)` / `marshalJSON(res.Content)` / `authKindFrom(ctx)`；`marshalJSON` 失败降级空串。
- `stats.go` 加 `InvocationQuery` / `InvocationPage` / `ListInvocations`（动态 WHERE + 分页 + COUNT）。
- 测试：`ListInvocations` —— 插入若干条不同 kind/tool/auth 的记录，按各筛选条件查回，断言条数与排序（desc）；分页 total 正确。
- 验证：`go test ./plugins/mcp-hub/ -v` + `go build ./...`。

### Step 4 — middleware 注入 + admin 端点（`plugins/gateway-keys/manager.go` + `plugins/admin-api/service.go`）

- `MCPKeyMiddleware` 两处放行点注入 auth kind（public / mcp-key）。
- admin-api：路由表加 `GET /api/mcp-invocations`；`handleMCPToolCall` 注入 `"session"`；新 handler `handleMCPInvocationsList`（query 解析 + 参数校验 + 转发 `hub.ListInvocations` + `writeJSON`）。
- 测试：`admin_api_test.go` 补用例（httptest + 会话：建 2 条记录 → 列表分页/过滤/401 无会话；`page`/`page_size` 非法值 400）。
- 验证：`go test ./plugins/gateway-keys/ ./plugins/admin-api/ -v` + `go build ./...`。

### Step 5 — 前端（`McpPanel.vue` + `McpInvocationsTab.vue` + `types.ts`）

- `types.ts` 加 `McpInvocation`。
- `McpPanel.vue`：改 tab 结构（invocations + 原始日志）。`McpLogsTab` 需在 `<script setup>` 显式 import（`McpPanel.vue:25` 已有该 import，新组件同样加 import 行）。
- 新组件 `McpInvocationsTab.vue`：筛选 + 表格 + 展开行 + 分页（复用 `DataPagination.vue`，参考 `RouteLogsView.vue` 的 useListLoader + 真分页交互）+ 空态。
- 验证：`vue-tsc -b --force` + `vite build` 通过。

### Step 6 — 端到端验证（真实环境，用户参与）

- 启动服务；分别触发：单 MCP 直接调用（如 `/mcp/github` 工具）、分组调用、`$smart` 聚合调用、管理后台「测试工具」调用。
- 检查：`GET /api/mcp-invocations` 返回各记录，`aggregate_kind` 正确区分三类，`auth_kind` 正确（`/mcp/*` 带/不带 key、admin 测试 = session），input/output JSON 完整、耗时正确。
- 失败场景：调用不存在的工具 → 记录 `result=not_found` + `error_message`；工具超时 → `result=timeout`。
- 前端「工具调用」Tab 展示 + 筛选 + 分页 + 展开行正常；「原始日志」Tab 行为不变。
- 回归：Overview 页 MCP 统计（趋势/排行）仍正常（`Stats()` 未改）。

---

## 风险与对策

| 风险 | 等级 | 对策 |
|---|---|---|
| `ALTER TABLE` 非幂等（重复迁移报错） | P1 | `ensureColumns` 用 `PRAGMA table_info` 查列名，存在即跳过；`migrate` 测试覆盖幂等（调两次） |
| 埋点路径的 `marshalJSON` 失败拖慢请求 | P1 | 失败降级空串，只记日志；埋点本身在 `recordInvocation` 的 goroutine 内，不阻塞业务（现有行为） |
| 失败调用 `res == nil` 解引用 panic | P1 | `callEntry:425` 只在 `err == nil` 时取 `res.Content`，失败传空串（计划内已写死） |
| 认证注入漏路径（如 `InvokeTool` 被别处调用） | P2 | `AuthKindFrom` 缺省返回 `"public"`，任何未注入路径都有合理默认值，不 panic |
| input/output 体积大拖慢 SQLite 写 | P2 | 写入在异步 goroutine；SQLite 单行 TEXT 无硬上限；`idx_mcp_started` 索引不包含新列，写入成本增量有限；后续若需可加截断（本轮不做） |
| 前端表格大列表卡顿 | P2 | 分页（默认 20/页）+ 展开行懒渲染（只有展开才渲染 JSON `<pre>`） |
| `McpLogsTab` 改名后语义混淆 | P2 | 组件文件不改名（`McpLogsTab.vue`），只改 `McpPanel.vue` 里的 TabsTrigger 文案为「原始日志」；组件内部文案「暂无工具调用记录」如需可改为「暂无原始日志」 |

---

## 验收标准

1. `mcp_invocations` 表在重启后（老库已有数据）自动补 3 列且不报错；老行三列 NULL；`migrate` 幂等。
2. 每次工具调用（单 MCP / 分组 / `$smart` / 技能 / admin 测试）自动落库完整 input_json、output_json（成功时）、auth_kind、duration_ms；失败调用 output_json 为空、error_message 有值。
3. `GET /api/mcp-invocations` 支持分页（page/page_size）与 kind/tool/server/auth/from/to 过滤，按时间倒序；无会话返回 401；非法分页参数 400。
4. 前端「MCP 管理」有 5 个 Tab（上游 MCP / 分组 MCP / 连接端点配置 / 工具调用 / 原始日志）；「工具调用」展示表格（时间/类型/名称/工具/认证/耗时/结果），点击行展开显示输入输出 JSON，筛选与分页生效，空态文案正确；「原始日志」行为与之前完全一致。
5. `vue-tsc -b --force` 与 `vite build` 通过；`go test ./plugins/mcp-hub/ ./plugins/gateway-keys/ ./plugins/admin-api/ ./core/plugin/` 全绿。
6. Overview 页 MCP 统计（趋势 / 排行）回归正常。
