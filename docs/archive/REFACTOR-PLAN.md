# 模型路由与数据层全面重构计划

状态：已评审，待确认后实施

目标：将渠道、模型状态、模型路由、聚合目标和转发日志统一迁移到 SQLite，并新增独立的模型状态插件、模型状态页面和转发日志页面。

## 0. 前置阶段：插件框架契约与生命周期整改

在执行下面的数据库和路由重构前，先修正插件框架本身的生命周期语义。这个阶段只处理 `core/plugin` 及现有插件的装配契约，不改变 SQLite 表结构、路由业务规则或前端范围。

### 0.1 必须完成

1. 插件在 `Apply` 期间通过 `ctx.Set`、`ctx.On`、`ctx.RegisterRoute`、`ctx.RegisterCheck` 注册的内容，自动归属当前插件，并在插件卸载时逆序清理。调用方不需要手动保存每个 disposer。
2. 修正 `ctx.Effect` 的幂等语义：手动调用 disposer 后，`Assembly.Unload()` 不得再次执行同一个清理函数。
3. 严格校验服务契约：
   - `Manifest.Provide` 中声明的每个服务必须在 `Apply` 成功后真实注册；
   - 禁止插件重复注册同名服务；
   - 禁止覆盖基础服务（如 `store`、`logger`），除非未来增加明确的替换机制。
4. 为有后台任务的插件提供可取消生命周期。当前至少覆盖 aggregate 的健康检查 ticker；卸载、装配失败和测试结束时都必须停止任务。
5. 为插件服务和跨插件事件整理稳定的 contracts 接口。使用方优先依赖小接口和公共 payload 类型，不直接依赖其他插件的具体实现类型。
6. 增加框架回归测试：卸载后服务、事件监听、路由和自检项不可继续生效；未真实提供服务时装配失败；重复清理只执行一次；后台任务可以停止。

### 0.2 与后续阶段的边界

- 本阶段不迁移业务数据，不新增 SQLite 表，不实现 model-health 或 route-log。
- 阶段 1 以后新增的插件必须遵守本阶段确定的服务注册、事件 contracts 和生命周期规则。
- 阶段 3 的 model-health、阶段 5 的 route-log 仍负责各自的业务状态和日志，不把业务逻辑塞回插件框架。

### 0.3 完成标准

- `go test ./core/plugin ./plugins/...` 中与插件装配相关的测试通过；
- `go vet ./...` 和 `go build ./...` 通过；
- 可用测试证明 `Assembly.Unload()` 后不会残留服务、事件监听、路由、自检项或后台 goroutine；
- 插件之间只通过声明的服务和事件 contracts 协作，后续重构不再新增跨插件具体类型断言。

## 1. 当前问题

- 运行时配置散落在多个 JSON 文件，跨文件更新没有事务与关联约束。
- model-gateway 既查找渠道又转发请求；aggregate 又单独维护健康状态。
- 同名模型在多个渠道中出现时，状态、开关、选择与失败切换关系不够清楚。
- 渠道尝试过程没有统一结构化日志，前端无法还原完整转发流。
- 无法可靠区分单模型或下游余额不足，与整个渠道账户余额不足。

## 2. 重构范围

本次必须完成：

1. SQLite 成为运行时数据的唯一写入来源。
2. JSON 仅用于首次导入和备份，不长期双写。
3. 新增 model-health 插件，统一维护渠道级、模型级状态和健康策略。
4. 普通模型可在同名模型的多个渠道之间自动切换。
5. 聚合模型可按模型加指定渠道的目标优先级切换。
6. 每次请求和每次尝试均以 request_id 关联并记录。
7. 前端新增模型状态、转发日志两个 Tab。
8. 渠道增加 sync_billing 配置。

明确不做：多机共享数据库；启动时探测所有模型；记录请求正文或密钥；把所有 HTTP 402 自动判为渠道级禁用。

## 3. 模块职责

调用链：

    客户端
      -> model-gateway
           -> route planner：生成候选模型和渠道
           -> model-health：过滤可用项，记录成功和失败
           -> upstream adapter：执行一次上游请求
           -> route-log：持久化请求和每个步骤
           -> aggregate：虚拟模型的目标顺序

