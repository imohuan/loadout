# 08 - 运行时数据存储

> 本文讲 Loadout 的运行时数据怎么落盘。重点纠正一个常见误解：**当前不是"纯 JSON"，而是 JSON + SQLite 双轨**。
> 配套：[01-架构总览](./01-architecture.md)、[09-管理后台 API](./09-admin-api.md)。

源码：`core/store/`（JSON 原子存储）、`core/db/`（SQLite 连接与迁移）、`core/servercore/server.go`（装配与导入）。

## 1. 双轨概览

| 存储 | 包 | 承载内容 | 特点 |
|---|---|---|---|
| JSON store | `core/store` | 敏感配置（渠道 key、API key、管理员密码、`.secret`） | 原子写（tmp+rename）、进程内锁、AES-GCM 加密字段 |
| SQLite | `core/db`（modernc.org/sqlite，单连接） | 结构化业务数据（路由、模型状态、日志、MCP、技能、预设等） | 版本化 schema 迁移、查询友好 |

> 旧文档（如 `docs/archive/DESIGN.md`）曾描述为"纯 JSON 存储"——**那已过时**。当前 SQLite 才是主存储，
> JSON store 主要用于仍需加密/兼容的敏感配置与 `.secret` 本地密钥。

## 2. `~/.loadout/` 布局

```
~/.loadout/
├── data/                 # 运行时数据根（DataDir，由 config 派生，默认 ~/.loadout/data）
│   ├── .secret           # 32 字节本地密钥（0600），AES 加密 + JWT 签名复用
│   ├── channels.json     # 上游渠道（仍保留 JSON，敏感 APIKey 加密）
│   ├── api_keys.json     # 模型 API key（sk-，哈希 + 密文）
│   ├── mcp_keys.json     # MCP endpoint key
│   ├── users.json        # 管理员账号
│   ├── capability_routes.json  # 能力路由表（结构见 plugins/types.CapabilityRoute）
│   ├── mcp_servers.json  # 上游 MCP 服务器
│   ├── tools_state.json  # 工具开关/分类
│   ├── groups.json       # 分组
│   ├── skills.json       # 技能仓库清单
│   ├── presets.json      # 技能预设
│   ├── settings.json     # 运行时设置（当前预设等）
│   ├── aggregates.json   # 聚合模型
│   ├── model_health.json # 模型健康状态（持久化，启动恢复）
│   └── *.db / sqlite      # SQLite 主库（channels/channel_models/... 见下）
├── skills/               # 技能完整仓库（永不删除）
├── logs/                 # loadout.log（轮转）
├── backups/              # 备份
└── admin-password       # 首启随机密码（改密后自动删除）
```

## 3. 启动迁移（ImportJSON）

`servercore.assemble` 在装配插件前：

1. `db.OpenForStore(st)` 打开 SQLite（路径在 DataDir 下）。
2. `db.ImportJSON(ctx, database, st)` 把旧 JSON 配置**导入 SQLite**（建表 + 数据迁移）。
3. `db.ImportAdminJSON(ctx, database, st)` 导入管理员相关 JSON。
4. 之后业务插件（mcp-hub、skills、model-health 等）用 `db.NewRepository(database)` 读写 SQLite；
   失败时降级回 JSON store 并记 warn。

> 因此"配置从哪来"的真相是：JSON 是兼容/加密层，SQLite 是主存储，启动时一次性把 JSON 迁进 SQLite。

## 4. SQLite 表清单（core/db/migrate.go）

主要表（以迁移脚本为准，可能随版本增加）：

- `channels` / `channel_models` —— 上游渠道与模型探测结果
- `aggregates` / `aggregate_targets` —— 聚合模型与 targets
- `channel_states` / `model_states` —— 渠道/模型健康状态
- `route_requests` / `route_attempts` —— 转发日志（每次请求/每次渠道尝试）
- `capability_routes` —— 能力路由表
- `mcp_servers` / `mcp_groups` —— 上游 MCP 与分组
- `tools_state` —— 单工具开关与分类
- `skills` / `presets` / `settings` —— 技能/预设/设置
- `gateway_keys` / `users` —— sk-key / mcp-key / 管理员
- `volc_quota_config` / `volc_quota_models` / `volc_quota_usage` / `volc_quota_packages` —— 火山免额聚合

迁移是**版本化**的（`schema_migrations` 表记录已应用版本），重启时只跑未应用的迁移。

## 5. JSON store 的加密与原子写

- 所有数据文件用"先写 `name.tmp` 再 `rename` 覆盖 `name`"的原子写，避免半截文件。
- 读/写/删/存在性判断都在进程内加锁，保证并发安全。
- `.secret` 为 32 字节随机密钥（权限 0600），首次自动生成，供 AES-256-GCM 加密字段与 JWT 签名复用。
- 敏感明文（如 `api_key`、完整 key）只在**创建响应**返回一次，列表/读取永不回传（见 [09-管理后台 API](./09-admin-api.md)）。

## 下一步

- 看这些数据的读写 API → [09-管理后台 API](./09-admin-api.md)
- 看部署与数据目录配置 → [10-部署与排错](./10-deployment.md)
