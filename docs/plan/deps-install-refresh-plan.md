# 依赖安装成功后状态不刷新 —— 根因修复计划

## 现象

设置页点击「安装/更新」依赖，安装成功（npm 已装好）但页面状态不刷新，仍显示「未安装」，需手动点「刷新状态」按钮才能看到最新。

## 根因（已用代码链路确认）

问题在后端，不在前端收尾机制。

前端 `ManagementView.vue` 的 `installDep` 用 `registerTask(taskId, { onDone })` 注册收尾回调：
安装完成后经 procreg → SSE → `onDone` 触发 → 调 `refreshDeps()` → `api.depsStatus()` → 后端 `handleDepsStatus`。
**而 `handleDepsStatus` 返回的是缓存 `s.depsCache`，不是实时查询。**

后端 `handleDepsInstall` 启动安装（`go func` → `deps.Install`）后，**从没在安装完成时刷新 `s.depsCache`**。
于是安装成功但缓存仍是旧的「未安装」，`onDone` 读缓存自然刷新不出新状态。

对照：
- 「刷新状态」按钮 → `checkDeps` → `/api/deps/refresh` → `handleDepsRefresh` 实时 `deps.CheckAll` → 能看到最新 → 印证缓存是旧的那个环节断掉了。

## 修复方案（最小改动）

### 后端（治本）
`plugins/admin-api/service.go` 的 `handleDepsInstall`：在 `go func` 里 `deps.Install` 返回后（无论成败）调用 `s.RefreshDeps()` 刷新缓存。
- `RefreshDeps` 已有 `depsChecking` 防重入 + 并发安全，直接复用。
- 成功后缓存更新 → 前端 `onDone` → `refreshDeps()` 读缓存即拿到最新状态。

不改前端：前端收尾读缓存的逻辑本就合理（轻量），只需让缓存与真实安装同步。

## 验证

1. 后端 `go build ./...` 编译通过。
2. 设置页点击安装，等待完成后页面状态自动从「未安装」变为「已是最新」。
3. 安装失败场景（断网/错名）状态应同样刷新。
