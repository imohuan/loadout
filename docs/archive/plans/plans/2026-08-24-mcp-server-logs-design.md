# MCP 服务器完整日志（连接 + JSON-RPC 帧 + stdio stderr）设计与实现计划

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 为 mcp-hub 管理的三种 MCP 连接方式（stdio / http-streamable / sse）提供**完整的会话日志**：连接层（启动/失败/断开）+ JSON-RPC 帧（loadout 主动发出的请求与响应）+ stdio 子进程 stderr，实时写入 `~/.loadout/logs/mcp/<server-name>/<首次连接时间>.log`；提供 API 供前端「MCP 管理 → 日志」tab 以 1s 轮询增量拉取展示。

**Architecture:** 后端在 `core/mcpkit` 的 `Upstream` 上加可选日志 hook（stdio 补 stderr 管道捕获、connect/disconnect 埋点），在 `plugins/mcp-hub` 新增 `ServerLog`/`LogManager`（每个 server 一个追加文件 + 32MB 滚动 + 并发锁 + 敏感字段掩码），帧级日志在 `mcp-hub/service.go` 的 `callEntryInner`（`up.CallTool` 前后）埋点；admin-api 新增 3 个只读端点（全部 server 列表 + 单 server 段列表 + 增量读）。前端 `McpPanel.vue` 加「日志」tab + 新组件 `McpLogsTab.vue`（下拉选 server、`<pre>` 渲染、1s `?offset=` 增量轮询、跟随滚动）。

**Tech Stack:** Go（mcpkit / mcp-hub / admin-api 现有分层）、Vue 3 SFC + shadcn-vue（Select/Badge）+ Tailwind 工具类 + `@remixicon/vue`、SQLite `mcp_invocations` 不动。

---

## 决策记录（用户已拍板，实现时勿改）

| 维度 | 决策 | 说明 |
|---|---|---|
| 日志粒度 | **连接层 + JSON-RPC 帧（主动调用）+ stdio stderr** | 用户原话：「启动日志，启动失败，就是一个完整的连接日志，包括后续的 JSONRPC 通信」。stderr 单独确认要捕获 |
| 帧采集范围 | **只抓 loadout 主动发起的帧** | 用户选「只抓主动调用」：init / tools/list / tools/call 等 mcp-hub 主动发起的请求+响应。**不**抓 server 主动 push 的 notifications/progress（YAGNI，改动最小） |
| 文件分片 | **每个 server 一个会话文件** | 用户选「每个 server 一个会话文件」：stdio 一次启动 → 一个文件持续追加；HTTP/SSE 首次 connect 成功 → 同一文件持续追加；后续 stdio 重启 / HTTP 重连 / 失败重试**不新开文件**续写 |
| 文件命名 | `~/.loadout/logs/mcp/<server-name>/<首次连接时间YYYYMMDD-HHMMSS>.log` | `<server-name>` = mcp-hub server.name（如 `github`）；`<首次连接时间>` 取该 server **首次 connect 成功**的时间戳（本地时区） |
| stdio stderr | **单独捕获** | 用户选「捕获 stdio stderr」：`os.Pipe()` 自建管道 + goroutine 按行读出（`StderrPipe` 有进程退出竞态，弃用，见「1. core/mcpkit 增强」），写 `[STDERR]` 条目。启动失败根因（npx 拉包失败/env 缺失/server panic）从此可见 |
| 文件清理 | **单文件上限 32MB + 滚动保留** | 用户修正：不做总磁盘上限。单文件写满 **32MB**（用户「你觉得多好合适」→ 推荐值：UI 增量读取 + 尾部加载视角，普通会话几小时 <2MB，极少滚动）→ 滚动到新文件续写（`<首启时间>-2.log`、`-3.log`…）；**旧文件全部保留**，直到 server 删除时整目录清除 |
| server 删除联动 | 删除 server 时清理其整个日志目录 | 在 admin-api `handleMCPServerDelete` 里加 |
| 日志行格式 | 纯文本行：`ISO8601+08:00 [KIND] key=value ...` | UI `<pre>` 直接渲染；每行带时间戳与 KIND 前缀，可 grep 可解析 |
| 敏感信息 | headers / env 值做掩码 | 项目已有 `maskSecret` 惯例（unifyai service.go），避免 token 泄入日志文件 |
| 前端刷新 | 1s 轮询增量（`?offset=`） | 用户原话：「每过1s无感刷新一次」。SSE 推送不做（YAGNI，1s 轮询满足） |

### 边界写死（不做）