| 模块 | 负责 | 不负责 |
|---|---|---|
| model-gateway | 解析请求、尝试上游、普通模型渠道切换 | 健康策略和聚合状态存储 |
| model-health | 状态判断、失败分类、冷却、禁用、定时检查 | 聚合目标顺序 |
| aggregate | 虚拟模型与目标顺序 | 自己保存健康状态、判断余额错误 |
| route-log | 请求和每次尝试的日志、查询、清理 | 影响路由决定 |
| admin-api | 管理接口、鉴权、返回前端对象 | 直接操作业务 SQL |
| core/db | SQLite 连接、迁移、事务、加密支持 | 业务规则 |

aggregate 最终不再拥有 healthMap、model_health.json 写入、后台健康检查和错误分类规则。

## 4. SQLite 数据层

数据库路径：

    ~/.loadout/loadout.db

启动顺序：创建目录，打开数据库，设置 WAL/外键/忙等待，执行版本化迁移，首次导入旧 JSON，最后装配业务插件。

数据库设置：

    PRAGMA journal_mode = WAL;
    PRAGMA foreign_keys = ON;
    PRAGMA busy_timeout = 5000;

API Key 仍使用现有 AES-GCM 加密；保留 .secret，密文直接写入 SQLite，不发生明文往返。

### 4.1 配置表

channels：渠道基础配置。

    id TEXT PRIMARY KEY
    name TEXT NOT NULL
    base_url TEXT NOT NULL
    api_key_cipher TEXT NOT NULL DEFAULT ''
    manual_enabled INTEGER NOT NULL DEFAULT 1
    sync_billing INTEGER NOT NULL DEFAULT 0
    models_error TEXT NOT NULL DEFAULT ''
    created_at TEXT NOT NULL
    updated_at TEXT NOT NULL

channel_models：渠道探测到的模型。

    channel_id TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE
    model TEXT NOT NULL
    source TEXT NOT NULL DEFAULT 'probe'
    enabled INTEGER NOT NULL DEFAULT 1
    first_seen_at TEXT NOT NULL
    last_seen_at TEXT NOT NULL
    PRIMARY KEY (channel_id, model)

同名模型通过 channel_id 加 model 组成唯一键。例如 deepseek-v4-pro 在渠道 A 和渠道 B 是两条独立记录。模型列表未知的渠道不制造假模型记录，只在路由时作为最后兜底候选。

aggregates 与 aggregate_targets：聚合模型和目标优先级。

    aggregates(id INTEGER PRIMARY KEY, name TEXT UNIQUE NOT NULL,
      enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL)

    aggregate_targets(aggregate_id INTEGER NOT NULL REFERENCES aggregates(id)
      ON DELETE CASCADE, position INTEGER NOT NULL, model TEXT NOT NULL,
      channel_id TEXT NOT NULL REFERENCES channels(id),
      PRIMARY KEY (aggregate_id, position))

position 决定优先级；每个目标明确指定模型和渠道。

### 4.2 状态表

channel_states：渠道自动状态。

    channel_id TEXT PRIMARY KEY REFERENCES channels(id) ON DELETE CASCADE
    status TEXT NOT NULL DEFAULT 'available'
    disabled_until TEXT
    fail_count INTEGER NOT NULL DEFAULT 0
    last_error TEXT NOT NULL DEFAULT ''
    last_failure_class TEXT NOT NULL DEFAULT ''
    last_success_at TEXT
    last_checked_at TEXT
    updated_at TEXT NOT NULL

model_states：模型手动开关和自动状态。

    channel_id TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE
    model TEXT NOT NULL
    manual_enabled INTEGER NOT NULL DEFAULT 1
    status TEXT NOT NULL DEFAULT 'available'
    disabled_until TEXT
    fail_count INTEGER NOT NULL DEFAULT 0
    last_error TEXT NOT NULL DEFAULT ''
    last_failure_class TEXT NOT NULL DEFAULT ''
    last_success_at TEXT
    last_checked_at TEXT
    updated_at TEXT NOT NULL
    PRIMARY KEY (channel_id, model)

状态取值为 available、cooling、disabled。查询不到状态记录时默认 available。

手动开关和自动健康状态必须分开。手动关闭不会被成功请求自动打开；成功仅恢复自动状态。

