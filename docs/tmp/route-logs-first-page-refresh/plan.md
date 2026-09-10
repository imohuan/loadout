# 转发日志页：定时刷新只在第一页触发

## 用户诉求
「转发日志」页有一个 3 秒定时刷新。希望**只在第 1 页**触发定时刷新，翻到其他页时不刷新。
同时前端不能丢掉「翻页回到第 1 页时立刻自动刷新」这个体验。

## 关键判断
- 需求本身是行为要求（自动刷新只在第 1 页跑），不是实现要求。
- 直接只做「不是第一页就不刷新」会带来一个副作用：翻页回第 1 页时要等最多 3 秒才更新，
  且分页组件会自动纠正越界页码回到第 1 页、而不发 update:page 事件（那种情况下定时器必须继续兜底）。
- 因此：`page === 1` 是定时刷新的**唯一开关**，另外在「用户主动翻回第 1 页」时触发一次立刻刷新。

## 落地实现
1. `frontend/src/lib/autoRefresh.ts`（新增）
   - `readAutoRefreshEnabled()` / `writeAutoRefreshEnabled()`：localStorage 开关，key `loadout:route-logs-auto-refresh`，默认开启。
2. `frontend/src/lib/reloadOnFirstPage.ts`（新增）
   - `reloadOnFirstPage(getPage, reload)`：`watch` 页码，从 >1 回落到 1 时调一次 `reload()`
     （`flush: 'post'` 等页码更新渲染完成；比较的是 watch 前后值，不会与 `refresh()` 的 `page.value` 赋值抢跑）。
3. `frontend/src/composables/useRouteLogs.ts`
   - 新增 `listStats()` → `GET /api/route-logs/stats`（日志库大小，与列表是否在当前页无关）。
4. `frontend/src/views/RouteLogsView.vue`
   - 定时刷新改为「仅第一页 + 开关开启」才跑；
   - 翻回第一页立即刷一次；
   - 「日志大小」独立成只读行，不再依赖定时器，也不再有清空按钮（清空入口统一到设置页）。
5. `frontend/src/components/route-logs/RouteLogFilters.vue`
   - 筛选行加「自动刷新」开关，并提示「仅第 1 页生效，翻到其他页会自动停」。
6. `frontend/src/components/LogRetentionCard.vue`
   - 详情里补上「清除占用」入口（此前只有转发日志页的日志大小按钮能清空 request-log 库）。
7. `frontend/src/views/ManagementView.vue`
   - 「运行设置 → 日志保留」卡片新增「转发日志自动刷新」开关（默认开启，持久化到 localStorage）。

## 验证
- `npm run build`（vue-tsc + vite build）
- `npx prettier --check` 改动文件
- 启动 dev server + Playwright 真机验证翻页行为
