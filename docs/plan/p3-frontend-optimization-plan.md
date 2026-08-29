# P3 前端优化实施计划

## 任务概述
对 frontend/ 中最大的两个面板组件 McpPanel.vue 与 UnifyaiPanel.vue 做最小化重构：
1. 删除死文件 `composables/useClickOutside.js`（已确认零引用）。
2. 清理 `lib/unifyai.ts` 的 TODO 半成品死代码。
3. 抽取公共 `useEnvRows` composable，消除两面板重复的 env/header 行增删逻辑。
4. 将重复的键值行编辑 UI 抽成 `KeyValueRowsEditor.vue` 子组件。
5. 范围内的 v-for 均已带 :key，无需改动。

## 范围
- 仅改：McpPanel.vue、UnifyaiPanel.vue、useClickOutside.js（删）、unifyai.ts、新增 composables/useEnvRows.ts、新增 components/mcp/KeyValueRowsEditor.vue。
- 不改：SkillsView.vue、ModelTestView.vue（后续）、Go 代码、其他插件、P4 逻辑。

## 步骤
1. 删除 composables/useClickOutside.js。
2. 清理 unifyai.ts 死代码（runSync / updateMetadata / NOT_IMPLEMENTED）。
3. 新建 composables/useEnvRows.ts + components/mcp/KeyValueRowsEditor.vue。
4. McpPanel.vue 接入子组件与 composable。
5. UnifyaiPanel.vue 接入子组件与 composable。
6. 每步 vue-tsc --noEmit 验证 + git commit。
7. 写 docs/tmp/p3-implementation.md。

## 验证
- `cd frontend && npx vue-tsc --noEmit` 零类型错误。
- 行为保持完全不变（只做抽取，不改语义）。