可用性顺序：

    渠道手动关闭 -> 不可用
    渠道自动 disabled 或 cooling -> 不可用
    模型手动关闭 -> 不可用
    模型自动 disabled 或 cooling -> 不可用
    其他 -> 可用

### 4.3 路由日志表

日志以两张扁平表保存，前端接口再按 request_id 聚合成时间线。

route_requests：一次客户端请求一条摘要。

    request_id TEXT PRIMARY KEY
    requested_model TEXT NOT NULL
    virtual_model TEXT
    started_at TEXT NOT NULL
    finished_at TEXT
    result TEXT NOT NULL DEFAULT 'running'
    final_model TEXT
    final_channel_id TEXT
    http_status INTEGER
    duration_ms INTEGER
    error_message TEXT NOT NULL DEFAULT ''

route_attempts：每次模型或渠道尝试一条记录。

    id INTEGER PRIMARY KEY AUTOINCREMENT
    request_id TEXT NOT NULL REFERENCES route_requests(request_id)
      ON DELETE CASCADE
    step_no INTEGER NOT NULL
    action TEXT NOT NULL
    model TEXT NOT NULL
    channel_id TEXT
    started_at TEXT NOT NULL
    finished_at TEXT
    result TEXT NOT NULL
    failure_class TEXT NOT NULL DEFAULT ''
    status_code INTEGER
    error_message TEXT NOT NULL DEFAULT ''
    duration_ms INTEGER
    metadata_json TEXT NOT NULL DEFAULT '{}'
    UNIQUE(request_id, step_no)

动作固定为：attempt、retry、switch_channel、switch_model、skipped、success、failed。

metadata_json 只允许非敏感、小型扩展数据，严禁写入 Authorization、API Key、完整请求和完整响应。

索引：

    route_requests(started_at DESC)
    route_requests(requested_model, started_at DESC)
    route_attempts(request_id, step_no)
    route_attempts(channel_id, started_at DESC)
    route_attempts(model, started_at DESC)
    route_attempts(result, started_at DESC)

前端展示示例：

    auto-demo [成功]
      1. deepseek-v4-pro @ 渠道1      失败：余额不足
      2. deepseek-v4-flash @ deepseek 失败：网络不稳定
      3. deepseek-v4-flash @ deepseek 重试失败：不支持图片
      4. kimi-k3 @ 渠道3              成功

## 5. 路由和失败策略

普通模型请求，例如 deepseek-v4-pro：

1. 按渠道优先级找出明确支持该模型的渠道。
2. 模型未知的渠道排在后面作为兜底。
3. model-health 过滤不可用项。
4. 依次尝试渠道；单个渠道失败后记录日志，再尝试下一个。
5. 某渠道成功即结束；全部失败返回可解释的最终错误。

聚合模型请求，例如 auto-demo：

1. aggregate 按 position 选择首个可用目标。
2. 目标是模型加指定渠道。
3. model-gateway 只访问指定渠道，不跨到同名模型其他渠道。
4. 当前目标失败后，aggregate 选择下一个可用目标。

统一失败分类：

    model_quota：单模型或下游额度不足
    channel_billing：渠道总账户余额不足
    auth：密钥或权限失败
    rate_limit：频率限制
    capability：能力不支持
    network：网络或超时
    temporary：临时服务错误
    unknown：未知错误

分类器依据 HTTP 状态、上游 JSON 的 code/type/message、渠道配置和关键词。

    sync_billing = false
      所有费用错误只影响当前 model@channel

    sync_billing = true
      明确为 channel_billing 才禁用渠道下全部模型
      model_quota 仍只影响当前 model@channel

    未明确识别的 HTTP 402
      默认模型级失败，禁止误禁用整个渠道

## 6. 插件和事件

新增 plugins/model-health。对外接口保持小而稳定：Check、RecordSuccess、RecordFailure、设置渠道或模型手动开关、手动恢复、状态列表查询。

其内部实现失败分类、状态计算、渠道级传播和定时健康检查。默认只检查有历史使用、失败或冷却结束的模型，避免启动后额外消耗费用。

新增 plugins/route-log。它负责创建和结束 route_requests，写入 route_attempts，按 request_id 聚合查询，按条件筛选，按保留期清理，并做敏感字段校验。第一版默认保留 30 天。

重构 plugins/model-gateway：

