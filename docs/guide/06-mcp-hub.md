# 06 - MCP 聚合网关

> 本文讲 Loadout 的 MCP 聚合：如何把一堆上游 MCP 工具和技能"收口"成每个端点只暴露 3 个工具，
> 工具定义按需加载，工具再多也不更贵更慢。配套：[01-架构总览](./01-architecture.md)、[03-插件开发指南](./03-plugin-dev-guide.md)。

源码：`plugins/mcp-hub/`（plugin.go / service.go / stats.go / server_logs.go）。

## 1. 核心思路

Loadout 把"MCP 工具聚合"用三个工具装起来：

| 工具 | 作用 |
|---|---|
| `status` | 列出当前端点可见的工具分类/概览 |
| `get` | 按工具名批量拉取完整 JSON Schema（按需加载，不在 status 阶段下发） |
| `invoke` | 校验工具在当前端点可见 → 转发给所属上游 `CallTool` |

**聚合工具只存在于索引里**：客户端永远只看到这 3 个工具，真正的上游工具定义按需 `get` 才返回。
因此"工具越多越贵越慢"被消除——索引是轻量的，schema 才在用到时拉取。

## 2. 三种路由方式

每个 MCP 端点按配置生成，三种方式（见 `service.go` 的 `EndpointServer` / `SmartEndpointServer` / `EndpointServerOrEmpty`）：

| 方式 | 路径示例 | 行为 |
|---|---|---|
| 单 MCP | `/mcp/github` | 直接暴露该上游 MCP 的 status/get/invoke 视图 |
| 分组 | `/mcp/group1` | 把分组内多个上游的工具聚合进同一视图 |
| `$smart` | `/mcp/$smart` | 按 header（默认 `X-Loadout-Group`）动态解析分组视图；空 = 全部工具 |

`servercore.assemble` 用 `mcp.NewStreamableHTTPHandler` 挂载 `/mcp/`，并按请求路径动态分发：
`getServer` 只在**新 session**时调用一次，因此重新连接总能拿到最新配置的工具视图（端点随配置增删实时生效，不需要重启）。

## 3. 调用链路

```
客户端调用 /mcp/$smart 的 invoke(tool=X)
  → Service.Invoke → invokeWith：校验 X 在当前端点视图可见
  → callEntry → callEntryInner：找到 X 所属上游 Server，转发 CallTool
  → 结果按 config.MaxToolResultChars 截断后返回
  → 统一记录到 mcp_invocations / mcp_server_logs（成功失败都记）
```

- `BuildIndex`：聚合所有上游 MCP 与已安装技能的工具进索引；故障上游的工具在 invoke 时走"工具不可见"分支并埋错误记录。
- `ToolView(endpoint)` / `resolveTools(endpoint, group)`：按端点/分组解析可见工具视图。
- 同步：配置变化后 `Invalidate()` 让索引重建。

## 4. 后台拉起

`servercore` 在启动后后台运行 `mcp-hub-start`：拉起所有 enabled 的 stdio MCP 进程（如 npx/uvx 启动的命令），使其常驻后台。单个失败只记日志，不阻断启动；前端经 `/api/mcp-servers` 展示失败状态。

## 5. 日志与埋点

- `mcp_invocations` 表：每次 `$smart`/分组/单 MCP 的 invoke 都记录（调用方、工具、成功/失败、耗时）。
- `mcp_server_logs`：上游 MCP server 的运行日志。
- 埋点统一在 `callEntry`，保证 `$smart` invoke、单 MCP/分组直接调用、技能调用全部被记录。

## 下一步

- 看技能仓库/预设怎么和 MCP 工具结合 → [07-skills 预设](./07-skills-presets.md)
- 看 MCP server / 分组 / 技能的数据存储 → [08-数据存储](./08-data-storage.md)