- **不做** server 主动 push 帧（notifications / progress / resources.update / sampling）——只抓主动调用
- **不做** HTTP/SSE 原始字节捕获（请求/响应 header body 原文）——帧层足够排障
- **不做** 日志搜索 / 过滤 / 高亮（tab 先只做展示 + 下拉）
- **不做** 日志清空 / 下载 / 归档按钮（删 server 即清；需要再说）
- **不做** 总磁盘上限轮转（用户拍板「滚动保留，删 server 时全清」，磁盘增长由单文件上限 + 删除 server 控制）
- **不做** 多文件历史会话回看的下拉级联（server 下拉 → 正文尾部加载 + 「加载更早」向上翻页覆盖历史段，不引入 server×文件二维下拉）
- **不做** 改动 `mcp_invocations` 表结构 / stats.go（元数据统计独立于新日志，互不干扰）
- **不做** 单元测试框架新增——沿用现有 `go test` 风格补测试（mcpkit 用 fake stdio server、admin-api 用 httptest）
- **不做** 日志脱敏之外的任何安全改造（鉴权沿用现有 `AuthSession` 会话中间件）

---

## 背景事实（已核实，代码位置）

1. **三种 transport 分发**：`core/mcpkit/mcpkit.go:212-237` `buildTransport()` 按 `u.cfg.Transport` switch：`stdio` → `exec.Command` + `mcp.CommandTransport`；`http` → `mcp.StreamableClientTransport`（Endpoint + `newHTTPClient(headers)`）；`sse` → `mcp.SSEClientTransport`（同 HTTPClient）。非法 transport 返回错误。
2. **stdio stderr 当前被丢弃**：`mcpkit.go:216` `cmd := exec.Command(...)` 未设置 `cmd.Stderr`（Go 默认 nil = 丢弃）；`u.cmd = cmd` 用于 `Alive()` 存活判断（`:138`）、建连失败时的进程回收（`:192-195`）与 Close 未建连时的 Kill（`:164-166`）。**捕获方案已核实 SDK 后定稿：自建 `os.Pipe()` 管道（`cmd.Stderr = pw`）+ reader goroutine**，`StderrPipe()` 有退出竞态弃用——理由见下方「1. core/mcpkit 增强」。
3. **http header 注入**：`mcpkit.go:248-270` `headerTransport.RoundTrip` 克隆请求并写固定头（headers 含鉴权头，**掩码点**）。
4. **帧埋点位置**：`plugins/mcp-hub/service.go:411-421` `callEntry()` 是**所有工具调用的统一入口**（$smart invoke、单 MCP/分组端点直接调用、技能调用全走这里，成功失败都埋点 `recordInvocation`）。实际执行体 `callEntryInner`（`:424-440`）调 `up.CallTool(ctx, t.RawName, args)`（`:434`，返回前 `:438` 已按 `config.MaxToolResultChars` 截断）—— **帧日志改在 `callEntryInner` 内 `up.CallTool` 前后各记一条**（request / response）。注意：SDK `ClientSession.CallTool` 不暴露底层 JSON-RPC id，日志行的 `id=` 用**按 server 递增的合成序号**（非真实 wire id）。
5. **元数据统计（不动）**：`plugins/mcp-hub/stats.go` `mcp_invocations` 表只记 tool/status/latency/error，**不含通信原文**。
6. **日志根目录**：`core/config/config.go:292` `LogsDir = filepath.Join(home, "logs")`（`HomeDir = "~/.loadout"`，`:39`，支持 `LOADOUT_HOME_DIR` 覆盖）。MCP 日志根 = `filepath.Join(config.LogsDir, "mcp")`。
7. **admin-api 路由注册**：`plugins/admin-api/service.go:124-129` `GET/POST/PUT /api/mcp-servers`、`PUT /api/mcp-servers/{id}`（`:127`）、`DELETE /api/mcp-servers`（`:128`）、`POST /api/mcp-servers/test`（`:129`），均 `AuthSession` 鉴权。**无 `GET /api/mcp-servers/{id}` 通配路由**；路由由 `core/servercore/server.go:94-111` 挂到单个 `http.NewServeMux()`（Go 1.22+ 方法 pattern，`r.PathValue` 取参），匹配规则**字面量优先于通配符**——新增 `GET /api/mcp-servers/logs` 与 `GET /api/mcp-servers/{name}/log`（及其 `/files`）均不与现有路由重叠，注册顺序无关。删除处理器 `handleMCPServerDelete`（`:1264`）——日志目录联动清理加在这（注意删除请求体只有 `id`，需先从读出的 server 列表取 `Name` 再清目录）。
8. **Upstream 接线**：`plugins/mcp-hub/service.go:642-660` `getUpstream()` 建 `mcpkit.NewUpstream(UpstreamConfig{...})`；`Connect`（`:717`）/`StartEnabled`（`:723`）/`Close`（`:779`）——日志 hook 通过 UpstreamConfig 传入。
9. **前端 tab 结构**：`frontend/src/components/mcp/McpPanel.vue:32` `activeTab = ref('upstream')`；`:321-326` `TabsList` 三个 `TabsTrigger`（upstream / groups / endpoints）。**新 tab `logs` 加在这**。
10. **server 列表数据源**：`frontend/src/composables/useMcpManagement.ts:8-28` `McpServer` 接口（含 transport/command/args/url/headers/env/enabled/status/error）、`:49` `servers` ref；`@/lib/api` 的 `request` 通用请求封装。

---

