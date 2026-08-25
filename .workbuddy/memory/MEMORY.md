# Project Memory (loadout)

> 短期记忆进 `YYYY-MM-DD.md`；这里只放长期事实。详见当日文件。

## 架构

- Go 后端 module `apps/server`，默认 :3000；`apps/desktop` 是 Wails 壳。
- Plugins 走 capability 扩展（如 `plugins/field-filter` 嵌套 `field_rules_json`）。
- Frontend 用 shadcn-vue-CDN（**注意组件全局可用**），禁止手写重复组件。
- 流式日志：从 `response_json.body`（SSE 原文拼接串）解析，最大 32MB；超过标 `truncated:true`。

## 视觉规范（自己沉淀，UI 设计请遵循）

- 徽标 / 指标 tint：`bg-{color}-500/15 text-{color}-700 dark:text-{color}-300 border-{color}-500/20`。
- markdown 容器：`.markdown-body`（位于全局 `<style>`，用项目 token 而非 gray-*）。
- 嵌套分组：用 `step_no` 点列式（1 / 1.1 / 1.2），不引入 parent 列。
- 折叠块统一用 `StreamCollapsibleBlock`（这是新基类，**已取代** col-Accordion 在流式面板场景的使用）。

## 依赖策略

- 用户偏好「零依赖优先」，引入新依赖前**必须显式确认**（AskUserQuestion 列出三个选项）。
- 已确认引入：`marked@^18.0.10`、`dompurify@^3.4.14`、`highlight.js@^11.12.0`（流式 markdown 可视化）、`vue-draggable-plus@^0.6.1`（VolcQuotaCard 关注区拖拽重排；间接依赖 sortablejs）。
- Token 估算拒绝引 `tiktoken-go`，走启发式（CJK 1 字/token、英文 4 字/token）。

## 已知陷阱

- vite dev server 启动时新依赖 `re-optimizing dependencies` 可能要 30s+；不要在依赖刚加完的 5 分钟内断 dev server。
- `marked.use({ renderer })` 在 marked v18 中支持，但**全局**生效；多实例隔离用 `new Marked()`。
- `pnpm add` 在 WorkBuddy 下会被 safe-delete require 拦截，绕开：`NODE_OPTIONS="--use-system-ca"` 前缀。
- **shadcn-vue `SelectItem` 的 value 不能是空串**（`A <SelectItem /> must have a value prop that is not an empty string`）——空串是框架保留的"清除选择显示 placeholder"语义。做"全部"选项用 `value="all"` 占位，在 `@update:model-value` 里 `$event === 'all' ? '' : $event` 映射回空串表示不过滤。
- **Vite + Go embed 组合**：Vite build 必输出 `_plugin-vue_export-helper-*.js`（vue plugin 内部 vendor 拆分），Go `//go:embed dist` **默认过滤下划线开头的文件**，会让这个 chunk 默默丢失→ 浏览器报 404。**修复**：必须写 `//go:embed all:dist`。任何把 vite dist 嵌入 Go 的 embed.FS 都要用 `all:` 前缀。下次跑前端 build 后，记得 `go build` 一下才会重新 embed。
- **`git fetch` 后 tracking ref 消失（`origin/master [gone]`）**：本环境对 `.git/refs/remotes/` 下 git 自身的写入（lock+rename）有系统级拦截——fetch 报成功、reflog 正常，但 `refs/remotes/origin/` 目录整体消失；`refs/heads/`/`refs/tags/`/`refs/notes/` 写入正常，唯独 `refs/remotes/`。非磁盘/非 git 版本/非 fscache/非 ACL 问题。**workaround**：fetch 一律用 `bash scripts/git-sync-remotes.sh`（fetch + `ls-remote` 重建 tracking ref），或 fetch 后手动 `printf '<sha>' > .git/refs/remotes/origin/master`。详见 2026-08-24 日志。
- **⚠️ 升级版事故（2026-08-24 凌晨，整个 git 仓库数据被删）**：本环境拦截不只动 `refs/remotes/`——本次**整个 `.git/refs/`（含 heads）+ `.git/objects/pack/*.pack` + 部分 loose objects 一起消失**，`git status` 直接报 `not a git repository`。**远端是唯一可靠备份，本地 commit 务必当天 push**。完整恢复流程（锚定备份 commit → fetch 拉回 → 清坏索引 → 删脏 index → reset → expire reflog）见 2026-08-24 日志。**教训：每天结束前 push，别囤本地 commit。**
- **vue-draggable-plus 三个坑**（装这个库时必踩）：
  1. **`ghost-class` / `drag-class` / `chosenClass` 必须单 token**——库内部直接 `classList.add(value)`，传 `"opacity-40 border-dashed border-primary/40"` 这种带空格的串会抛 `InvalidCharacterError: Failed to execute 'add' on 'DOMTokenList'`，拖拽一启动就崩。**做法**：单类名直接传 Tailwind 单类（`ghost-class="opacity-0"` 隐藏占位、`drag-class="shadow-xl"` 加阴影）；要多类样式 → 自定义单类名（如 `volc-dragging`）+ 非 scoped `<style>` 块定义。
  2. **sortable 克隆的 drag 元素不带 Vue scoped data-v 属性** → 组件内 `<style scoped>` 匹配不到。给 drag/chose/ghost 加视觉反馈必须用**非 scoped `<style>`**（或 `:global()`），类名取独特前缀避免全局污染。
  3. **computed 只读不能直接 v-model**——VueDraggable 的 v-model 要可写数组。**做法**：加 `ref` 数组 `watch(computed, v => refArr = v, { immediate: true })` 跟随同步，VueDraggable 绑 ref；`@update:model-value` 回调里把新顺序写回源头（localStorage 等），别用 v-model 让库自己改 ref（数据流不清晰、computed 写不动）。

