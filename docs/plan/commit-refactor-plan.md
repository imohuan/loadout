# feat/translate 分支 commit 重构计划（v2）

> 状态：✅ 全部完成——90 提交重构为 8 个清晰提交，树完全一致、构建测试通过
> 备份：`backup/translate-original` + 标签 `backup/translate-original-HEAD`（原始 90 提交完整保留）
> 远端无 feat/translate → 历史仅本地，可安全重写

## 最终成果（8 个提交）

**非翻译段（原 50 提交）：**
1. `c10721d` feat(message-inject): 消息注入能力 + 虚拟模型匹配路由
2. `6b2c18b` feat(process): 后台任务框架、进程管理与依赖检查/更新
3. `8996e3e` feat(model-test): 模板 meta 快照与加载回填
4. `033578e` feat(ui): 模型聚合卡片视图（卡片/表格切换，按 Key 隔离）

**翻译段（原 40 提交）：**
5. `0231ea8` feat(translate): 后端翻译插件（hash缓存+切句+网关子请求+批量+来源收集）
6. `cdde776` feat(translate): 前端翻译 pinia store 与 TranslateText 组件
7. `976c3e9` feat(translate): 翻译设置页 TranslateView 与各页面接入
8. `07d1e27` docs(translate): 翻译计划文档、前端约定与 UI 原型

## 验证
- ✅ 最终树 `2b6d314` 与原始 HEAD 完全一致，内容零丢失
- ✅ `go build ./...` + `go test ./plugins/translate/... ./plugins/message-inject/...` 通过
- ✅ 前端 `pnpm build`（vue-tsc + vite）通过

## 方法
临时分支从 f09f7b5 建 → `git reset --soft`（改动放回暂存区，零冲突）→ 按功能分组 `git add <文件>` + commit → 验证树一致 → 落回 feat/translate。

## 可行性结论（关键验证结果）

两拨功能文件重叠极少：非翻译 61 文件、翻译 24 文件，**仅 5 个文件重叠**：
- `frontend/src/components/capability-routes/CapabilityRouteEditor.vue`
- `frontend/src/components/mcp/McpPanel.vue`
- `frontend/src/lib/types.ts`
- `frontend/src/views/SkillsView.vue`
- `plugins/registry.go`

→ **方案 1（逐块重挑）可高度自动化**：绝大多数文件天然单功能归属，只需对 5 个重叠文件做 -p 拆分。

## 拟重构结果：squash 成 8 个功能提交

1. **feat(procreg): 进程管理与全局后台任务框架**
   - useTask/进程 Pinia store/ProcessFooter/日志弹窗/进程面板/透传 task id
   - 涉及提交：67239eb, c618551, 1b9362b, bc6b3db, 9fa5b5f, eac57ee, 5f3d30b, f23517e, cde8a2f, bc27a5b, e0c7e10

2. **feat(deps): 依赖检查与更新卡片**
   - npm outdated/ls 检查、安装/更新卡片、指令开关
   - 涉及提交：6e8ab43, a320d1f, 6671bb9, 4308fe2, 23e75bc, 529ae8a, 12ea517, 8fe6f5a, 4350b2b, 58784c2

3. **feat(ui): 模型聚合卡片**
   - VolcQuotaModelCards/模型聚合卡片/拖拽/K 折叠
   - 涉及提交：42b9dec, 699c84b, cdb43ea, 1376f04, f09f7b5

4. **feat(message-inject): 消息注入 + 虚拟模型匹配**
   - message_inject 插件/路由编辑器/虚拟模型匹配/能力插件重构
   - 涉及提交：3b2c94e, 03d2192, 8e9866f, 552ec97, 87fcb34, 202a68a, ebf9040, 08e9a73, 3755149, 0fefe72, 49de82b, 80830ec, 002f1f4, d89e5bb, f33667d, 78d0e5f, d2bc3a8, 65cee7a, d02e9e3, 7631254

5. **feat(model-test): 模板 meta 快照与加载回填**
   - 涉及提交：0883008, 309ca49, f52131c, 0c946ca

6. **feat(translate): 翻译功能后端插件**
   - 后端翻译插件（hash 缓存/切句/网关调用/批量/收集）
   - 涉及提交：d75915a, 1387034, d6eb06c, 4c1e4c7 等后端相关

7. **feat(translate): 前端翻译 store/页面/组件**
   - 前端 pinia store/TranslateView/TranslateText/SkillsView 接入/后台任务轮询/并发/优化
   - 涉及提交：c804fcc, d55e2a4, 1601999, b315cfe, 4614b0e, b8f66bc, b062d8a, 948d020, 8f123f1, e871f27, 39c3764, 1020695, ba8806c, 0eac286, 080bcfe, c1b58ed, 52f0cec, b52c496, bf1251a, 8343091, 07c2f13, 2922c21, 9e9bcaa, 642ab6e, 7a532b5, 6e1f6d0, 725afdf, 0274bbd, 21de2b0, 11ab5b7, 2b633b7, 83fc9c4, 5c0d73d

8. **docs: 翻译计划与前端约定文档**
   - c6da01c, 6c58c0d, 384a850

## 需要 -p 拆分的 5 个重叠文件
CapabilityRouteEditor.vue / McpPanel.vue / lib/types.ts / SkillsView.vue / registry.go
（由功能提交在各自 squash 时自动携带，若同文件两功能改动区域重叠，则用 git add -p 手动拆）

## 风险
- commit hash 全重写（已备份，安全）
- squash 冲突自动解决，极少数需人工
- 功能分组边界由用户确认