- 将候选渠道生成与单次上游请求分开。
- 每次渠道尝试前后产生统一事件。
- 普通模型在候选渠道间自动 failover。
- 聚合指定渠道时也必须检查模型状态。
- 流式请求区分连接成功和中途断流。连接成功后不能切换渠道，只记录失败并结束响应。

统一事件：

    route:started
    route:attempt-started
    route:attempt-failed
    route:attempt-skipped
    route:switched
    route:attempt-succeeded
    route:finished

每个事件至少带 request_id、requested_model、model、channel_id、action、step_no、时间戳；失败事件额外带 failure_class 和脱敏后的 error_message。

model-health 监听尝试成功/失败；route-log 监听所有事件；aggregate 仅处理失败结果后选择下一个目标。

## 7. 管理 API 和 UI

新增接口：

    GET    /api/model-status
    PATCH  /api/model-status/models/{channel_id}/{model}
    PATCH  /api/model-status/channels/{channel_id}
    POST   /api/model-status/models/{channel_id}/{model}/recover
    POST   /api/model-status/channels/{channel_id}/recover
    POST   /api/model-status/check
    GET    /api/route-logs
    GET    /api/route-logs/{request_id}
    DELETE /api/route-logs

模型状态 Tab：新增 web/src/ModelStatusManager.vue。

- 渠道折叠展示。
- 渠道行显示名称、地址、费用同步、渠道手动开关、自动状态和原因。
- 展开后显示所有已发现模型。
- 每个模型显示独立开关、自动状态、最后错误、失败次数、最近成功、冷却结束时间。
- 支持恢复模型、恢复渠道和手动健康检查。
- 渠道级异常在相关模型行显示继承原因。

转发日志 Tab：新增 web/src/RouteLogManager.vue。

- 列表显示时间、request_id、请求模型、最终模型/渠道、结果和耗时。
- 展开按 step_no 显示完整时间线。
- 支持按模型、渠道、结果、时间范围筛选。
- 支持手动刷新与清理过期日志。实时推送留到后续版本。

App.vue 的渠道表单增加费用同步开关，对应 sync_billing，默认关闭。渠道手动开关保持独立，不使用自动健康状态替代它。

## 8. JSON 迁移

迁移原则：

1. 导入前备份 JSON 到 ~/.loadout/backups/{timestamp}/。
2. 每种 JSON 只导入一次，记录到 schema_migrations。
3. 每个文件独立事务，失败回滚该文件。
4. API Key 密文原样迁移。
5. 未识别字段写迁移报告，不静默丢失。

导入顺序：

    channels -> channel_models -> aggregates -> aggregate_targets
    -> capability_routes -> MCP 配置 -> skills/presets/settings
    -> users/api_keys/mcp_keys -> model_health.json 到 model_states

兼容窗口：首次启动从 JSON 导入 SQLite；之后 UI 和业务只读写 SQLite；JSON 保留为备份和回滚依据。SQLite 稳定两个版本后删除 JSON 业务读路径，禁止长期双写。

## 9. 实施阶段

### 阶段 0：基线冻结

确认当前 dirty worktree 的修改归属，记录 go test、go vet、前端构建基线，补充当前普通和聚合路由测试。

完成标准：当前可用行为有测试保护，现有未提交改动不被误删。

### 阶段 1：SQLite 与迁移框架

实现数据库连接、迁移、事务、JSON 备份/导入与迁移报告，建立所有表和索引。

完成标准：空目录建库、旧 JSON 导入、重复启动幂等、导入失败回滚、Windows/Linux 构建通过。

### 阶段 2：配置仓储切换

依次将 channels、aggregates、能力路由、keys、MCP、skills 从 JSON 切换至 SQLite。

完成标准：后台所有增删改查使用 SQLite；重启不丢数据；旧功能回归通过。

### 阶段 3：模型状态插件

创建 model-health，迁移旧健康记录，实现手动开关、成功恢复、失败分类、冷却、渠道传播和定时检查。

完成标准：渠道级与模型级状态隔离；sync_billing 全面测试；成功不覆盖手动关闭。

### 阶段 4：路由执行重构

重构 model-gateway 的候选生成、单次尝试、普通模型 failover、聚合目标切换和流式错误处理。

完成标准：同名多渠道切换正确；聚合严格按目标切换；不可用项不发上游请求；事件顺序稳定。

### 阶段 5：路由日志插件