## 数据模型（后端新增，纯文件，不动 SQLite）

### 日志文件布局

```
~/.loadout/logs/mcp/
├── github/
│   ├── 20260824-144325.log        # server "github"，首启 2026-08-24 14:43:25（当前活跃段）
│   ├── 20260824-144325-2.log      # 写满 32MB 滚动出来的第 2 段（历史）
│   └── 20260824-144325-3.log      # 第 3 段（历史）
└── <server-name>/...
```

- 文件名时间戳 = 该 server **首次 connect 成功**时间（本地时区），滚动段追加 `-N`（N 从 2 起）。
- 段文件按名排序 = 时间序；`-N` 越大越新。删 server 时整个目录删除。
- **文件创建时机（首连失败的关键补丁）**：文件在**第一次日志写入时**惰性创建（首个事件几乎总是 CONNECT 尝试行），base 时间戳 = 首条日志行的时间。含义：
  - 首连即成功 → base ≈ 首次 connect 成功时间，与决策表语义一致；
  - 首连失败（只有 CONNECT-FAIL）→ 用该失败尝试的时间作 base，**失败日志不会丢**（否则「启动失败根因」落不了盘，与目标矛盾）；之后某次重连成功**不重命名**，续写同一文件。
  - 若坚持「文件名必须等于首次 connect 成功时间」，需在首成功前把 CONNECT-FAIL 先缓存/改名——复杂度高收益低，**实现前请主线程确认取舍**（默认按首写时间）。
- **幂等续写**：进程重启 / server 停用再启用后，`Ensure` 找到目录里已存在的旧段文件即继续追加（base 冻结），不新建文件——与决策表「后续重启/重连不新开文件」一致。

### 日志行格式（每条一行，UTF-8）

```
2026-08-24T14:43:25.123+08:00 [CONNECT]      transport=stdio cmd="npx" args=["-y","codegraph"] env={"LOG_LEVEL":"info"} pid=12345
2026-08-24T14:43:25.456+08:00 [CONNECT-OK]   handshake=ok protocol=2025-06-18 caps={"tools":{"listChanged":true}}
2026-08-24T14:43:26.789+08:00 [FRAME→]       id=1 method=initialize params={"protocolVersion":"2025-06-18","clientInfo":{"name":"loadout","version":"0.7.0"}}
2026-08-24T14:43:26.792+08:00 [FRAME←]       id=1 duration_ms=3 result={"serverInfo":{"name":"codegraph"},"capabilities":{...}}
2026-08-24T14:43:30.001+08:00 [FRAME→]       id=2 method=tools/call params={"name":"search","arguments":{"query":"mcp"}}
2026-08-24T14:43:30.234+08:00 [FRAME←]       id=2 duration_ms=233 result={"content":[{"type":"text","text":"..."}]}
2026-08-24T14:43:31.000+08:00 [STDERR]       Error: EACCES: permission denied, open '/etc/config.json'
2026-08-24T15:00:00.000+08:00 [DISCONNECT]   reason=process_exit code=1
2026-08-24T15:00:01.000+08:00 [CONNECT-FAIL] err="dial tcp 127.0.0.1:8080: connect: connection refused"
```

**KIND 集合**：

| KIND | 触发点 | 关键字段 |
|---|---|---|
| `CONNECT` | Connect 尝试（每次重试/重连都记） | transport, cmd/args/env(stdio) 或 url(sse/http), pid；headers/env 值掩码 |
| `CONNECT-OK` | Connect 成功（**此时间戳 = 文件名时间戳**） | handshake=ok, protocol, caps |
| `CONNECT-FAIL` | Connect 失败 | err（错误全文） |
| `FRAME→` | loadout 发出 JSON-RPC 请求（`callEntryInner` 内 `CallTool` 前） | id（**合成序号**，见背景事实 #4）, method, params（JSON 压缩；**params 同样按 `MaxToolResultChars` 封顶**，超限加 `truncated=true`） |
| `FRAME←` | 收到响应（`CallTool` 后） | id, duration_ms, result 或 error；`callEntryInner:438` 已在返回前截断，日志记**截断后**内容，`truncated=true` 由截断点（截断前后长度比较）判定 |
| `STDERR` | stdio stderr 每行 | 原文 |
| `DISCONNECT` | 显式 Close / stdio 进程退出 | reason（process_exit / closed / disabled）, code。**HTTP/SSE 只能记显式 Close**：SDK 的 SSE 监听流后台自动重连（失败退避重试），重连失败不回调，mcpkit 观测不到「连接丢失」——只会在下一次请求上表现为 FRAME← 的 error |

**掩码规则**：`CONNECT` 行的 env/headers 值统一按项目 `maskSecret` 惯例处理（如 `GITHUB_TOKEN=***`），不落明文。注意：`maskSecret`（`plugins/unifyai/service.go:95`）是 **unifyai 包内未导出函数**（≤8 位返回 `****`，否则保留首 4 + `****` + 尾 4），mcp-hub 需**自带一份同语义实现**（Step 2 已列）。该掩码只做部分遮蔽，Authorization 等 header 值的明文主体仍会泄露尾部 4 位——沿用项目惯例，不做深度扫描（YAGNI，风险表已注明）。

