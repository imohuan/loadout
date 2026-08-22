# Loadout 管理后台 REST API 契约

> 本文档是 DESIGN.md 缺失的 API 规格的补全。端点与数据结构以代码为准：
> 路由清单见 `plugins/admin-api/service.go` 的 `Routes()`，数据模型见 `plugins/types/types.go`。
> 随着能力路由 / MCP 子代理并行开发，部分字段会演进，本文记录当前快照。

## 认证

| 类别 | 机制 | 说明 |
|---|---|---|
| `public` | 无 | 仅登录端点 |
| `session` | `Cookie: loadout_session=<JWT>` | 管理后台，登录后由服务端 Set-Cookie（HttpOnly） |
| `sk-key` | `Authorization: Bearer sk-xxx` | `/v1/*` 模型 API，不属本文件范围 |
| `mcp-key` | 自定义 header（默认 `X-Loadout-Key`） | `/mcp/*` 端点，不属本文件范围 |

## 统一响应

- 成功：直接返回 JSON 对象/数组，HTTP 200。
- 错误：`{"error":{"message":"...","type":"invalid_request_error|internal_error"}}` + 4xx/5xx。
- 渠道/密钥等敏感明文（`api_key`、完整 key）只在**创建响应**返回一次，列表/读取永不回传。

## 端点清单

### 认证
| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/login` | body `{username,password}`；成功 Set-Cookie 并返回 `{ok:true}` |
| POST | `/api/logout` | 清 Cookie |

### 概览与插件
| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/overview` | `{app, version, plugins, channels, active_preset}` |
| GET | `/api/plugins` | 插件自检结果 `{plugins:[{plugin, checks:[{name, issues:[{level,message}]}]}], count}`，每次实时重跑自检 |

### 渠道（provider）
| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/channels` | 渠道列表（含 `models` 探测结果、`models_error`） |
| POST | `/api/channels` | 创建，body `{name, base_url, api_key, enabled}`；`api_key` 落盘为 `api_key_cipher` |
| PUT | `/api/channels/{id}` | 更新；`api_key` 非空才重加密 |
| DELETE | `/api/channels/{id}` | 删除 |
| POST | `/api/channels/test` | 连通性测试，body `{id?, model?, vision?}` → `{ok, latency_ms, reply|error|body}` |
| POST | `/api/channels/{id}/refresh-models` | 触发 `/v1/models` 探测，刷新 `models` 列表 |
| POST | `/api/channels/{id}/move` | body `{direction:"up"|"down"}`，调整渠道优先级 |
| POST | `/api/channels/reorder` | body `{ids:[渠道 id 按新顺序]}`，全量重排优先级；未知/重复 id 忽略，未提交的记录追加尾部（前端按 base_url 分组整组移动用） |

### 模型测试（后台代理，规避跨域）
| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/test/models` | body `{channel_id?}` 或 `{base_url, api_key}` → `{models:[], error?}`，后台代理上游 `/models` |
| POST | `/api/test/chat` | body `{channel_id? | base_url+api_key, model, messages, stream?}`，后台代理上游 `/chat/completions`；非流式透传 JSON，流式转发 SSE；响应头 `X-Request-Id` 关联转发日志 |

> 目标二选一：`channel_id` 复用已存渠道（后端解密密钥，不回传明文），或临时 `base_url` + `api_key`（不落盘）。
> 每次 `/api/test/chat` 都会以 `request_id` 写入转发日志（route-log），可在 `/api/route-logs` 查看完整时间线。
> `GET /api/route-logs` 支持 `page`（默认 1）/`pageSize`（默认 20，上限 100）分页参数，返回 `{items: [...], total: N}`；`GET /api/route-logs/{request_id}` 返回单条完整时间线（attempts 按 step 排序）。

### 能力路由（视觉附加）
| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/capability-routes` | 路由表列表 |
| POST | `/api/capability-routes` | 追加一条 |
| PUT | `/api/capability-routes` | 整体替换 |
| DELETE | `/api/capability-routes` | body `{models, channel_ids, capability}` 定位删除 |

> `CapabilityRoute` 结构：`{models: []string, channel_ids?: []string, capability: "vision", route: "native|proxy|error", via_options: [{via_model, channel_id?}]}`。
> `models` 支持 `*` 通配与 `prefix*` 前缀匹配；`via_options` 是 proxy 时的视觉候选（顺序即兜底优先级）。
> `channel_ids` 为目标模型绑定的渠道（多选）：空 = 全渠道；含 `*` = 通用全匹配（任何渠道生效）；否则仅这些渠道上的目标模型命中路由。请求渠道取自聚合模型指定的 `__current_channel`，普通请求渠道未知时仅全渠道/通配路由命中。

### MCP 服务器
| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/mcp-servers` | 上游列表 |
| POST | `/api/mcp-servers` | 创建（自动生成 id） |
| PUT | `/api/mcp-servers` | 整体替换 |
| PUT | `/api/mcp-servers/{id}` | 单条更新 |
| DELETE | `/api/mcp-servers` | body `{id}` 删除 |
| POST | `/api/mcp-servers/test` | 测试上游连通性 |
| GET | `/api/mcp-tools` | 列出聚合的工具索引 |

### 工具状态与分组
| 方法 | 路径 | 说明 |
|---|---|---|
| GET / PUT | `/api/tools-state` | 单工具开关与分类（`[{server_id, tool_name, enabled, category, tags}]`） |
| GET / POST | `/api/groups` | 分组列表 / 创建 |
| PUT / DELETE | `/api/groups` | 整体替换 / body `{name}` 删除 |

### 密钥
| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/keys` | `{sk_keys:[...], mcp_keys:[...]}` |
| POST | `/api/keys/sk` | body `{name, models?}` → `{key, prefix}`（完整 key 仅此一次） |
| DELETE | `/api/keys/sk/{id}` | 删除 |
| POST | `/api/keys/mcp` | body `{endpoint}` → `{key}` |
| DELETE | `/api/keys/mcp` | body `{endpoint}` 关闭认证 |

### Skills 与预设
| 方法 | 路径 | 说明 |
|---|---|---|
| GET / POST | `/api/skills` | 技能清单 / 登记 body `{name, source, version}` |
| DELETE | `/api/skills/{name}` | 移除 |
| GET / POST | `/api/presets` | 预设列表 / 创建 body `{name, skills}` |
| DELETE | `/api/presets` | body `{name}` 删除 |
| POST | `/api/presets/apply` | body `{name}` 切换预设（重建链接） |

### 设置
| 方法 | 路径 | 说明 |
|---|---|---|
| GET / PUT | `/api/settings` | 运行时设置 `{active_preset, default_model}` |
| POST | `/api/change-password` | body `{old, new}` |
