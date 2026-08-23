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
- 已确认引入：`marked@^18.0.10`、`dompurify@^3.4.14`、`highlight.js@^11.12.0`（流式 markdown 可视化）。
- Token 估算拒绝引 `tiktoken-go`，走启发式（CJK 1 字/token、英文 4 字/token）。

## 已知陷阱

- vite dev server 启动时新依赖 `re-optimizing dependencies` 可能要 30s+；不要在依赖刚加完的 5 分钟内断 dev server。
- `marked.use({ renderer })` 在 marked v18 中支持，但**全局**生效；多实例隔离用 `new Marked()`。
- `pnpm add` 在 WorkBuddy 下会被 safe-delete require 拦截，绕开：`NODE_OPTIONS="--use-system-ca"` 前缀。

## 沟通偏好

- 中文简体，结构化输出（表格/代码块），长回复末尾 ≤300 字「说人话」收尾。
- 反 scope creep：每个实施阶段前显式确认，禁止未说「开始」前写代码。
- 每个实施阶段后做 code review，按 P0/P1/P2 分级。