---

## 后端组件设计

### 1. `core/mcpkit` 增强（最小侵入）

`UpstreamConfig` 新增可选字段：

```go
// LogHook 收到会话事件时回调（kind ∈ connect/connect_ok/connect_fail/disconnect/stderr）。
// mcp-hub 注入 ServerLog 写入器；nil 表示不采集（现有调用方零影响）。
LogHook func(kind string, fields ...any)
```

- `buildTransport()` stdio 分支：stderr 捕获。**SDK 已核实**（go-sdk v1.7.0 `mcp/cmd.go`）：`CommandTransport.Connect` 只用 `StdoutPipe()` + `StdinPipe()` + `cmd.Start()`，**从不设置 `cmd.Stderr`**；进程回收在 `pipeRWC.Close()`（关 stdin → Wait(5s) → SIGTERM → SIGKILL）。因此 stderr 空间完全开放，三种方案对比：
  - **A `cmd.Stderr = io.MultiWriter(os.Stderr, lineSink)`**：os/exec 内部起拷贝 goroutine，`Wait()` 等它排空 → **无尾部丢失**；但 `io.Copy` 无 EOF 回调，**末行无换行符时无法 flush**（sink 留缓冲等后续字节，极端情况丢末尾半行）。
  - **B（推荐）`os.Pipe()` 自建管道**：`pr, pw := os.Pipe(); cmd.Stderr = pw`（*os.File 被 os/exec 直通子进程，不归它管理，`Wait()` 不关也不等）→ reader goroutine 读 `pr` 得到 **EOF 即进程退出**，可 flush 尾部半行、可顺带触发 `DISCONNECT(process_exit)`；需在 `client.Connect` 返回后（成功/失败分支都要）`Close(pw)`，否则父进程持有写端读不到 EOF。reader 同时 tee 到 `os.Stderr` 保持可见性。
  - ~~C `cmd.StderrPipe()`~~：SDK 的 `Wait()` 在进程退出后立即关闭 pipe 读端，**存在尾部丢失竞态**（丢的恰好是「启动失败根因」），弃用。
  - reader goroutine 每行 → `LogHook("stderr", "line", line)`；EOF 时若 session 已建立且未 Close → `LogHook("disconnect", "reason", "process_exit")`（exit code 尽力取 `ProcessState.ExitCode()`，未 Wait 时可能拿不到则省略）。
- `Connect()`：入口 `LogHook("connect", ...)`、成功 `LogHook("connect_ok", ...)`、失败 `LogHook("connect_fail", "err", err)`。注意 `ensureSession` 全程持 `u.mu`，LogHook 内不得反向调用 Upstream 方法（死锁）；mcpkit 侧维护 `disconnected` 防重标记，Close 与 stderr EOF 只记一次 `disconnect`。
- `Close()`：`LogHook("disconnect", "reason", "closed", ...)`（先标 `disconnected=true` 防 stderr EOF 重复记）。
- `CallTool` 帧不在 mcpkit 记（统一在 mcp-hub `callEntryInner` 记，避免双记——与现有 `recordInvocation` 单点埋点原则一致）。

### 2. `plugins/mcp-hub/server_logs.go`（新文件）

```go
// ServerLog 一个 server 的会话日志（追加写；单文件达 maxSize 滚动到 -N 新文件）。
type ServerLog struct {
    mu      sync.Mutex
    f       *os.File
    path    string      // 当前活跃段路径
    seq     int         // 当前段序号（1=无后缀）
    size    int64       // 当前活跃段已写字节
    base    string      // 目录 + 首启时间戳前缀（不含 -N 后缀）
    maxSize int64       // 默认 32MB
}

// LogManager 管理全部 server 的日志：map[serverID]*ServerLog + 根目录。
type LogManager struct {
    mu      sync.Mutex
    logs    map[string]*ServerLog
    root    string // filepath.Join(config.LogsDir, "mcp")
}
```

**并发与生命周期（两把锁，职责分开）**：
- `LogManager.mu` **只保护 map** 的增删查（Ensure/Get/Remove 时短暂持有），不参与写文件；
- `ServerLog.mu` 串行化该 server 的 `Write` 与 `roll`（滚动判定 + 换句柄全程在锁内；`roll()` 幂等：打开新段前再检查一次 `size > maxSize`）；
- **`Remove(serverID)` 与并发写互斥**：Remove 先持 `LogManager.mu` 从 map 摘除（阻止新写入）→ 再持该 `ServerLog.mu`（等已在写的那次完成）→ 关句柄 → 解锁 → `os.RemoveAll(目录)`。这样「server 删除时正在写」不会出现写已关句柄/边删边写的场景；若删除时恰有 API Read 打开旧段文件，Windows 下 Go 的 `os.Open` 默认带 `FILE_SHARE_READ|WRITE|DELETE`，`RemoveAll` 仍可成功，但实现仍应先关写句柄再删（文档已有此要求）。
- `Ensure(server)` 幂等：目录/文件存在则续开（**找最新段**：`<base>.log` 或 `<base>-N.log` 中 N 最大者），Remove 之后可被再次 Ensure 重建。
- **写入必须快**：`Write` 持 `ServerLog.mu` 期间做一次文件写；mcpkit 的 stderr 拷贝/读取 goroutine 与 `os/exec` 的 `Wait()` 会等写入方结束，写日志不得做任何慢操作（如再次锁 LogManager.mu 查表后再写——先查表取 *ServerLog 再单独锁它）。

