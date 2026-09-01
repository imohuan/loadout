# Spec 审查报告：d02301f（unifyai source 持久化）

## (a) spec 要求缺失 / 部分实现
- **前端测试缺失**：spec「测试」节明确写「前端：改输入框失焦后刷新，值保留」，但 diff 只有后端 Go 测试（source_persist_test.go / source_persist_cli_test.go），无任何前端测试/验证用例提交。此项仅实现了一半。

## (b) 范围蔓延
- 未发现越界改动。所有文件都在 spec「改动文件」清单内，`docs/plan` 为计划文档本身。

## (c) 实现可能错误的项
1. **失焦保存与整包同步对"空值"处理不一致，可能互相覆盖**
   - spec 关键逻辑 3：「失焦保存与点同步整包写不冲突（只改 source 字段）」。
   - 实际：`handleSourceBlur` 空值 `return`（UnifyaiPanel.vue:715 `if (!v) return`，不保存，旧值保留）；但点同步时 `executeWithArgs` 会 `saveSyncConfig(buildSyncConfig())` 整包覆盖 sync.json，且 `commandOpts` 含 `source: sourcePath.value`（UnifyaiPanel.vue:736）→ 整包写**会**改写 source 字段。若用户清空输入框后直接点同步，source 被清空；而失焦保存却拒绝清空。两条保存路径行为矛盾，且整包写确实会覆盖 UpdateSource 持久化的值，与 spec 宣称的"只改 source 字段不冲突"不符（功能上通常一致，仅空值/并发边界存在覆盖风险）。

2. **`UpdateSource` 空串也写入**：API 层 PUT 空 source 会写空字符串到文件（虽前端失焦已拦截空值）。`sourceFromSync` 返回空串→不拼 `--source`，行为安全，但与 spec「source 为空→不拼」语义一致，可接受。

## 核对通过项
- 失焦保存/onMounted 回填：`fetchSyncConfig` 返回完整 map，`cfg.source` 取回填，与后端 `SyncConfig()` 结构一致 ✅
- `ListAll`/`OpenCodexModels` 非空才拼 `--source` ✅
- 后端 `SyncConfig` 文件不存在返回空 map 不报错；`UpdateSource` 读→只改 source→写回，保留其他字段 ✅（测试已覆盖）
- 前后端接口路径/字段名一致：GET/PUT `/api/unifyai/sync-config(/source)`、body `{source}` ✅
