# MCP 会话日志：启动清空全部 + 开关工具清空单个

## 任务概述

用户希望「上游 MCP」页面的会话日志遵循两条清空规则：

1. **应用启动时**：清空 mcp 日志根目录（`config.LogsDir/mcp/`）下**全部** server 的日志。
2. **每个 MCP 工具启动（开关）时**：清空**该 server** 的日志。

## 根因

当前日志清空只在 `mcpkit` 真正新建连接时发 `connect` 事件才触发
（`getUpstream` 的 `LogHook` 里 `kind == "connect"` → `RemoveServerLogs`）。
但 `Connect()` → `ensureSession()` 中，若连接池里 `u.session != nil`（连接仍存活），
会**直接复用、不发 `connect` 事件**，导致：
- 应用启动拉起时连接复用 → 旧日志不清空；
- 页面开关工具时连接复用 → 旧日志不清空。

## 改动方案

### 1. 应用启动时清空全部日志 —— `StartEnabled`

`StartEnabled`（`plugins/mcp-hub/service.go`）是应用启动后台拉起的唯一入口
（`core/servercore/server.go:158` 调用）。在其开头，遍历 `logMgr.ListServers()`
逐个 `RemoveServerLogs(name)`，清空全部 server 日志，然后再拉起。

### 2. 开关工具时清空该 server 日志 —— `SetServerEnabled`

`SetServerEnabled(ctx, id, true)` 里在 `s.getUpstream(*srv).Connect(ctx)` **之前**，
先 `s.logMgr.RemoveServerLogs(srv.Name)`。

### 3. 保留真重连兜底

`getUpstream` LogHook 里 `kind == "connect"` 的 `RemoveServerLogs` 保留，
作为真正新建连接（连接断开重建）时的兜底。

`RemoveServerLogs` 幂等（目录不存在时 no-op），三处叠加无副作用。

## 修改文件

- `plugins/mcp-hub/service.go`：
  - `StartEnabled` 开头加"清空全部"；
  - `SetServerEnabled` 的 enabled 分支加"清空单个"。

## 测试

- 手动：应用重启 → 日志全清；启停单个 server → 该 server 日志清空、其余保留。
- 跑 `go test ./plugins/mcp-hub/...`。

## 风险

- 清空只删日志目录，不影响连接与工具调用。
- `StartEnabled` 清空全部只在该函数内做一次，不重复。