- `Ensure(server)`：目录不存在则 `MkdirAll`；文件存在则续开（**找最新段**：`<base>.log` 或 `<base>-N.log` 中 N 最大者）。
- `Write(serverID, kind, fields ...any)`：拼行后 `mu` 互斥写；写前检查 `size+len > maxSize` → `roll()`（关旧句柄、`seq++`、开 `<base>-<seq>.log`、重置 size）。
- `Roll()` 竞态：多 goroutine 并发触发滚动由 `mu` 串行化；滚动后旧句柄 Close（Windows 下删文件前必须先 Close，滚动场景文件保留无需删，天然安全）。
- `Close(serverID)`：server 停用/删除时关句柄（防泄漏）。
- `ListFiles(serverID) []LogFileInfo`：列出该 server 全部段（name/size/first_ts/last_ts），供 UI 段列表与「加载更早」。
- `Read(serverID, segment, offset, limit)`：`os.Open(段文件)` + `Seek`，从 offset 读 limit 字节（append-only，读不锁写）。
- `Remove(serverID)`：关句柄 + 删整个目录（server 删除联动）。

mcp-hub `service.go` 接线：

- `NewService` 内初始化 `LogManager`（root 用 `config.LogsDir`）；`Close()` 里遍历 Close 全部。
- `getUpstream()` 传 `UpstreamConfig{LogHook: ...}` → 回调里 `s.logMgr.Write(srv.ID, kind, fields...)`。**注意 `getUpstream` 全程持 `s.mu`（`:642-660`）**，LogHook 回调（即 `logMgr.Write`）不得再获取 `s.mu` 或反向调用 hub 方法，否则死锁；`Write` 按 serverID 定位，段目录按 server **name** 建——Ensure 需要 server 全量信息，hook 闭包捕获 `srv` 即可。
- 帧埋点改在 `callEntryInner`（`:424-440`）内、`up.CallTool`（`:434`）前后各记 `FRAME→` / `FRAME←`：用 `t.ServerID` 定位日志文件，`id` 用**按 server 递增的合成序号**（存 LogManager 或 ServerLog 内，非真实 wire id，SDK 不暴露）；`params` 与 `result` 都按 `MaxToolResultChars` 封顶，`truncated=true` 在截断点判定；`IsSkill` 分支不记帧（技能无上游通信，直接 return，`:425-429`）；`up == nil` 分支（`:431-433`）无帧可记，报错即可。
- `handleMCPServerDelete`（admin-api `:1264`）删除前调 **mcp-hub `Service` 新暴露的方法 `RemoveServerLogs(serverName)`**（admin-api 不持有 LogManager，经 `s.hub` 调用；删除请求体只有 `id`，需先从读出的列表取 `Name`）。

### 3. admin-api 新增端点（`plugins/admin-api/service.go` 注册 + 新 handler）

| 方法 | 路径 | 说明 | 响应 |
|---|---|---|---|
| GET | `/api/mcp-servers/logs` | 列出全部有日志文件的 server（含每 server 段列表） | `{items: [{name, transport, files: [{name, size, first_ts, last_ts}]}]}` |
| GET | `/api/mcp-servers/{name}/log/files` | 列出该 server 全部段文件 | `{items: [{name, size, first_ts, last_ts, active}]}` |
| GET | `/api/mcp-servers/{name}/log` | 增量读指定段 | `?file=<段名，默认最新段>&offset=N&limit=65536` → `{name, file, offset, size, eof, content}` |

