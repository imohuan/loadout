# 渠道模型同步 Dialog —— 实现计划

## 需求（已与用户确认）
在「渠道与模型」页展开后的**每个 Key 行**（模型数量位置）加「同步模型」按钮，
弹出单栏 dialog，自上而下：

1. **加载源**：从某个 Key 载入它的模型清单（Select，每个 Key 显示模型数 + hover 预览模型列表）。
2. **模型列表编辑区**：效果同 ChannelEditor 的模型 Popover 内容（搜索 / 批量粘贴 / 自定义添加 /
   全选 / 反选 / 清空 / tag 网格），但直接铺在 dialog 上，不藏在下拉里。
3. **目标 Key 多选**：BulkSelectButtons（全选 / 反选 / 清空）+ 两种显示模式：
   - 列表模式：每行一个 Key，行内把该 Key 的模型以 tag 平铺。
   - tag 模式：每个 Key 一个 tag，hover tooltip 显示模型列表（参考 SkillsView 的标签/详情切换）。
4. **同步**：把「我的模型列表」全量写进每个目标 Key（PUT /api/channels/{id}/models）。

同步语义：用户选 A（全量替换）。新模型统一 enabled=true。
不动目标 Key 的 API Key / 开关 / 费用设置，只改模型目录。

## 改动文件
- 新增 `frontend/src/components/channels/ChannelModelSyncDialog.vue`
- 改 `frontend/src/components/channels/ChannelTable.vue`（按钮 + emit）
- 改 `frontend/src/views/ChannelsView.vue`（接线 + 并发下发 + 刷新）

## 验证
- `pnpm build`（vue-tsc + vite build）通过。
- 手动核对模板属性与现有组件用法一致。
