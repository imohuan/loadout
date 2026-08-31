# 进程面板断联 —— 根因修复计划（更新版）

## 真正的根因（已用日志铁证确认）

不是「安装 npm 库导致断联」，而是：

**`core/servercore/server.go` 里 `http.Server.WriteTimeout = config.UpstreamTimeout`（300 秒）作用于所有 HTTP 连接，包括 SSE 长连接。**

- Go 的 `http.Server.WriteTimeout` 是**绝对写超时**：从请求头读完开始倒计时，到点**强制掐断连接**，不管连接是否活跃。
- `/api/processes/stream` 等 SSE 长连接从建立那一刻起就被倒计时 300s，每 5 分钟被服务端强掐一次。
- 日志铁证（`~/.loadout/logs/loadout.log`）：
  - `11:26:20` SSE 建立 → `11:31:21` 断开，`duration_ms=300200`（恰 300s）
  - `00:17:49` SSE 建立 → `00:22:49` 断开，`duration_ms=300000`（恰 300s）
  - 断开后 2 秒前端 `scheduleReconnect` 重连。
- 前端 `scheduleReconnect` 只重连 5 次（约 31s）就**永久放弃**，所以断开的连接不会自动恢复，直到用户刷新页面（Pinia store 重建后重新 connect）。

**为什么用户感觉是「安装 npm 时断」**：只是安装过程恰好跨越了某个 300s 断点，跟安装本身无因果关系。

## 修复方案

### 1. 核心修复（治本）：去掉 SSE 长连接的 WriteTimeout
- `core/servercore/server.go`：移除 `WriteTimeout: config.UpstreamTimeout`（设为 0 = 无写超时）。
- 安全性论证：
  - SSE 处理器已有 `r.Context().Done()` 主动退出 + 15s 心跳保活。
  - server shutdown 时有 `connCancel()` 主动断开所有活跃连接。
  - 普通请求由 `ReadHeaderTimeout`(10s) + 各自 handler 超时兜底，不受影响。
- 这样 SSE 长连接不再被定时掐断，进程状态持续实时刷新。

### 2. 前端兜底修复（保险）：重连不再「5 次即放弃」
- `frontend/src/stores/processes.ts`：`scheduleReconnect` 改为持续指数退避重连（封顶 30s），永不放弃。
- 即使将来出现其他断线原因（后端重启、网络抖动），也能自动恢复，不依赖手动刷新。
- 同步调整 `reconnectFailed` 语义与 `ProcessFooter.vue` 的「重连失败，请刷新」提示文案。

## 需要修改的文件

| 文件 | 改动 |
|------|------|
| `core/servercore/server.go` | 移除 `WriteTimeout` 字段（治本，SSE 不再被掐断） |
| `frontend/src/stores/processes.ts` | `scheduleReconnect` 改为持续退避重连（兜底） |
| `frontend/src/components/ProcessFooter.vue` | 状态点提示文案调整（去掉误导性「重连失败」） |

## 实施步骤

1. ✅ 修改 `server.go` 移除 WriteTimeout（commit 9636f41）。
2. ✅ 修改 `processes.ts` 重连策略：`scheduleReconnect` 改为持续退避重连，清理 `reconnectFailed` 死代码。
3. ✅ 调整 `ProcessFooter.vue` 文案：断开时统一显示「重连中 (N)」，去掉误导性「重连失败，请刷新」。
4. ✅ 验证：`go build ./...` + `pnpm build` 均通过。
5. ⏳ 手动验证（需用户重启 loadout 生效）：观察日志中 `/api/processes/stream` 是否不再卡在 300s 断开。

## 验证方案

- 后端：`go build ./...` 通过。
- 前端：`pnpm build` 类型与构建通过。
- 手动（关键）：观察日志中 `/api/processes/stream` 连接是否还卡在 300s 断开；若不再断开，说明治本成功。

## 风险与注意

- 去掉 WriteTimeout 后，理论上恶意慢客户端可挂住连接；但本服务是本地/局域网工具，且有 `ReadHeaderTimeout` 与 handler 层超时，风险可忽略。
- 保留前端持续重连作为兜底，双保险。
