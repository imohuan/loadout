# Loadout 启动流程架构：后台化与非阻塞启动

> 一句话定位：服务上线永远是第一优先级、秒级就绪；skills 文件监听、MCP 进程拉起等"杂活"全部后台执行，卡住也不阻塞服务启动。

## 背景与问题

早期 Loadout 启动是**一条同步直线排队**：任何一环慢，后续全部堵住。

```
开数据库 → 装配插件 → (逐个 Apply) → 启动 MCP 进程(≤30s) → 监听端口 → 服务可用
                                │
                     └── skills 插件抓 fsnotify 监视权 ← 曾在此卡死
```

- **skills 插件**在装配路径里同步执行 `fsnotify.NewWatcher()` + `WalkDir` 递归注册全部技能目录。旧实例刚退出、句柄未释放时，这一步可能长时间阻塞，服务起不来。
- **MCP 进程**在装配路径里同步 `StartEnabled`，等全部拉起（最多 30s）才继续。

## 目标

1. **服务秒级上线**：启动到监听端口不依赖任何"杂活"完成。
2. **杂活后台化**：skills 监听、MCP 拉起失败/卡住都不阻塞服务。
3. **可观测**：后台工作（尤其 MCP）有明确的成功/失败/汇总日志。

## 现状：启动路径分层

```
同步、必须等：  db 打开 → 导入配置 → 插件装配(轻量) → 监听端口 → 服务可用
后台、不等：    skills 文件监听初始化 + 技能全量同步
                MCP 进程拉起（stdio 常驻）
```

- **服务响应** 是唯一必须等待的路径。
- skills / MCP 全部后台执行，主流程零等待。

## 实现手段

### 1. 通用后台执行工具 `core/plugin/background.go`

`plugin.RunBackground(name string, fn func() error) <-chan error`

- 开 goroutine 立即返回，主流程不阻塞。
- goroutine 内 panic 会被 recover 并记为 `slog.Error`，**绝不向上崩溃进程**。
- 返回有缓冲(1)的 error channel：调用方可"完全不等"或"select 超时等结果"。

### 2. skills 插件完全后台化（`plugins/skills/`）

`Watcher.Start()` 中：

- `initListen()`（fsnotify 初始化 + 递归注册 + 事件循环）→ `go w.initListen()` **零等待**。
- `pollLoop()`（定时全量扫描）→ 独立 goroutine。
- **启动即全量同步** → 独立 goroutine，负责把目标目录全部技能复制进技能库；可被 `Stop` 中断。

关键点：

- **fsnotify 卡住不再阻塞**：装配路径不检查它的结果、不等它。技能同步靠"启动全量同步 + 定时轮询"兜底，无强制即时要求。
- **`Stop` 幂等且防时序**：`stop/done/syncDone` channel 在 `NewWatcher` 构造时创建（而非 `Start`），即使后台 goroutine 尚未运行就卸载，`Stop` 也能安全调用。
- **卸载不残留写入**：`Stop` 会等待启动全量同步收尾（`syncDone`），避免退出/测试清理时后台仍在写技能库目录导致目录删不掉。

### 3. MCP 进程后台化（`core/servercore/server.go` + `plugins/mcp-hub/`）

`assemble` 中把 `hub.StartEnabled(ctx)` 包进 `plugin.RunBackground`，服务先监听、MCP 后绪。`StartEnabled` 内部逻辑不变（并行 Connect、单个失败只记日志、状态由前端 `ServerStatus` 展示），仅补充日志：

```
mcphub: 启动恢复：无启用的 MCP 服务器，跳过
mcphub: 启动恢复：开始拉起  count=N
mcphub: 启动恢复拉起成功  server=xxx        （每个服务器一条）
mcphub: 启动恢复拉起失败  server=xxx err=.. （每个服务器一条）
mcphub: 启动恢复完成  total=N ok=N failed=N
```

## 验证与效果

- `go build ./...`、`go test ./...`、`go vet ./...` 全绿；关键包 `-race` 检测通过。
- 隔离实测（150 技能）：`启动 app` → `监听 addr` → `技能同步` **同一秒**完成；无任何"初始化超时"日志。
- skills 监听即便偶发卡住，服务照常上线，技能由后台同步/轮询兜底。

## 相关 commit

- `79c3435` 新增 `plugin.RunBackground` 后台执行工具（panic 兜底）
- `3c79e82` skills watcher 启动后台化，fsnotify 不再阻塞装配
- `24a007a` skills 插件装配不再等待监听器就绪
- `cfc9f43` mcp-hub 启动恢复后台化，不再阻塞服务监听
- `3828051` watcher 全量同步可被 Stop 中断，卸载时不再残留写入
- `adbd7ef` skills 监听完全后台化、零等待，移除就绪窗口
- `8713cce` mcp-hub 启动恢复增加成功/失败/汇总日志

## 注意事项 / 边界

- **后台化 ≠ 丢状态**：MCP 失败状态仍经 `/api/mcp-servers` 展示，后台启动只是不再阻塞服务。
- **技能同步兜底**：启动瞬间的技能变化由"启动全量同步"和"定时轮询"覆盖，实时事件监听晚到不影响技能库最终一致。
- **不改动既有工作**：桌面端 `apps/desktop` 的未提交改动不属本文档范围。
