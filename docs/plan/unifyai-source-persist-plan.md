# UnifyAI source 路径持久化 + 查询接口传递 — 实现计划

## 任务概述
前端「模型源配置路径（--source）」输入框目前是内存临时值，刷新即丢；且 `--list all` / `--list models` 查询接口不传 `--source`，只能死磕默认 `~/.opencodex/config.json`。
目标：source 持久化到 sync.json（失焦保存），查询接口读同一份 source 传给 CLI。

## 改动文件

### 后端 Go
1. `plugins/unifyai/service.go`
   - 新增 `SyncConfig() (map[string]any, error)`：读 ~/.unifyai/sync.json（不存在返回空 map，不报错）
   - 新增 `UpdateSource(source string) error`：读 sync.json（无则空）→ 只改 `source` 字段 → 写回
2. `plugins/admin-api/service.go`
   - 新增路由：`GET /api/unifyai/sync-config`、`PUT /api/unifyai/sync-config/source`
   - 新增 handler：读 / 改 source
   - 改 `handleUnifyaiAll` / `handleUnifyaiOpenCodexModels`：查询前读 sync.json 的 source，非空则拼 `--source <path>`

### 前端 Vue/TS
3. `frontend/src/lib/unifyai.ts`
   - 新增 `fetchSyncConfig()`：GET sync-config，返回 source
   - 新增 `saveSourcePath(source)`：PUT sync-config/source
4. `frontend/src/components/unifyai/UnifyaiPanel.vue`
   - sourcePath 输入框加 `@blur` → 失焦保存
   - onMounted 初始化读回 source 填到输入框

## 关键逻辑
1. `UpdateSource`：文件不存在时用 `{}` 起步，只写 source 字段，不碰其他字段
2. 查询接口：sync.json 有 source 且非默认空值才拼 `--source`，避免传空
3. 前端失焦保存：保存当前值；初始化回填

## 测试
- 后端：UpdateSource 写入后 SyncConfig 能读回；查询接口能收到 --source
- 前端：改输入框失焦后刷新，值保留

## 潜在坑
- sync.json 可能不存在 → UpdateSource 新建
- source 为空 → 不拼 --source
- 失焦保存与点同步整包写不冲突（只改 source 字段）
