# 01 - 架构总览

> 读完本文，你应理解 Loadout 的整体分层、三类入口、启动流程与目录布局。
> 插件系统是本项目的核心，请接着读 [02-插件系统](./02-plugin-system.md) 与 [03-插件开发指南](./03-plugin-dev-guide.md)。

## 1. 总体架构

```
┌───────────── 客户端 / Agent ─────────────────────┐
│  OpenAI 兼容 API (sk- key)    MCP (header key)   │
└──────────────┬───────────────────────┬───────────┘
               ▼                       ▼
┌──────────────────────────────────────────────────┐
│                    Loadout (Go)                   │
│  ┌────────────┐  ┌────────────┐  ┌─────────────┐ │
│  │ 模型网关插件 │  │ MCP 聚合插件 │  │ skills 插件  │ │
│  │ + 视觉适配  │  │ status/get/ │  │ 仓库+预设    │ │
│  │            │  │ invoke     │  │             │ │
│  └────────────┘  └────────────┘  └─────────────┘ │
│  ┌────────────────────────────────────────────┐  │
│  │ core：插件框架 / 日志 / 存储 / 认证 / 装配   │  │
│  └────────────────────────────────────────────┘  │
│  ┌────────────────────────────────────────────┐  │
│  │ 管理后台（Vue 3，需登录）                    │  │
│  └────────────────────────────────────────────┘  │
└──────────┬───────────────────────┬───────────────┘
           ▼                       ▼
     上游模型 API             上游 MCP Server
```

**分层铁律**：`core/` 放零业务逻辑的框架（插件框架、日志、存储、认证、装配、MCP 封装），
业务全部以**插件**形式挂在 `plugins/`，`core` **从不 import 任何业务包**。

## 2. 三类入口（单端口 `:3000`）

| 路径 | 入口 | 认证（AuthKind） | 说明 |
|---|---|---|---|
| `/v1/*`、`/v2/*` | 模型 API（OpenAI 兼容） | `sk-key` | `Authorization: Bearer sk-xxx` |
| `/mcp/*` | MCP 端点（status/get/invoke） | `mcp-key` | 默认 header `X-Loadout-Key` |
| 其余 | 管理后台（Vue SPA） | `session` | 登录后 JWT Cookie（HttpOnly） |

认证中间件在 `core/servercore/server.go` 的 `assemble()` 中按 `RouteSpec.Auth` 自动挂载；
`public` 类别不挂任何中间件。详见 [02-插件系统](./02-plugin-system.md) 的路由注册。

## 3. 启动流程

入口：`core/servercore/server.go` 的 `Run()` → `assemble()`。关键步骤：

1. `store.New(DataDir)` 打开 JSON 数据目录（生成/加载 `.secret` 本地密钥）。
2. `db.OpenForStore(st)` 打开 SQLite（路径在 DataDir 下）。
3. `db.ImportJSON` / `db.ImportAdminJSON` 把旧 JSON 配置**导入 SQLite**（双轨迁移）。
4. `plugin.Load(plugins.All(), opts)` 装配全部插件：
   - 基础服务预注册：`store` / `logger` / `http-client` / `db`。
   - 框架按 `Manifest.Inject/Provide` **拓扑排序**后逐个 `Apply`（详见 [02](./02-plugin-system.md)）。
5. 取出 `gateway-keys` / `admin-auth` / `mcp-hub` 服务，注入插件计数与自检提供者。
6. `auth.EnsureFirstRun()` 首启生成随机管理员密码（`~/.loadout/admin-password`）。
7. 用 `http.ServeMux` 挂载：插件路由（按 Auth 挂中间件）+ `/mcp/`（StreamableHTTP 动态分发）+ `/`（Vue SPA fallback）。
8. 后台 `mcp-hub-start` 拉起所有 enabled 的 stdio MCP 进程（失败只记日志，不阻断启动）。
9. `requestIDMiddleware`（注入/复用 `X-Request-Id`、panic 兜底、访问日志）+ CORS 包裹；
   监听（桌面模式仅 `127.0.0.1`）并等待信号/可编程 `TriggerShutdown` 优雅退出（先终止子进程、再断 SSE 长连接）。

## 4. 目录布局

```
loadout/
├── core/            # 框架（不 import 业务）
│   ├── plugin/      # 插件框架：Manifest / Context / 装配 / 自检 / 路由
│   ├── servercore/  # 启动逻辑（apps/server 与 apps/desktop 共用）
│   ├── config/      # 程序级配置（LOADOUT_* 环境变量优先）
│   ├── store/       # JSON 原子存储 + AES 加密 + .secret
│   ├── db/          # SQLite 连接 + 版本化 schema 迁移
│   ├── auth/        # 认证（session/JWT、sk-key、mcp-key）
│   ├── logger/      # 日志（文本格式 + 轮转）
│   ├── mcpkit/      # MCP SDK 封装
│   ├── linkfs/      # 跨平台链接（symlink/junction/复制）
│   └── procreg/     # 统一子进程注册器（启动/终止/监控）
├── plugins/         # 全部业务插件（业务在此）
│   ├── registry.go  # All() 集中登记所有插件 ← 新增插件要改这里
│   ├── model-gateway/  # /v1 转发核心 + 事件 hook
│   ├── vision_v2/      # 视觉能力附加
│   ├── aggregate/      # 聚合模型轮询 + failover
│   ├── model-health/   # 健康检查
│   ├── mcp-hub/        # MCP 聚合网关
│   ├── skills/         # 技能仓库 + 预设
│   ├── admin-api/      # 管理后台 REST API
│   ├── admin-auth/     # 后台认证
│   ├── gateway-keys/   # sk-key / mcp-key 管理
│   ├── field-filter/   # 字段过滤能力
│   ├── sensitive-filter/ # 敏感词过滤能力
│   ├── route-log/      # 转发日志
│   ├── request-log/    # 完整请求日志
│   └── volc-free-quota/# 火山免额聚合刷新
├── apps/
│   ├── server/      # 纯服务版 main（Linux）
│   └── desktop/     # Wails 桌面壳（Windows）
├── frontend/        # Vue 3 管理后台（dist 由 servercore 内嵌）
├── docs/guide/      # 本学习文档集
└── docs/archive/    # 旧文档归档（历史参考）
```

## 5. 配置（环境变量优先）

所有程序级配置在 `core/config/config.go`，`LOADOUT_*` 环境变量优先、默认值兜底：

| 变量 | 默认 | 含义 |
|---|---|---|
| `LOADOUT_RUN_MODE` | `server` | `server`（全网卡）/ `desktop`（仅 127.0.0.1） |
| `LOADOUT_SERVER_ADDR` | `:3000` | 监听地址 |
| `LOADOUT_UPSTREAM_TIMEOUT` | `300s` | 转发上游超时（含流式全程） |
| `LOADOUT_SMART_GROUP_HEADER` | `X-Loadout-Group` | `$smart` 端点指定分组的 header |
| `LOADOUT_APP_NAME` / `LOADOUT_VERSION` | `Loadout` / `0.1.0` | 应用名 / 版本 |

运行时数据（渠道、路由、MCP、key、预设等）落在 `~/.loadout/` —— 见 [08-数据存储](./08-data-storage.md)。

## 下一步

- 想懂插件怎么工作 → [02-插件系统](./02-plugin-system.md)
- 想自己加一个插件 → [03-插件开发指南](./03-plugin-dev-guide.md)
