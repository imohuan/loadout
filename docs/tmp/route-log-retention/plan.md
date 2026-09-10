# 转发日志：日志库大小 / 清空按钮 / 保留策略

## 需求
1. 转发日志页顶部「清空日志」按钮右侧，加一个按钮显示当前日志库大小（request-log.db 文件大小）。
2. 点该按钮弹确认框，确认后清空该日志（日志可能非常大，直接清空有风险）。
3. 表格「完整日志」列：`request_log_id` 指向的 request-log 行已不存在时（被保留策略清掉），不显示「进入日志」按钮。
4. 设置里提供日志保留配置：
   - 只存最近 N 天的日志；
   - 限制最大日志存储空间，超了就删旧的（FIFO）。

## 涉及文件
- 后端
  - `plugins/request-log/service.go`：新增 `Stats`（db 文件大小/行数）、`Clear`、保留策略 `ApplyRetention`、日志库路径记录。
  - `plugins/request-log/plugin.go`：注册 `GET /api/request-logs/stats`、`DELETE /api/request-logs`；启动时应用一次保留策略。
  - `core/db/migrate.go`：settings 表加两列（保留天数、最大 MB）。
  - `core/db/admin_repository.go`：Get/PutSettings 带上新列。
  - `plugins/types/types.go`：Settings 加字段。
- 前端
  - `frontend/src/composables/useRequestLogs.ts`：stats / clear / settings API。
  - `frontend/src/views/RouteLogsView.vue`：按钮 + 确认框。
  - `frontend/src/components/route-logs/RouteLogTable.vue`：无效 request_log_id 时隐藏入口。
  - `frontend/src/lib/types.ts`：类型。
  - 设置页：新增「日志保留」卡片。

## 待用户拍板
- 保留策略（天数/容量）只有「写日志时顺带清理」这一个触发点，还是也支持定时清理？
  默认按「写日志时清理 + 启动时清理一次」实现——零额外定时器，日志库只在有请求时增长。

## 验证
- `go build ./...` + `go test ./plugins/request-log/... ./core/db/...`
- `pnpm -C frontend run build`（vue-tsc 类型检查）
- 手动：起服务，看按钮大小、确认框、设置保存、超限清理。