- **路由冲突已核实无风险**：现有注册表 `service.go:124-129` 只有 `PUT /api/mcp-servers/{id}` 一个通配路由（method 不同），**无 `GET /api/mcp-servers/{id}`**；框架用 Go 1.22 `http.NewServeMux`（servercore/server.go:94-111），**字面量优先于通配符**，`GET /api/mcp-servers/logs` 不会与任何现有 pattern 重叠、注册顺序无关。handler 放 mcp-hub `Service`（`ListLogs` / `ListLogFiles` / `ReadLog` 方法暴露），admin-api 转发——与现有 `handleMCPServerUpdate` 调 `s.hub.SetServerEnabled` 的模式一致（admin-api 持 mcp-hub 服务引用）。
- **`offset` 语义**：**单段文件内的字节偏移**（非跨段全局偏移）。`?file=<段名>` 指定段，`offset=N` 从该段 N 字节起读 `limit`（默认 64KB）；`eof=true` 表示已到该段末尾。前端据此增量：`next = offset + len(content)`。
- 鉴权沿用 `AuthSession`；`{name}` 校验：`filepath.Base(name) == name`（Windows 下 `filepath` 同时识别 `/` 与 `\`，比 `path.Base` 更稳）**且 `name != "." && name != ".." && name != ""`**（`path.Base` 对 `..` 会原样返回，单纯 `Base` 校验挡不住 `../` 穿越）；`file` 参数同样校验（只允许段文件名格式 `[0-9]{8}-[0-9]{6}(-[0-9]+)?.log`）。
- 无日志（server 未连接过 / 已删）→ `200 {items:[]}` / `{content:"",offset:0,size:0,eof:true}`，不报错。
- **测试性连接不落日志（明确可接受）**：`handleMCPServerTest` / `handleMCPToolsList` / `handleMCPToolSchema` / `handleMCPToolCall` 建的是**临时 Upstream**（admin-api `listUpstreamTools` :1408-1418、`newMCPUpstream` :1447-1452），不走 mcp-hub 连接池、无 LogHook → 这些探测请求不写会话日志（如需记录需在 admin-api 侧同样注入 hook，YAGNI，不做）。

---

## 前端组件设计

### `McpPanel.vue`（改动）

- 在 `:325`（第三个 `TabsTrigger` `endpoints`）之后、`</TabsList>`（`:326`）之前加 `<TabsTrigger value="logs">日志</TabsTrigger>`（`TabsList` 是 `:322-326`，触发器在 `:323-325`）。
- `TabsContent` 末尾（`:780` 前）加 `<TabsContent value="logs" class="space-y-4"><McpLogsTab /></TabsContent>`（组件全局可用，无需 import；与项目惯例一致）。

### `McpLogsTab.vue`（新组件，`frontend/src/components/mcp/`）

- **顶部工具栏**：`Select`（server 下拉，数据来自 `GET /api/mcp-servers/logs`，项显示 `name · transport`，选中即展示该 server 日志）+ 元信息 Badge（最新段 size / 段数，tint 配色沿用项目规范 `bg-{c}-500/15 text-{c}-700 dark:text-{c}-300 border-{c}-500/20`）+ 跟随滚动 Toggle（默认开）。API 调用统一走 `@/lib/api` 的 `api<T>` / `request`（与 `useMcpManagement.ts` 一致，`credentials: 'same-origin'` 自动带会话 Cookie）。
- **加载策略（尾部 + 向上翻页，不整读全文件）**：
  - 首次选中 server：`GET /api/mcp-servers/{name}/log/files` 拿段列表 → `GET /api/mcp-servers/{name}/log?file=<最新段>&offset=<size-512KB>` 读**最新段尾部 512KB**，正文区上方显示「加载更早…」按钮。
  - 点「加载更早」：向前读当前段剩余部分；读完当前段再向前一档切换 `file=<前一段>`（按段列表倒序逐段回溯），直至全部段读完按钮消失。
  - 1s 轮询：**维护独立的「尾部游标」`tailOffset`（仅对最新段累计），与「查看游标」分开**——轮询永远 `?file=<最新段>&offset=<tailOffset>` 增量追加；即使用户正停在上游历史段看「加载更早」的结果，轮询也只推进 `tailOffset`，不影响查看区域；最新段滚动后（`files` 列表变化）`tailOffset` 归零、自动切到新段继续追。`eof=true` 且无新内容不重渲染。
- **主体**：`<pre>` 等宽字体、`max-h-[calc(100vh-320px)] overflow-auto`、`whitespace-pre-wrap`、深色/浅色主题用项目 token（`bg-muted/40` 等）。可选：按 `[KIND]` 着色（FRAME→ 蓝 / FRAME← 绿 / STDERR 红 / CONNECT 黄——仅在实现时用文本 token 级别做，不做复杂高亮）。
- **生命周期（已核实）**：本项目的 Tabs 底层是 reka-ui（shadcn-vue-cdn），`TabsContent` **切走即 unmount**（无 `forceMount`/`keepMounted`，且项目路由无 KeepAlive）→ `onBeforeUnmount` 清 interval 成立，切走 tab 不会后台空轮询；切换 server 同样重建组件/重置状态。server 停用不轮询（接口返回 200 空即可，无需特判）。
- **滚动**：跟随模式开启且用户滚动在底部（`scrollTop + clientHeight >= scrollHeight - 40`）时自动滚到底。
- 空态：无 server 有日志 → 居中提示「暂无 MCP 日志」；选中 server 无内容 → 「等待 MCP 活动…」。

---

## 实施步骤（按 commit 拆分，每步可独立回溯）

### Step 1 — core/mcpkit 日志 hook（`core/mcpkit/`）
- `UpstreamConfig` 加 `LogHook`；`buildTransport()` stdio 分支用 **方案 B（`os.Pipe()` 自建管道，`cmd.Stderr = pw`，reader goroutine 逐行 → LogHook）** 捕获 stderr（SDK 已核实不碰 `cmd.Stderr`，且 `Wait()` 不关自建 pipe，无尾部丢失竞态）；`Connect`/`Close` 埋 connect/connect_ok/connect_fail/disconnect，加 `disconnected` 防重标记。
- 时序注意：`cmd.Stderr`/pipe 必须在 `client.Connect`（SDK 内部 `cmd.Start()`）**之前**设好；`pw` 在 Connect 返回后（成功/失败都）Close，否则读不到 EOF。
- 测试：`mcpkit_test.go` 加 fake stdio server（启动后往 stderr 打两行**含无换行符的末行**、随即退出）→ 断言收到 stderr hook 行且末行不丢（验证方案 B 的 EOF flush）、进程退出触发 disconnect(process_exit)；Connect 失败场景（非法 command）→ 断言 connect_fail hook + stderr 行。
- 验证：`go test ./core/mcpkit/`。

### Step 2 — mcp-hub ServerLog/LogManager（`plugins/mcp-hub/server_logs.go` 新文件）
- `ServerLog`/`LogManager` 实现（Ensure/Write/roll/Close/ListFiles/Read/Remove + **自带 maskSecret 副本**；maxSize 默认 32MB，可注入便于测试）。锁职责与 Remove 顺序按「并发与生命周期」小节；帧合成序号计数也放这里（`callEntryInner` 用）。
- `service.go`：NewService 初始化、Close 清理、`getUpstream` 注入 LogHook（回调只调 `logMgr.Write`，不得取 `s.mu`）、`callEntryInner` 帧埋点。
- 测试：`server_logs_test.go` —— 写 3 条 → Read 全量 + offset 增量一致；写满小 maxSize → 自动滚动 `-2.log` 且旧段可读、新行进新段；段文件排序；Remove 后目录消失；**并发写 + 滚动**（多个 goroutine 同时 Write 触发滚动，段文件数正确、无丢失）；**Remove 与 Write 并发**（Remove 期间在写的行要么整行在旧句柄要么整行被丢弃，不 panic、不残留已删文件句柄）；**Ensure 幂等**（Remove 后可重建）。
- 验证：`go test ./plugins/mcp-hub/`。

### Step 3 — admin-api 端点（`plugins/admin-api/`）
- mcp-hub `Service` 暴露 `ListLogs()` / `ListLogFiles(name)` / `ReadLog(name, file, offset, limit)` / `RemoveServerLogs(name)`；admin-api 注册 `GET /api/mcp-servers/logs` + `GET /api/mcp-servers/{name}/log/files` + `GET /api/mcp-servers/{name}/log`（`AuthSession`；name 用 `filepath.Base` + 拒 `.`/`..`，file 用段名正则）。
- `handleMCPServerDelete`（`:1264`）删除前调 `s.hub.RemoveServerLogs(it.Name)`（先关句柄再删目录）。
- 测试：`admin_api_test.go` 补用例（建 server → 写日志 → 列表含它 → files 列表 → 增量读 → 删 server → 列表空；**无会话 401**；**路径穿越**：`{name}=..`、`..%2Fevil`、`file=../x.log` → 400；file 非法格式 → 400；offset 超过段大小 → 空 content + eof）。
- 验证：`go test ./plugins/admin-api/`。

### Step 4 — 前端日志 tab（`frontend/src/components/mcp/McpLogsTab.vue` 新 + `McpPanel.vue` 改）
- 新组件：下拉 + 尾部 512KB 加载 + 「加载更早」向上翻页（跨段回溯）+ 1s 轮询增量追最新段 + 跟随滚动 + 空态；`McpPanel.vue` 加 tab。
- 验证：`vue-tsc -b --force` + `vite build` 通过。

### Step 5 — 端到端验证（真实环境，用户参与）
- 配 1 个 stdio MCP（如 `codegraph`）+ 1 个 http MCP（如 WorkBuddy `$smart` 对端）；分别触发 invoke；
- 检查：`~/.loadout/logs/mcp/<name>/<ts>.log` 含 CONNECT/FRAME→/FRAME←（stdio 另有 STDERR）；UI tab 下拉可选、1s 增量刷新、跟随滚动、元信息正确；删 server → 目录清理。
- 失败场景：stdio 配错 command / http 指向不可达地址 → 落盘含 `[CONNECT-FAIL]`（与 `[STDERR]`），文件名存在且可被 UI 选中展示。
- 磁盘轮转：临时把 maxSize 调小验证滚出 `-2.log` 续写且旧段可在 UI 回看；删 server → 整目录清除。

---

## 风险与对策

| 风险 | 等级 | 对策 |
|---|---|---|
| SDK `CommandTransport` 覆盖 `cmd.Stderr` 导致捕获失效 | ~~P1~~ **已消除** | 已读 go-sdk v1.7.0 源码（`mcp/cmd.go`）：只 `StdoutPipe`/`StdinPipe` + `cmd.Start`，从不碰 `Stderr`。定稿方案 B：`os.Pipe()` 自建管道 + reader goroutine（详见「1. core/mcpkit 增强」） |
| stderr 尾部丢失（进程退出瞬间的管道竞态） | P1 | ~~`StderrPipe()` 方案弃用~~（SDK `Wait()` 退出后立即关读端，丢「启动失败根因」）；方案 B 读端不归 SDK 管，EOF 可知可 flush |
| 帧结果超长写爆日志（MCP 大文本/图片 base64 返回） | P1 | FRAME← 按 `config.MaxToolResultChars`（已存在于 `core/config/config.go:184`，无需新增常量）截断 + `truncated=true`；**FRAME→ 的 params 同样按该值封顶**（大 arguments 同理） |
| 多 goroutine 并发写（stderr goroutine + invoke 埋点 + Connect） | P1 | `ServerLog.mu` 互斥写；单次 write 用一次 `fmt.Fprintf` 而非分次；`Write` 内不做慢操作（stderr 拷贝 goroutine 与 `os/exec` `Wait` 会等它） |
| 滚动竞态（写满瞬间多 goroutine 触发滚动） | P1 | 滚动判定 + 换句柄全程在 `mu` 内完成；`roll()` 幂等（打开前再检查 `size > maxSize`） |
| `Remove` 与并发写（server 删除时正在写） | P1 | 已定义锁顺序：先摘 map（阻新写入）→ 持 `ServerLog.mu` 等已写完成 → 关句柄 → `RemoveAll`；Step 2 加并发测试 |
| 段文件无限增长（旧段保留直到删 server） | P2 | 用户已接受（「删 server 时全清」）；单文件上限保证每段可管理；实现时在 ListFiles 返回段数，UI 展示 |
| 日志含 token（headers/env） | P1 | CONNECT 行 env/headers 值统一 maskSecret（mcp-hub 自带一份同语义实现，unifyai 版未导出）；FRAME params 里若有鉴权字段由上游决定（不做深度扫描，YAGNI） |
| 大文件读性能（offset 增量） | P2 | 每次只读增量（limit 64KB）；首次只读尾部 512KB，不整读全文件；offset 是单段内字节偏移 |
| `{name}` 路径穿越 | P1 | `filepath.Base(name) == name` **且 `name` ∉ {`.`, `..`, ``}** 才放行（`path.Base` 对 `..` 原样返回、对 Windows 反斜杠无效）；`file` 参数用段名正则 `[0-9]{8}-[0-9]{6}(-[0-9]+)?.log` |
| 文件句柄泄漏（server 频繁增删） | P2 | Close/Remove 统一关句柄；LogManager 生命周期跟 Service |
| HTTP/SSE 后台断开不可观测 | P2 | SDK 的 SSE 监听流自动重连（失败退避），重连失败不回调 → HTTP/SSE 的 DISCONNECT 只记显式 Close；「连接丢失」表现为下一次请求的 FRAME← error，文档 KIND 表已注明 |
| 首连失败时的文件名（决策表语义是「首次 connect 成功」） | P2 | 首写即建文件、base=首条日志时间（首连失败也落盘）；若必须等于首次成功时间需实现前与主线程确认（见「文件创建时机」） |
| server 改名后旧日志目录孤儿 | P2 | 日志目录以 name 建；改名不迁移旧目录，需等删 server 时整目录清除（与现有删除联动一致，接受） |

---

## 验收标准

1. 三种 transport（stdio/http/sse）的 server 在启停、调用、失败时都产生对应日志行（CONNECT/FRAME/STDERR/DISCONNECT 等），落盘路径与命名符合 `~/.loadout/logs/mcp/<server-name>/<YYYYMMDD-HHMMSS>.log`。
2. stdio server 启动失败时 stderr 原文可见（`[STDERR]` 或 `[CONNECT-FAIL] err=...`），且**首连即失败的 server 也有日志文件可读**（base=首条日志时间，不丢失败日志）。
3. API：`GET /api/mcp-servers/logs` 与 `GET /api/mcp-servers/{name}/log?offset=N` 行为符合契约（offset=单段内字节偏移，返回 `offset`/`eof`，前端据此算 next），鉴权生效（无会话 401），无路径穿越（`{name}`/`file` 的非法值 400，含 `..`、`..%2F`、非法段名）。
4. 前端：MCP 管理页新增「日志」tab，下拉可选 server，1s 无感增量刷新（只追最新段，不影响查看历史段），跟随滚动，「加载更早」可向上翻页回溯历史段，元信息正确；切走 tab 组件销毁、轮询停止；`vue-tsc` + `vite build` 通过。
5. 单文件写满 32MB 自动滚动到 `-2.log` 续写，旧段保留可在 UI 回看（测试用小 maxSize 验证）；删 server 时整目录清除；滚动/删除与并发写场景不丢行、不 panic（并发单测覆盖）。
6. `mcp_invocations` 表与 stats 功能不受影响（回归 `go test ./plugins/mcp-hub/ ./plugins/admin-api/`）。
