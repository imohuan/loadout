# 技能文件树 + 预览 dialog 实现计划

目标：Skills 页面点击技能名 → 弹窗，左侧该技能目录树（扁平 entries，前端自建树）、右侧文件内容预览
（md 渲染、代码高亮、带行号、大文件截断）。

## 后端（plugins/skills/service.go + plugins/admin-api/service.go）

1. `skills.Service.SkillTree(name)` → `types.SkillTree{name, root, entries[], truncated}`。
   - `resolveSkillDir(s.repoDir, name)` 定位目录（不信任前端 path）。
   - 递归 os.ReadDir，相对 root 的斜杠相对路径，排序 目录优先 + 名称。
   - 过滤 .git/.DS_Store/node_modules 等噪声；上限 5000 条 / 深度 12。
2. `skills.Service.SkillFile(name, rel)` → `types.SkillFile{path, size, truncated, binary, content}`。
   - rel 清洗（拒绝对路径/`..`）+ HasPrefix 二次防御。
   - 上限 512 KiB；NUL 检测判二进制（只回元信息不回内容）。
3. admin-api 追加 2 条路由 + 2 个 handler：
   - `GET /api/skills/{name}/tree`
   - `GET /api/skills/{name}/file?path=<相对路径>`
   - 错误：400 writeError / 500 writeServerError。

## 前端

4. `lib/types.ts`：`SkillTreeNode` / `SkillTree` / `SkillFileContent`。
5. `composables/useManagementApi.ts`：`skillTree(name)` / `skillFile(name, path)`。
6. 新建 `components/SkillPreviewDialog.vue`：
   - Dialog 壳（大宽高，p-0），`SplitPane` 左树右内容。
   - 树：递归组件 `components/skill-preview/SkillTreeNode.vue`（默认展开第一层、图标、大小）。
   - 预览：按扩展名分流 md（marked+dompurify）/ 代码（highlight.js，懒加载，行号+横向滚动）/ 文本。
   - 复制按钮、截断/二进制提示、加载态、空状态。
7. `views/SkillsView.vue`：技能名改成可点按钮打开弹窗（普通表格 + 按来源聚合的子表都支持）。

## 测试与验证

8. `plugins/skills/skills_test.go`：树结构/穿越拦截/大小上限/二进制判定。
9. `plugins/admin-api/admin_api_test.go`：tree 与 file 接口冒烟（用 svc.RepoDir() 造技能）。
10. `go test ./plugins/... ./core/...`；`npm run build`（vue-tsc 类型检查）。
