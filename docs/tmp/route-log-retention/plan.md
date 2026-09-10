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

## 已采用的方案（未收到用户异议，按推荐项实现）
- 保留策略生效时机：「写新日志时清理一次」+ **服务启动时清理一次**，不额外起定时器。
- 「清空日志」= 清空完整请求日志库（request-log.db），确认框里写明条数/占用/最早一条时间。

## 验证（已完成）
- `go build ./...`；`go test` 覆盖 core/、request-log、route-log、field-filter、
  force-stream、model-gateway、vision、contracts、apps/server —— 全绿。
- 前端 `vue-tsc -b` 类型检查 + `vite build` 通过。
- 端到端起真实服务验证：
  - `/api/request-logs/stats` 报出文件大小/条数/时间范围，并回显保留配置。
  - 45 条日志（40 条 90 天前 + 5 条昨天）+ 7 天策略 → 重启后只剩 5 条，文件 1.5MB → 200KB。
  - 300 条 ×16KB + 1MB 上限 → 触发保留下限（100 条）停止，符合预期保护。
  - 清空后 VACUUM 生效：主文件 1.36MB → 36KB（并清空 route_requests 关联列）。
  - route-logs 列表只回仍然存在的 request_log_id，失效关联的入口不再显示。

## 踩到的坑（已修）
1. **装配顺序**：request-log 与 route-log 的拓扑顺序不保证，起初 request-log 先装配，
   存在性钩子回填静默失效 → 在 request-log Manifest 显式 `Inject "route-log"`，
   并加 `plugins/assembly_order_test.go` 固化。
2. **VACUUM 后必须再 checkpoint**：只在 VACUUM 前 checkpoint 的话，VACUUM 自身的写入
   先落 WAL，读主文件仍是旧尺寸，看起来"没收缩"。两次 checkpoint 才拿到真实大小。
3. **-shm 不该计入占用**：它是固定 32KB 的共享内存索引，计入会让清空后的大小不掉。
