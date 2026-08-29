# P3 前端优化实施记录

## 改动清单

### 删除
- `frontend/src/composables/useClickOutside.js` —— 全项目零引用，确认后删除。

### 新增
- `frontend/src/composables/useEnvRows.ts` —— 公共键值行（env/header）增删逻辑 `addRow`/`removeRow`，收口 McpPanel 与 UnifyaiPanel 的重复实现。
- `frontend/src/components/mcp/KeyValueRowsEditor.vue` —— 键值行编辑器子组件（key/value 输入 + 增删按钮），内部用 useEnvRows。
- `frontend/src/components/mcp/ToolTestDialog.vue` —— McpPanel「测试工具」弹窗子组件（状态/逻辑由父组件以 props 传入，行为不变）。
- `frontend/src/components/unifyai/HelpDialog.vue` —— UnifyaiPanel「平台能力矩阵」帮助弹窗子组件（自包含）。

### 修改
- `frontend/src/lib/unifyai.ts` —— 删除零引用的 TODO 半成品死代码 `runSync`、`updateMetadata`、`NOT_IMPLEMENTED` 常量，并修正头部过时注释。
- `frontend/src/components/mcp/McpPanel.vue`（1182 → 1049 行）：
  - 移除内联 `add`/`remove` 函数，Header/环境变量行改用 `<KeyValueRowsEditor>`。
  - 测试工具弹窗改用 `<ToolTestDialog>`。
- `frontend/src/components/unifyai/UnifyaiPanel.vue`（1532 → 1440 行）：
  - 移除内联 `addEnvRow`/`removeEnvRow`，添加/编辑弹窗的环境变量行改用 `<KeyValueRowsEditor>`。
  - 帮助弹窗改用 `<HelpDialog>`。

### 说明
- 范围内（McpPanel/UnifyaiPanel）的 v-for 均已带稳定 `:key`，无需改动；约 18 处缺 :key 位于 SkillsView/ModelTestView 等本次范围外文件，留待后续。
- 未触碰 Go 代码、其他插件与 P4 相关逻辑。

## Commit 摘要
1. `chore: 删除零引用的死文件 composables/useClickOutside.js`
2. `refactor: 清理 lib/unifyai.ts 中零引用的 TODO 死代码 runSync/updateMetadata`
3. `refactor: 抽取公共 useEnvRows 与 KeyValueRowsEditor，McpPanel 接入去除重复 env/header 行逻辑`
4. `refactor: UnifyaiPanel 接入 KeyValueRowsEditor，去除重复 env 行增删逻辑`
5. `refactor: 将 UnifyaiPanel 帮助弹窗拆分为独立子组件 HelpDialog`
6. `refactor: 将 McpPanel 测试工具弹窗拆分为独立子组件 ToolTestDialog`

## 验证结果
- `cd frontend && node node_modules/vue-tsc/bin/vue-tsc.js --noEmit` —— 零类型错误，退出码 0。
- 全部改动仅做抽取/删除，功能语义未变。