## Wails 桌面端

- **打包入口**：`scripts/pack-desktop.ps1`（项目根）是唯一正式打包脚本：构建根 `frontend/` → robocopy /MIR 到 `apps/desktop/frontend/dist`（`go:embed all:frontend/dist` 落点）→ rsrc 图标 → `go build -tags production` → `apps/desktop/dist/loadout-desktop.exe`（内嵌 Loadout Server）。
- `apps/desktop/scripts/pack.ps1` 是 Wails 模板遗留（打 `apps/desktop/frontend/` 那套 `myapp-frontend` 空壳），不用。
- 桌面版 UI 与网页版是同一套代码（根 frontend/），desktop 不维护独立前端。
- **免二次登录（SSO）**：WebView2 与浏览器 cookie 存储物理隔离，登录态无法共享；托盘「打开网页」走 `?sso=<30s JWT>` → 网页版 `POST /api/sso/login`（限 127.0.0.1）换完整会话 cookie。桌面签 token 用 `store.New(config.DataDir)` + `auth.SignToken`（`tray.go` 的 `ssoWebURL()`）。后端接口在 `plugins/admin-api`（handleSSOLogin）。
- `apps/desktop/backend/app/runner.go` 用 `application.BundledAssetFileServer(assets) + server.SPAFallback(...)` 三层包：API 代理（`/api`/`/v1`/`/mcp` → :3000）+ SPA history 路由 fallback + 文件服务。任何想在桌面端加新路径，先看这三层是否已覆盖，再决定要不要碰。
- SPA fallback 实现：`apps/desktop/backend/server/spa.go`（buffered ResponseWriter，只对「404 + 末段无扩展名」改写 index.html；资源 404 透传）。复用即可，不要重写。
- **embed.FS 找 index.html 的两个坑**（很容易踩，配合 vite-go-embed-trap skill 看）：
  1. `//go:embed dist` 默认排除下划线开头文件——Vite 的 `_plugin-vue_export-helper-*.js` 会丢。要用 `all:dist`。
  2. `//go:embed all:frontend/dist` 后 fsys 根是 `frontend/dist/*`，路径要带 `dist/` 前缀或先 `fs.Sub("dist")`。直接 `fs.ReadFile(fsys, "index.html")` 必失败——这就是 SPA fallback 第一版退回透传的根因。改用「先尝试直读、再 WalkDir 找 + Sub + ReadFile」的双形态兜底（spa.go 内 readIndexHTML）。
- 桌面端快捷键 vs 全局快捷键：
  - 应用前台（webview 焦点在前）→ `WebviewWindowOptions.KeyBindings: map[string]func(window Window){...}`，wails 内置 accelerator 解析，跨平台不用改。
  - 系统级（即使应用不在前台）→ `app.GlobalShortcut.Register(...)`，需要 OS 注册（Windows RegisterHotKey / macOS NSEvent / Linux DBus），仅在确实需要「全局触发」时使用，否则优先 KeyBindings。
- 桌面端右键菜单：`WebviewWindowOptions.DefaultContextMenuDisabled: true`（wails 一行配置），不要去前端 `oncontextmenu preventDefault`——webview 内置菜单是 Chromium 自己绘的，前端拦不住也拦不到。

## 沟通偏好

- 中文简体，结构化输出（表格/代码块），长回复末尾 ≤300 字「说人话」收尾。
- 反 scope creep：每个实施阶段前显式确认，禁止未说「开始」前写代码。
- 每个实施阶段后做 code review，按 P0/P1/P2 分级。
