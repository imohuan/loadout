# 09 - 管理后台 REST API

> 本文是管理后台（Vue SPA，登录后使用）REST API 的契约摘要。端点以 `plugins/admin-api/service.go` 为准，
> 这是当前快照，新增端点以代码为准。配套：[08-数据存储](./08-data-storage.md)、[01-架构总览](./01-architecture.md)。

## 1. 认证

| 类别 | 机制 | 说明 |
|---|---|---|
| `public` | 无 | 仅登录端点 |
| `session` | `Cookie: loadout_session=<JWT>` | 管理后台，登录后由服务端 Set-Cookie（HttpOnly） |
| `sk-key` | `Authorization: Bearer sk-xxx` | `/v1/*` 模型 API（不属本文件范围） |
| `mcp-key` | 默认 header `X-Loadout-Key` | `/mcp/*` 端点（不属本文件范围） |

所有 `/api/*` 端点除登录外都需要 `session` 认证（由 `s.session(...)` 包装）。

## 2. 统一响应

- 成功：直接返回 JSON 对象/数组，HTTP 200。
- 错误：`{"error":{"message":"...","type":"invalid_request_error|internal_error"}}` + 4xx/5xx。
- **敏感字段保护**：渠道/密钥等敏感明文（`api_key`、完整 key）只在**创建响应**返回一次，列表/读取永不回传（密钥在 `.secret` 加密，见 [08](./08-data-storage.md)）。

## 3. 端点清单（按分组）

### 认证 / 概览 / 插件
| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/login` | 登录，Set-Cookie 返回 `{ok:true}` |
| POST | `/api/sso/login` | SSO 登录（桌面端） |
| POST | `/api/logout` | 清 Cookie |
| GET | `/api/overview` | 概览：`{app, version, plugins, channels, active_preset}` |
| GET | `/api/plugins` | 插件自检结果（实时重跑） |

### 渠道（provider）
| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/channels` | 渠道列表（含 models 探测、models_error） |
| POST | `/api/channels` | 创建（api_key 落盘为密文） |
| POST | `/api/channels/probe` | 探测模型 |
| POST | `/api/channels/test` | 测试渠道 |
| PUT | `/api/channels/{id}` | 更新 |
| DELETE | `/api/channels/{id}` | 删除 |
| POST | `/api/channels/{id}/refresh-models` | 刷新模型清单 |
| PUT | `/api/channels/{id}/models` | 替换模型开关 |
| POST | `/api/channels/{id}/move` | 移动排序 |
| POST | `/api/channels/reorder` | 批量重排 |

### 能力路由（capability）
| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/capability-routes` | 列表 |
| POST | `/api/capability-routes` | 创建 |
| PUT | `/api/capability-routes` | 整体替换 |
| DELETE | `/api/capability-routes` | 删除 |

### MCP / 工具状态
| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/mcp-servers` | 上游 MCP 列表 |
| POST | `/api/mcp-servers` | 创建 |
| PUT | `/api/mcp-servers` / `/api/mcp-servers/{id}` | 整体/单条更新 |
| DELETE | `/api/mcp-servers` / `/api/mcp-servers/{id}` | 删除 |
| POST | `/api/mcp-servers/test` | 测试连接 |
| GET | `/api/mcp-servers/logs` / `/{name}/log/files` / `/{name}/log` | MCP 运行日志 |
| GET | `/api/mcp-tools` / `/api/mcp-tools/schema` | 工具列表 / schema |
| POST | `/api/mcp-tools/call` | 直接调用工具 |
| GET | `/api/mcp-invocations` | 调用记录（埋点） |
| GET/PUT | `/api/tools-state` | 单工具开关与分类 |

### 分组 / 密钥
| 方法 | 路径 | 说明 |
|---|---|---|
| GET/POST/PUT/DELETE | `/api/groups` | 分组管理 |
| GET | `/api/keys` | 密钥列表（不含明文） |
| POST | `/api/keys/sk` / `/api/keys/mcp` | 创建 sk-key / mcp-key（创建响应返回一次明文） |
| DELETE | `/api/keys/sk/{id}` / `/api/keys/mcp` | 删除 |

### 技能 / 预设
| 方法 | 路径 | 说明 |
|---|---|---|
| GET/POST/DELETE | `/api/skills` | 技能列表 / 注册 / 删除 |
| POST | `/api/skills/install` / `import-zip` / `sync` / `check-updates` / `restore` / `restore-all` | 安装/导入/同步/更新/恢复 |
| GET | `/api/skills/status` / `update-status` / `update-stream` | 技能状态/更新流 |
| GET/POST/DELETE | `/api/presets` | 预设列表 / 创建 / 删除 |
| POST | `/api/presets/apply` | 应用预设（切换生效技能） |

### 进程 / UnifyAI / 聚合 / 健康
| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/processes/stream` | 进程流（统一命令执行器 procreg） |
| POST | `/api/processes/{id}/kill` | 终止进程 |
| GET/POST/PUT | `/api/unifyai/*` | UnifyAI 平台/模型源/运行/同步 |
| GET/POST/PUT/DELETE | `/api/aggregates` | 聚合模型管理 |
| GET | `/api/model-health` / `/api/model-status` | 健康/状态 |
| PATCH/POST/DELETE | `/api/model-status/*` | 模型/渠道启停、冷却、恢复 |

### 路由日志 / 测试
| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/route-logs` | 转发日志（route_requests/route_attempts） |
| POST | `/api/test/models` / `/api/test/chat` | 模型/对话测试 |

## 4. 注意事项

- 端点会随能力演进增加（如 UnifyAI、route-log 分页等），本文是快照，最终以 `plugins/admin-api/service.go` 的 `Routes` 为准。
- 所有写操作返回的结构若含敏感字段，仅创建类返回一次明文；列表/读取永远脱敏。

## 下一步

- 看部署与运维 → [10-部署与排错](./10-deployment.md)
- 看数据落盘 → [08-数据存储](./08-data-storage.md)