实现请求和步骤日志、详情聚合、筛选和保留清理。

完成标准：单个 request_id 可还原完整链路；重试、切换、跳过、成功、失败均可区分；写日志异常不阻塞转发。

### 阶段 6：API 和 UI

实现模型状态和转发日志接口、两个 Vue 页面、渠道费用同步配置。

完成标准：开关即时生效，前端状态和日志与数据库一致，桌面和窄屏不重叠。

### 阶段 7：清理和发布

删除 aggregate 的旧健康实现、JSON 业务写路径和临时兼容层，更新文档、CI 和打包。

完成标准：检索不到旧健康写入路径；全量测试、迁移演练、回滚演练通过。

## 10. 测试和发布门槛

TDD 顺序固定为：先写失败测试，确认失败，再写最小实现，最后重构。

重点测试：

- 数据库迁移顺序、幂等、事务回滚、外键与加密密文不变。
- 默认可用、模型级冷却、渠道级禁用、手动关闭、成功恢复、费用同步。
- 同名多渠道切换、聚合目标切换、指定渠道不跨渠道、状态跳过、流式中断。
- request_id 贯穿、step_no 递增、日志聚合顺序、敏感信息不落库。
- Tab 加载、渠道折叠、开关、状态继承、日志筛选和异常状态。

发布前必须通过：

    go test ./...
    go vet ./...
    go build ./...
    web: npm run build
    空目录建库和旧 JSON 全量迁移演练
    迁移后重启演练
    普通模型多渠道切换演练
    聚合模型目标切换演练
    模型级和渠道级费用错误演练
    日志查询、清理与回滚演练

## 11. 风险和回滚

| 风险 | 处理 |
|---|---|
| SQLite 驱动在 Windows 构建失败 | 阶段 1 先验证双平台 CI，优先选择纯 Go 驱动 |
| JSON 字段无法完整映射 | 迁移前备份、迁移报告、文件级事务回滚 |
| 误禁用整个渠道 | 未明确的 402 一律按模型级处理，传播规则有测试 |
| 日志写入拖慢转发 | 使用短事务；必要时有界异步队列；写日志失败不影响请求 |
| 流式响应开始后无法切换 | 仅连接建立前切换，中途断流只记录并结束 |
| 新旧双写不一致 | 仅做一次导入，切换后只写 SQLite |

回滚流程：停止服务，备份当前 loadout.db，恢复迁移前 JSON 备份，使用上一版本二进制启动。重构期间新增配置可从 SQLite 导出后人工补回。

## 12. 已确认决策

1. 数据库路径为 ~/.loadout/loadout.db。
2. 日志为 route_requests 和 route_attempts 两张扁平表，按 request_id 聚合展示。
3. sync_billing 是布尔值，默认 false。
4. 手动开关与自动健康状态独立保存、独立展示。
5. 未记录的模型状态默认可用。
6. 未明确识别的 HTTP 402 只影响当前模型。
7. 定时检查默认只检查历史使用过或失败过的模型。
8. 第一版日志默认保留 30 天，不保存请求或响应正文。
9. SQLite 稳定前保留 JSON 备份，但不长期双写。

## 13. 评审补充决策（实施前必须落实）

本节优先级高于前文中存在歧义的描述。

### 13.1 首期边界

首期只迁移路由直接依赖的数据：channels、channel_models、aggregates、aggregate_targets、channel_states、model_states、route_requests 和 route_attempts。

capability_routes、MCP 配置、skills、presets、settings、users 和各类 keys 另立数据模型与迁移计划，不与核心路由重构捆绑发布。这样可以将故障范围限制在渠道路由、模型状态和日志，而不会同时改变无关配置。

### 13.2 状态和开关的唯一来源

channel_models 是渠道发现到的模型目录，不保存模型开关。模型的手动开关唯一写入 model_states.manual_enabled；渠道手动开关唯一写入 channels.manual_enabled。

状态接口必须显式返回，前端不得根据单个 status 推断可用性：

    manual_enabled
    health_status
    effective_available
    reason

渠道自动健康状态来自 channel_states，模型自动健康状态来自 model_states。手动关闭和系统冷却必须分别展示。

aggregate_targets 引用了渠道时，渠道删除必须被拒绝，直到相关聚合目标被删除或替换，禁止生成悬空目标。

