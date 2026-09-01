# 多模态纳入 MCP 管理/聚合（rev2：内置注册 + UI 展示）

> 承接上一版自连方案，按用户反馈改为**更简洁的「内置注册」方案**：不在 UI 里伪装成上游 MCP 自连，而是 mcp-hub 导出注册函数，多模态插件用它把工具注册进聚合；前端「连接端点配置」页**直接展示内置端点**。

## 一、目标

1. 多模态的 3 个工具（understand_image/video/audio）进入 `$smart` 聚合，可被聚合端点调用。
2. MCP 管理页「连接端点配置」**直接显示** `/mcp/multimodal` 内置端点（含工具数、复制地址、创建密钥）。
3. 不搞自连 HTTP / 不伪装上游 server / 不加 key 复杂度。
4. 保留多模态独立端点 `POST /mcp/multimodal`（两个入口都可用）。

## 二、方案（内置工具注册）

### 2.1 mcp-hub 新增「内置工具注册」能力
- `ToolEntry` 增加可选字段 `BuiltinHandler func(ctx, args) (*mcpkit.ToolResult, error)`（内置工具直调 handler，不走上游）。
- `Service` 增加注册表 `builtinTools []ToolEntry`（并发安全，mu 保护）。
- **导出注册函数**（多模态等插件调用）：
  ```go
  func (s *Service) RegisterBuiltinTools(tools []ToolEntry)   // 注册内置工具（进聚合）
  func (s *Service) UnregisterBuiltinTools(ids ...string)     // 注销（配置关闭时）
  func (s *Service) BuiltinEndpoints() []BuiltinEndpoint      // 供前端展示内置端点
  ```
- `BuildIndex` 收集时把 `builtinTools` 纳入（category=来源名，source=来源名），进入 `$smart` 聚合。
- `callEntryInner` 对 `BuiltinHandler != nil` 的工具直接调用 handler（不经上游）。

### 2.2 多模态插件接入
- `plugin.go` Manifest 增 `Inject: "mcp-hub"`。
- Apply 装配拿到 hub，调用 `hub.RegisterBuiltinTools([]ToolEntry{ 3 个工具 })`，handler 直调 `svc.understandImage/Video/Audio`（复用现有识别逻辑，不重复实现）。
- 保留独立端点 `POST /mcp/multimodal`。

### 2.3 前端展示内置端点
- 后端 mcp-hub 提供 `BuiltinEndpoints()`（路径 /mcp/multimodal、工具数、label、transport）。
- admin-api 暴露 `/api/mcp-builtins`（或并入现有端点列表接口）。
- 前端「连接端点配置」tab 把内置端点作为一条展示（复用现有「复制地址/复制配置/创建密钥」操作）。
- 内置端点用已有 `builtin` 标记显示「内置」Badge。

## 三、改动文件清单

| 文件 | 改动 |
|---|---|
| `plugins/mcp-hub/service.go` | ToolEntry 加 BuiltinHandler；Service 加 builtinTools 注册表；RegisterBuiltinTools/UnregisterBuiltinTools/BuiltinEndpoints；BuildIndex 纳入；callEntryInner 直调 |
| `plugins/mcp-hub/plugin.go` | 无（方法在 Service 上） |
| `plugins/multimodal-mcp/plugin.go` | Manifest 注入 mcp-hub；Apply 调 RegisterBuiltinTools |
| `plugins/admin-api/*` | 暴露内置端点列表给前端（或 mcp-hub 接口直接透出） |
| `frontend/src/composables/useMcpManagement.ts` | endpoints 合并内置端点；McpServer/McpEndpoint 支持 builtin |
| `frontend/src/components/mcp/McpPanel.vue` | 连接端点配置渲染内置端点 + 「内置」Badge |

## 四、实施步骤

1. mcp-hub：ToolEntry.BuiltinHandler + builtinTools 注册表 + 导出函数 + BuildIndex 纳入 + callEntryInner 直调。→ 单测 + commit。
2. 多模态：注入 mcp-hub，Apply 注册 3 工具。→ 单测 + commit。
3. 后端接口：admin-api 暴露内置端点列表（工具数/label）。→ 单测 + commit。
4. 前端：连接端点配置展示内置端点 + 「内置」Badge + 复制/密钥操作。→ build 验证。
5. 端到端：开启多模态 → `$smart` 聚合含 3 工具；连接端点配置显示 `/mcp/multimodal`；独立端点仍可用；浏览器验证。

## 五、风险与注意

- 内置工具 handler 复用多模态识别方法，避免逻辑重复；工具开关（tools_state）对内置工具是否生效需确认（可选）。
- `$smart` 聚合工具数量变化（原 125 + 3 = 128）。
- 前端改动收敛在 useMcpManagement + McpPanel。
- 已提交的 `builtin` 字段迁移（v29）保留用于前端标识；`UpsertBuiltinServer/RemoveBuiltinServer`（自连专用）在新方案下不再使用，评估是否移除。

## 六、待确认（已定案）

1. 内置工具是否也支持 tools_state 单工具开关？→ **支持**（BuildIndex 已处理，findToolState 对内置工具生效）。
2. `UpsertBuiltinServer/RemoveBuiltinServer`（自连方案的半成品）是否移除？→ **移除**，改用「内置注册 + 内存不落库」方案。

## 七、最终设计（已实现）

**内置 server 不落库**，只存内存，重启后由插件重新注册：

- `RegisterBuiltinServer` 只写 `builtinServers`（server_id→记录）+ `builtinTools`（server_id→工具）两个内存注册表，**不写 mcp_servers 数据库**；`UnregisterBuiltinServer` 只删内存。
- `BuildIndex` 合并内存内置 server → 工具进 `$smart` 聚合；调用直调 `BuiltinHandler`。
- `findServer`/`ServerStatus` 先查内存内置 server 再查库 → 内置 server 状态正确显示「运行中」。
- `BuiltinServers()`/`AllServers()` 供 admin-api 合并展示到上游 MCP 列表 + 连接端点配置（带「内置」标签）。
- 已删 `writeServers` 死代码；`builtin` 字段迁移（v29）不再用于落库，仅作前端标识（实际由内存注册）。

### 已完成 commit
- `8e81f5f` mcp-hub 内置注册机制（Register/Unregister + 工具进聚合 + 直调 handler）
- `0466345` 多模态启动时按配置注册为内置 server，开关联动
- `ae381d9` 前端「内置」标签（上游 MCP 列表 + 连接端点配置）
- `a0eebb3` route-log 修复（识别调用写转发日志 Start/Attempt/Finish）
- `73910a5` 内置 server 改存内存不落库（含状态识别 + admin-api 合并 + 测试断言）

### 待验证（需重启后端）
当前 loadout.exe（PID 7688）是旧二进制，既无内置注册也无 route-log 记录。重启后验证：
1. 多模态开启 → 上游 MCP 列表 + 连接端点配置显示内置 server（「内置」标签，状态运行中）
2. `$smart` 聚合含 3 个多模态工具，可直接调用
3. 图片/视频/音频识别写 route-log 与 request-log
