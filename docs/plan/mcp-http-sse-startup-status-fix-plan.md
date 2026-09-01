# MCP HTTP/SSE 启动失败状态误报修复

任务概述：上游 MCP 是 **Streamable HTTP** 或 **SSE** 时，后端 `ServerStatus` 一律返回 `running`，导致连接失败（dial tcp refused 等）也在前端展示为「运行中」、toggle 后错误地 toast「已启动」。stdio 类型不受此影响，因为 `cmd != nil && alive` 失败会走 `StateFailed` 分支。

## 现象

- 配置 `http://localhost:8765/mcp`（服务未起），点开关启动。
- 后端日志正确记录 `[CONNECT]`、`[FRAME_IN] msg="write error: ... dial tcp [::1]:8765: ... actively refused it"`、`[CONNECT_FAIL] err="..."`（走 go-sdk LoggingTransport → `frameLogWriter` → `LogHook(frame_in / connect_fail)`）。
- 前端 UI 列表行依然显示 **运行中**（绿色 Badge），且 toast 弹出 **「已启动」**。
- 下次点开关（禁用→重试），同样走一遍，依旧「已启动」。

## 根因

`plugins/mcp-hub/service.go` `ServerStatus`（约 822–838 行）：

```go
if srv.Transport != "stdio" {
    return StateRunning, nil   // ← BUG：HTTP/SSE 一律 running
}
```

HTTP/SSE 没有 stdio 的「进程存活」语义，所以原本的实现用「乐观：enabled 就视为 running」。但与 `SetServerEnabled(enabled=true)` 已经会主动 `Connect`（HTTP/SSE 也走 `ensureSession`）是矛盾的——connect 失败时 `u.lastErr` 已经存了，UI 却从来没读这条信息。

## 修复

最小改动，`ServerStatus` 对非 stdio 在返回 running 之前看一次 `LastError()`：

```go
if srv.Transport != "stdio" {
    if up := s.getUpstreamByID(id); up != nil && up.LastError() != "" {
        return StateFailed, nil
    }
    return StateRunning, nil
}
```

`ServerError(id)` 已经在 failed 时由 `withStatus` 回传 `up.LastError()`，错误文案自动复用。

**前端 0 改动**：`useMcpManagement.toggleServer` 已经写了
```ts
if (updated?.status === 'failed') {
  toast.error('MCP 进程启动失败', { description: updated.error || '...' })
  return
}
toast.success(nextEnabled ? '已启动' : '已停止')
```
对 HTTP/SSE 同样适用，自动把当前 toast 文案「已启动」改成「MCP 进程启动失败」。

## 验证方案

1. `plugins/mcp-hub` 单元测试覆盖 `ServerStatus`：
   - HTTP enabled + 上次 Connect 成功（lastErr=""）→ running
   - HTTP enabled + 上次 Connect 失败（lastErr="dial tcp..."）→ failed + ServerError 返回该 msg
   - HTTP enabled=false → stopped
   - stdio 已有行为不变
2. 配置一个明显打不通的 streamable http（如 `http://localhost:9999/mcp`），点启动：
   - 状态列显示「失败」红色 Badge
   - toast「MCP 进程启动失败」+ 错误描述含 `dial tcp`
   - 修好后能正常显示 running

## 风险/注意

- HTTP/SSE 的「running」语义本来就乐观（不在我们进程内的服务）。这条修复只是把 connect 失败的信号透出来，没有引入「每次状态查询都探活」的副作用。
- 现有 list 接口返回里 `Status` 字段没改 schema，只是字段值多了 `failed` 一种——前端模板已有 `v-if="server.status === 'failed'"` Badge 分支，无样式缺口。