### 13.3 数据库迁移和首次导入

schema_migrations 只记录数据库结构变更，字段固定为：

    version INTEGER PRIMARY KEY
    name TEXT NOT NULL UNIQUE
    checksum TEXT NOT NULL
    applied_at TEXT NOT NULL

每条结构迁移必须在事务中执行，并检查版本连续和 checksum 一致。数据库版本高于当前程序、或同版本 checksum 不一致时，服务必须拒绝启动。

data_imports 单独记录旧 JSON 的一次性导入：

    source_name TEXT PRIMARY KEY
    source_checksum TEXT NOT NULL
    imported_at TEXT NOT NULL
    report_path TEXT NOT NULL

channels、channel_models、aggregates、aggregate_targets、旧 model_health.json 属于同一套路由数据，首次导入必须使用一个总事务；任一部分失败，全部回滚。导入结束前不能加载依赖这些数据的业务插件。迁移报告必须列出已映射、跳过和失败的数据。

阶段 1 的第一个交付物不是仓储层，而是 SQLite 最小验证：确认选定驱动兼容当前 Go 版本、Windows/Linux 构建、WAL、外键、忙等待和事务行为。验证通过后才开始业务迁移。

### 13.4 路由事件与控制流程

Pipeline 必须新增 RequestID。requestIDMiddleware 生成的值同时写入 HTTP 响应头和 Pipeline，事件不得只靠回读请求头取得 request_id。

事件分为两类：

1. 观察事件：route-log 与 model-health 订阅。它们只能记录日志或更新状态，失败只能写内部错误，绝不能中断用户请求或改写路由。

2. 控制流程：model-gateway 在本次尝试失败后直接向 route planner 请求下一候选；aggregate 只提供“下一聚合目标”，不得通过监听观察事件来隐式改写路由。

失败后的固定顺序是：结束当前 attempt，更新模型状态，记录失败日志，route planner 选择下一项，创建下一 attempt。每一步的错误处理都不得阻塞转发。

### 13.5 路由日志的严格语义

route_attempts 的一行对应一次真实上游请求，或一次明确的“候选被跳过”。不能将切换、重试、成功和失败混在一个 action 中。

    action：initial、retry、switch_channel、switch_model、health_check
    result：running、success、failed、skipped、stream_interrupted、partial_success

新增 previous_attempt_id，指向导致本次重试或切换的上一步。step_no 在同一 request_id 内严格递增；不可用候选被跳过时也占用一个步骤，但不发起上游请求。

第一版日志使用同步短事务，只写必要的脱敏字段。日志写入失败绝不能影响转发。仅当性能基线不足时，才引入有界异步队列；届时必须规定队列满、服务关闭 flush 和允许丢失哪些日志。

### 13.6 流式请求

上游返回可用的 2xx 响应头时，attempt 只进入“已连接”状态；只有流正常结束才记录 result=success。

一旦向客户端写出任意内容，不能再切换渠道。此后中途断流记录为 stream_interrupted 或 partial_success，更新健康状态、写入日志后结束响应。未向客户端写出内容且连接失败时，才允许继续切换候选。

### 13.7 费用错误和健康检查

错误分类通过可扩展的 provider_type 或 billing_policy 完成，不能将 NewAPI 等渠道的关键词规则分散在 model-gateway 或 aggregate。

sync_billing 只决定已明确识别为 channel_billing 的错误是否传播到整个渠道。对于 NewAPI，必须区分“NewAPI 账户余额不足”和“下游平台或模型余额不足”；无法明确识别时，一律只影响当前 model@channel。

定时探测必须标记 source=health_check，默认不进入普通转发统计。实施时需定义最小非流式请求、模型和渠道并发上限、超时、重试次数及不同能力模型的探测方法。手动关闭的渠道或模型默认不检查；手动检查是否绕过开关必须由接口参数明确说明。

### 13.8 回滚

发布前必须提供 SQLite -> JSON 的配置导出工具。回滚顺序为：停止服务，导出当前配置，备份 loadout.db 及其 WAL/SHM 文件，恢复迁移前备份或导出的 JSON，再使用上一版本二进制启动。

日志和自动健康状态可以不回迁；渠道、模型目录、聚合目标和手动开关必须可以导出为旧版本可读取的格式。
