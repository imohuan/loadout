# 07 - Skills 仓库与预设

> 本文讲 Loadout 的"技能"体系：技能仓库怎么存、预设怎么切换、仓库怎么链接到 agent 的目标目录。
> 配套：[06-MCP 聚合](./06-mcp-hub.md)、[01-架构总览](./01-architecture.md)。

源码：`plugins/skills/`（plugin.go / service.go / watcher.go）、`core/linkfs/`。

## 1. 三个概念

| 概念 | 存储 | 说明 |
|---|---|---|
| 技能仓库 | `~/.loadout/skills/`（skills.json） | 完整技能清单，**永不删除**（仓库是源头） |
| 预设 | `~/.loadout/data/presets.json` | 一组"哪些技能生效"的配置，可一键切换 |
| 当前设置 | `~/.loadout/data/settings.json` | 记录当前生效的预设 |

目标目录：`~/.agents/skills/`（agent 真正读取技能的地方）。

## 2. 核心机制：仓库与目标目录分离 + 链接

- 仓库（`~/.loadout/skills/`）是**完整副本**，永远保留。
- 当前预设决定"哪些技能生效"，通过**链接**把它们放到目标目录（`~/.agents/skills/`）。
- 切换预设 = 重新链接，不影响仓库内容。

### 跨平台链接（core/linkfs）

`linkfs.Link(src, dst)` 按以下顺序尝试，自动降级：

1. **symlink**（符号链接，Linux 首选 / Windows 有权限时）
2. **junction**（Windows 目录联接 `mklink /J`，免管理员）
3. **copy**（递归复制目录，前两者都不可用时兜底）

这样同一套技能在 Linux（symlink）和 Windows（junction/copy）都能工作，且切换预设时不重复占用磁盘。

## 3. 监听与热更新（plugin.go）

skills 插件支持两种监听（由 `config` 开关控制）：

- `SkillWatchRecursive`：递归监听仓库变化。
- `SkillWatchPolling`：定时全量扫描（无法用文件系统事件的场景）。

监听器通过 `ctx.Effect(w.Stop)` 注册——**插件卸载时框架自动逆序停止监听**，无需手动清理。
监听初始化完全后台化（`plugin.RunBackground`），装配路径不等它，失败只记日志，服务照样秒级上线。

```go
if config.SkillWatchRecursive || config.SkillWatchPolling {
	w := NewWatcher(svc, ...)
	ctx.Effect(w.Stop)                 // 卸载时自动停止
	plugin.RunBackground("skills-watcher", w.Start)
}
```

## 4. SQLite 与 JSON 双存储

skills 插件优先用 SQLite 仓储（`db.NewRepository`），失败时降级到 JSON 存储（`core/store`）并记 warn。
技能/预设/设置的读写都经由 Service，保证两套存储语义一致。

## 下一步

- 看运行时数据怎么落盘（JSON + SQLite 双轨）→ [08-数据存储](./08-data-storage.md)
- 看管理后台怎么管理技能/预设 → [09-管理后台 API](./09-admin-api.md)
