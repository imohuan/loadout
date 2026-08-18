# 测试 AI KEy 所在 世视觉模型OpenAi 配置
D:\Code\Git\loadout\keys 这个文件夹下

# 测试的时候注意备份

# Loadout 设计文档

> 一句话定位：一个把所有 MCP 工具聚合后只暴露 3 个入口、按需加载工具定义的轻量网关——用 MCP 的壳装 skills 的灵魂。
> 附赠：给任何模型附加任何能力（视觉/TTS/图像/视频）的模型网关，以及 skills 预设管理器。

- 名称：**Loadout**（给模型"配装"能力）
- 语言：Go（后端）、Vue 3（前端）
- 平台：Linux（服务器部署）、Windows（Wails 桌面壳）
- 许可证：MIT
- 版本：v1 范围 = 视觉能力附加 + MCP 聚合网关 + skills 预设管理 + 管理后台

---

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
│  │ core：插件框架 / 日志 / JSON存储 / 认证      │  │
│  └────────────────────────────────────────────┘  │
│  ┌────────────────────────────────────────────┐  │
│  │ 管理后台（Vue 3 + shadcn-vue-cdn，需登录）   │  │
│  └────────────────────────────────────────────┘  │
└──────────┬───────────────────────┬───────────────┘
           ▼                       ▼
┌────────────────────┐   ┌─────────────────────────┐
│ NewAPI（本地部署）   │   │ 上游 MCP servers         │
│ 渠道/配额/计费/日志  │   │ (stdio / HTTP)           │
└─────────┬──────────┘   └─────────────────────────┘
          ▼
   上游模型服务商
```

- 对外只暴露一个端口 `:3000`：`/v1/*` = 模型 API（sk- key）、`/mcp/*` = MCP 端点（header key，路由方式：单 MCP / 分组 / $smart）、其余路径 = 管理后台（session）；认证中间件按路径前缀挂载（见 3.3 `RegisterRoute`）。
- Loadout 与 NewAPI 的关系：Loadout 做**能力附加与请求改写**，NewAPI 做**渠道管理与计费**。能力调用（视觉模型等）也走 NewAPI 渠道，账目统一。
- 数据目录：`~/.loadout/`（JSON 文件），技能目标目录：`~/.agents/skills/`。

---

## 2. 目录结构（monorepo）

```
loadout/
├── DESIGN.md                 # 本文档
├── LICENSE                   # MIT
├── README.md                 # 中文
├── go.mod
├── core/                     # 核心框架（零业务逻辑，全部可测）
│   ├── config/config.go      # ★ 所有程序级硬编码配置（中文注释）
│   ├── plugin/               # 插件框架（Cordis 思想）
│   ├── logger/               # slog + lumberjack
│   ├── store/                # JSON 存储（原子写入、文件锁、AES 加密字段）
│   ├── auth/                 # 登录/JWT、sk- key、MCP header key
│   ├── linkfs/               # 跨平台链接（symlink / junction / 降级复制）
│   └── mcpkit/               # MCP 客户端/服务端封装（官方 go-sdk）
├── plugins/                  # 业务插件（一个插件一个目录）
│   ├── admin-auth/           # 管理员登录、会话
│   ├── admin-api/            # 管理 API + 内置 Web UI
│   ├── model-gateway/        # 模型转发核心（/v1 请求管线）
│   ├── vision/               # 视觉能力适配器
│   ├── mcp-hub/              # MCP 聚合网关（status/get/invoke；路由方式：单 MCP/分组/$smart）
│   ├── skills/               # skills 仓库 + 预设切换
│   └── gateway-keys/         # sk- key / MCP key 签发与校验
├── apps/
│   ├── server/               # Linux 入口（单二进制 + systemd 单元）
│   └── desktop/              # Windows 入口（Wails v3 壳，参考 go-tools-app）
├── web/                      # Vue 3 + Vite + shadcn-vue-cdn（构建时本地打包）
├── testkit/                  # 测试基建：fake-llm、fake-mcp
├── scripts/                  # 构建/打包脚本（Linux、Windows）
└── .github/workflows/        # CI：Linux + Windows 测试矩阵
```

---

## 3. 插件框架（Cordis 思想的 Go 实现）

参考 DeepSeek Harness / Cordis 的五个核心概念，移植到 Go（编译期装配，方案 A）：

| Cordis 概念 | Go 侧实现 |
|---|---|
| 插件声明依赖 `inject` | `plugin.yaml` 里 `inject: [服务名...]`，依赖就绪才启动 |
| 服务容器 `ctx.xxx` | `Context.Get(name)` / `Context.Set(name, svc)`，按接口取用，不 import 实现 |
| 可逆副作用 `ctx.effect()` | `Context.Effect(fn) Disposer`，卸载时逆序执行 |
| 事件总线（emit/waterfall/serial） | `Context.On/Emit/Waterfall`，请求改写链用 waterfall |
| 一切皆插件 | 业务全部以插件形式挂在 core 上，core 不 import 任何业务 |

### 3.1 插件目录规范（一个插件一个目录）

```
plugins/vision/
├── plugin.yaml          # 清单：name/version/inject/provide
├── plugin.go            # Apply(ctx) 入口
├── plugin_test.go       # TDD 单元测试
├── integration_test.go  # httptest 全链路集成测试
├── fixtures/            # 测试素材（样例消息、SSE 片段）
└── README.md            # 插件说明（中文）
```

### 3.2 plugin.yaml 示例

```yaml
name: vision
version: 0.1.0
inject:                    # 依赖的服务（就绪后才启动）
  - logger
  - store
  - http-client
provide:                   # 提供的服务（供其他插件 inject）
  - vision-service
```

### 3.3 核心接口

```go
// core/plugin/plugin.go
type Manifest struct {
    Name    string   `yaml:"name"`
    Version string   `yaml:"version"`
    Inject  []string `yaml:"inject"`
    Provide []string `yaml:"provide"`
}

type Plugin interface {
    Manifest() Manifest
    Apply(ctx *Context) error // 启动入口
}

type Issue struct {           // 插件自检结果
    Level   string // info / warn / error
    Message string
}

type AuthKind string

const (
    AuthSession   AuthKind = "session" // 管理后台登录（session/JWT）
    AuthSkKey     AuthKind = "sk-key"  // 模型 API
    AuthMCPHeader AuthKind = "mcp-key" // MCP 端点
    AuthPublic    AuthKind = "public"  // 无需认证
)

type RouteSpec struct {
    Method  string      // GET / POST / ...
    Pattern string      // 路由路径：/v1/chat/completions、/mcp/github、/api/...
    Auth    AuthKind    // 认证类别，框架据此挂认证中间件并分发
    Handler http.Handler
}

type Context struct {
    // 服务注册与获取
    Get(name string) any
    Set(name string, svc any) Disposer
    // 事件
    On(event string, h Handler) Disposer
    Emit(event string, payload any)
    Waterfall(event string, payload any) (any, error) // 中间件链
    // 可逆副作用：卸载时逆序执行返回的清理函数
    Effect(fn func()) Disposer
    // 带插件名的日志（自动显示 plugins/vision/plugin.go:42）
    Logger() *slog.Logger
    // 插件自检：实现"自己检查问题所在"
    RegisterCheck(name string, fn func() []Issue)
    // 插件注册 HTTP 路由：框架按 AuthKind 挂认证中间件，单端口按路径分发入口
    RegisterRoute(spec RouteSpec) Disposer
}
```

### 3.4 插件自检（用户要求：插件能自己检查问题）

- 每个插件在 `Apply` 中调用 `ctx.RegisterCheck("检查项名", 自检函数)`。
- 自检内容示例（vision 插件）：
  - 视觉渠道是否已配置、key 是否存在；
  - 能力路由表里是否有引用了不存在的模型；
  - 视觉模型连通性（可选的主动探测，带超时）。
- 结果展示：① 启动时打进日志；② 管理后台"插件"页展示每个插件的自检结果，支持手动触发重新检查。

### 3.5 插件装配方式（方案 A：编译期装配）

- 所有插件在 `plugins/` 下以 Go 包形式存在，由生成器扫描 `plugin.yaml` 生成 `plugins/registry.go`（自动注册代码）。
- 加插件 = 建目录写代码 → 重新编译。以后若要支持外部热加载（WASM），接口已留 seam。

---

## 4. 配置体系

### 4.1 分工原则（已确认）

| 位置 | 内容 | 生效方式 |
|---|---|---|
| `core/config/config.go` | 程序级配置：端口、超时、路径、日志、默认视觉模型等 | 改完重新编译 |
| `~/.loadout/data/*.json` | 运行时可改数据：渠道、能力路由、MCP 列表、分组、预设、key 等 | 后台改完即时生效 |

### 4.2 config.go 结构（所有项必须带中文注释）

```go
// core/config/config.go
package config

// ============ 应用信息 ============
const AppName = "Loadout"          // 应用名，用于日志前缀、窗口标题、目录名
const Version = "0.1.0"            // 版本号，随每次发布更新

// ============ 运行模式 ============
// RunMode: "server"（Linux 服务器，监听全网卡）或 "desktop"（Windows 桌面，仅监听 127.0.0.1）
const RunMode = "server"

// ============ 目录路径 ============
const HomeDir       = "~/.loadout"        // 数据根目录（日志、数据、技能仓库、备份都在这里）
const DataDir       = HomeDir + "/data"   // JSON 数据目录
const SkillsDir     = HomeDir + "/skills" // 技能完整仓库（所有安装过的技能，永不删除）
const LogsDir       = HomeDir + "/logs"   // 日志文件目录
const BackupsDir    = HomeDir + "/backups"// 备份目录
const AgentSkillsDir = "~/.agents/skills" // 技能预设的目标目录（切换预设时在此重建链接）

// ============ 端口 ============
// 单端口服务，三类入口按路径分发（框架托管，认证中间件按前缀挂载，见 3.3 RegisterRoute）：
//   /v1/* → 模型 API（sk- key）；/mcp/* → MCP 端点（header key，单 MCP/分组/$smart 三种路由方式）；其余 → 管理后台（session）
const ServerAddr = ":3000" // 统一监听端口（RunMode 决定监听 127.0.0.1 还是全网卡）

// ============ 超时 ============
const UpstreamTimeout  = 300 * time.Second // 转发上游的最大时长（含流式生成全程）
const VisionTimeout    = 60 * time.Second  // 视觉模型调用超时
const MCPInvokeTimeout = 120 * time.Second // MCP 工具调用超时
const HTTPReadTimeout  = 10 * time.Second  // 普通 HTTP 请求读超时

// ============ 日志 ============
const LogLevel     = "info"   // 日志级别：debug/info/warn/error
const LogMaxSizeMB = 50       // 单个日志文件多大后轮转
const LogMaxBackups = 7       // 保留多少个历史日志文件
const LogMaxAgeDays = 30      // 历史日志最多保留多少天
// 日志格式固定为：时间 [等级] [源码相对路径:行号] 消息（见第 9 节）

// ============ 认证 ============
const SessionTTLHours = 24 * 7  // 管理后台登录有效期（小时）
const AdminPasswordFile = HomeDir + "/admin-password" // 首启随机密码存放文件（无后缀，0600 权限）

// ============ 模型网关 ============
const UpstreamBaseURL = "http://127.0.0.1:3001/v1" // NewAPI 地址（本地部署）
const DefaultVisionModel = "qwen-vl-max"           // 默认视觉模型（能力路由表未配置时兜底）
const VisionDescriptionPrompt = "请仔细观察以下图片，按顺序用中文详细描述每张图片的内容…" // 视觉描述提示词
const ReasoningInjectionStyle = "reasoning_content" // 流式注入风格：reasoning_content（DeepSeek 系）

// ============ MCP 网关 ============
const MaxToolResultChars = 8000   // invoke 结果截断上限（字符）
const StatusFlatThreshold = 10    // status 无参数时，工具总数 ≤ 该值直接返回完整列表（省一轮往返）
const ToolConflictPrefix = true   // 同名工具冲突时自动加"来源_"前缀

// ============ skills ============
const SkillInstallMode = "npx"    // 技能安装方式："npx"（npx skills CLI）或 "git"（git clone）
const SkillBodyMaxChars = 20000   // SKILL.md 通过 get 返回时的截断上限（字符）

// ============ 视觉描述缓存 ============
const VisionCacheEnabled = true    // 同图复用描述（md5 缓存）
const VisionCacheTTLHours = 24 * 7 // 缓存有效期
```

### 4.3 ~/.loadout 目录布局（已确认）

```
~/.loadout/
├── data/                  # JSON 数据（见第 5 节），原子写入
│   ├── .secret            # 本地密钥文件（0600 权限，用于 AES 加密与 JWT 签名）
│   └── *.json
├── skills/                # 技能完整仓库（所有技能真实文件所在，永不删除）
├── logs/                  # 日志（loadout.log，轮转）
├── backups/               # 备份（一键备份命令的输出）
└── admin-password         # 首启随机密码（无后缀；改密成功后自动删除）
```

---

## 5. 数据模型（~/.loadout/data/ 下的 JSON 文件）

所有文件使用**原子写入**（先写 `.tmp` 再 rename），读写加进程内锁。渠道 key、各类 key 的明文**不落盘**，只存哈希或 AES 密文。

### 5.1 users.json —— 管理员账号（单账号）

```json
[{ "username": "admin", "password_hash": "bcrypt...", "password_changed": false }]
```

### 5.2 api_keys.json —— 模型 API key（sk-）

```json
[{ "id": "k1", "name": "本机调用", "prefix": "sk-abc", "hash": "sha256...",
   "models": ["*"], "enabled": true, "created_at": "2026-08-15T15:00:00Z" }]
```
- 完整 key 只在创建时展示一次；`models` 为空数组或 `["*"]` 表示不限制。
- `/v1/models` 与 `/v1/chat/completions` 按 key 的 `models` 白名单过滤（真实模型与虚拟/聚合模型同等受约束，要用虚拟模型需将其名列入白名单）；`models` 为空或含 `*` 时不限制。

### 5.3 mcp_keys.json —— MCP endpoint key（一把 key 绑一个 endpoint，已确认）

```json
[{ "endpoint": "/mcp/group1", "header_name": "X-Loadout-Key", "hash": "sha256..." }]
```
- `endpoint` 即 `/mcp/*` 下的端点路径，对应三种路由方式之一：`/mcp/{mcp名}`（单 MCP）、`/mcp/{分组名}`（分组）、`/mcp/$smart`（仿技能）。

### 5.4 channels.json —— 上游渠道（NewAPI 及任何 OpenAI 兼容地址）

```json
[{ "id": "newapi", "name": "本地 NewAPI", "base_url": "http://127.0.0.1:3001/v1",
   "api_key_cipher": "AES...", "enabled": true }]
```
- `api_key` 用 AES-GCM 加密存储（密钥在 `.secret`）。

### 5.5 capability_routes.json —— 能力路由表（核心）

```json
[
  { "model": "deepseek-chat", "capability": "vision", "route": "proxy",  "via_model": "qwen-vl-max", "channel_id": "newapi" },
  { "model": "gpt-4o",         "capability": "vision", "route": "native" }
]
```
- `route` 三种取值：
  - `native`：模型原生支持，直接透传；
  - `proxy`：转发给 `via_model`（视觉等能力）；
  - `error`：明确不支持且不附加，直接报错。

### 5.6 mcp_servers.json —— 上游 MCP 服务器（支持多个，已确认）

```json
[
  { "id": "srv-github", "name": "github", "transport": "stdio",
    "command": "npx", "args": ["-y", "@modelcontextprotocol/server-github"], "enabled": true },
  { "id": "srv-weather", "name": "weather", "transport": "http",
    "url": "http://127.0.0.1:8000/mcp", "enabled": true }
]
```
- v1 出站传输：stdio + streamable HTTP；每个上游 MCP 自动对应一个 `/mcp/{name}` 单连接端点。

### 5.7 tools_state.json —— 单工具开关与分类（已确认）

```json
[{ "server_id": "srv-github", "tool_name": "search_code",
   "enabled": false, "category": "github", "tags": [] }]
```
- 未记录的默认启用；`category` 为 status 索引使用的分类（默认 = 来源 MCP 名），`tags` 为可选附加标签。

### 5.8 groups.json —— 分组（手动勾选，无自动规则，已确认）

```json
[{ "name": "group1", "tools": [{"server_id": "srv-github", "tool_name": "search_code"}] }]
```
- `$smart` 为**内置组**：内容 = 已安装的技能（见 8.7），不进 groups.json；端点由三种路由方式自动生成——每个上游 MCP → `/mcp/{mcp名}`、每个分组 → `/mcp/{分组名}`、技能 → `/mcp/$smart`。

### 5.9 skills.json —— 技能仓库清单

```json
[{ "name": "web-design", "source": "vercel-labs/agent-skills", "installed_at": "...", "version": "main" }]
```

### 5.10 presets.json —— 技能预设（已确认：只存名称 + 技能清单）

```json
[{ "name": "编程向", "skills": ["git-tools", "web-design"] }]
```

### 5.11 settings.json —— 运行时设置

```json
{ "active_preset": "编程向", "default_model": "deepseek-chat" }
```

---

## 6. 认证体系（三层，已确认）

| 层 | 对象 | 机制 |
|---|---|---|
| ① 管理后台 | 人（仅 admin 单账号） | 用户名密码登录 → JWT（HttpOnly Cookie）；登录只管管理页面 |
| ② 模型 API（/v1） | 程序 | `Authorization: Bearer sk-xxx`，只认 key |
| ③ MCP 端点（/mcp*） | agent 客户端 | 自定义 header（默认 `X-Loadout-Key`），一把 key 绑一个 endpoint |

### 6.1 首启流程

1. 检测 `users.json` 不存在 → 生成随机密码（crypto/rand，16 字符混合）；
2. 密码哈希写入 users.json，明文写入 `~/.loadout/admin-password`（0600）；
3. 日志打印："首次启动，管理员账号 admin，初始密码见 ~/.loadout/admin-password"；
4. 用户在 UI 修改密码成功后，删除 `admin-password` 文件。

---

## 7. 模块一：模型网关 + 视觉能力附加

### 7.1 请求管线（waterfall 中间件链，每个中间件是一个可替换实现）

```
请求 /v1/chat/completions
  → 鉴权（sk- key）
  → 请求归一化（把各家格式统一成内部结构）
  → 能力路由（查 capability_routes.json）
  → 视觉适配（见 7.2，无图或原生支持时跳过）
  → 字段清洗（移除目标模型不识别的字段）
  → 转发渠道（默认 NewAPI）
  → 流式响应注入（reasoning 块）
  → 日志与计量
```

### 7.2 视觉注入时序（核心，串行不可回避）

```
1. 检出 messages 里的图片（image_url：URL 或 base64）
2. 查路由表：目标模型 vision=proxy → 继续；native → 透传；error → 报错
3. 批量抽取图片 → 一次调用视觉模型（多图合并描述，不逐图调用）
4. 拿到描述文本（此步必须完成才能进行第 5 步）
5. 重写 messages：图片 content 替换为文字描述
6. 调用主模型（流式）
7. SSE 流开头注入 reasoning_content 块（重放第 4 步的描述）→ 转发
```

- 总延迟 = 视觉调用 + 主模型调用（串行相加，属设计取舍）。
- 描述缓存：图片内容 md5 → 描述文本，命中直接复用（`~/.loadout/data/vision-cache/`）。

### 7.3 错误策略（已确认：直接报错）

| 失败点 | 行为 |
|---|---|
| 视觉模型调用失败 | 立即返回标准 OpenAI 错误：`error.type = "vision_capability_error"` |
| 图片无法解析/下载 | `error.type = "image_parse_error"` |
| 主模型流式中断 | SSE 流中补发 `error` 事件后结束 |

### 7.4 流式注入格式

- 默认 `reasoning_content`（DeepSeek 系约定，主流客户端可渲染为思考折叠区）。
- 渠道级可覆盖（`injection_style` 字段：`reasoning_content` / `openai_reasoning`），v1 只实现前者。

### 7.5 v1 之后的能力插件（均为独立插件，接口已预留）

- tts 插件：拦截 `/v1/audio/speech` → 路由到 TTS 引擎；
- image 插件：拦截 `/v1/images/generations` → 路由到图像模型；
- video 插件：拦截 `/v1/videos/generations` → 路由 + 帧采样视觉描述。

---

## 8. 模块二：MCP 聚合网关（3 工具设计）

### 8.1 核心原则：每个端点永远只暴露 3 个工具

- 聚合进来的**所有工具**（上游 MCP 的工具、已安装的技能）**只存在于 status 返回的索引里，永远不会被注册为 MCP 工具**；
- 客户端连接任意端点后，看到的工具列表**永远只有**：`status`、`get`、`invoke`；
- 所有聚合工具都通过 `invoke` 调用、通过 `get` 按需加载定义——这就是"只暴露 3 个入口"的含义；
- 效果：无论背后聚合了 10 个还是 1000 个工具，注入模型上下文的工具定义永远只有 3 个（几百 tokens 量级），工具越多不会越贵越慢。
- 消歧：「3 个入口 / 3 个工具」指**每个端点内**永远只有 status/get/invoke；8.2 的「3 种路由方式」指**端点类型**，是两个维度。

### 8.2 端点与"路由方式"（已确认：一把 key 绑一个 endpoint）

| 路由方式 | 端点 | 工具视图 |
|---|---|---|
| 单 MCP 连接 | `/mcp/{mcp名称}` | 仅该上游 MCP 服务器的工具（`mcp_servers.json` 的 `name`） |
| 分组 | `/mcp/{分组名称}` | 仅该分组勾选的工具（`groups.json`） |
| 仿技能 | `/mcp/$smart` | 仅已安装的技能（内置组，见 8.7） |

- **路由方式可扩展**：`/mcp/*` 下每个端点由"路由方式（route kind）"决定其工具视图来源，v1 内置上表 3 种，日后新增路由方式 = 在 mcp-hub 里加一个 kind 实现，核心不动；
- **端点 = 一套连接**：每个端点是进程内独立运行的 MCP server 实例，各管各的工具视图，互不影响；
- **每套连接独立认证**：每个端点可设置/关闭独立的 header auth（header 名可配，默认 `X-Loadout-Key`；key 由后台生成或自定义，只存哈希）；一把 key 只对绑定它的那一个端点生效（v1 每端点最多一把 key，可随时重置或关闭）；
- **端点随配置自动生成**：新增上游 MCP `github` → 自动出现 `/mcp/github`；新建分组 `group1` → 自动出现 `/mcp/group1`；删除 → 端点消失。`/mcp/$smart` 为固定端点；
- **端点可单独启用/禁用**（禁用 = 拒绝连接）；
- 传输方式：streamable HTTP（兼容 SSE 客户端）。

### 8.3 三个工具的完整定义

> 提示词锁死，禁止跳步：先 status 看有哪些工具 → 模型分析任务需要哪些 → get 批量加载 schema → invoke 执行。

**status —— 返回所有工具列表和描述（支持按分类查询，避免一次吐全）**

- 参数：`{ "category": "浏览器" }`（可选）
- 无参数调用 → 返回**分类总览**（两级导航，避免一次吐全）：
  - 全部分类：每个分类的名称、一句话描述、包含的工具数量；
  - 附 `index_version`（索引版本号）；
  - 特例：工具总数 ≤ `StatusFlatThreshold`（config.go，默认 10）时，直接返回完整工具列表，省一轮往返；
- 带 `category` 调用 → 返回该分类下所有工具条目，每条：`{ "name", "description", "category", "source" }`（只含描述不含 schema，单条极小）；
- 工具描述写死：
  > 查看当前可用的工具。第一步必须调用本工具了解有哪些工具；如果工具很多，先无参数调用看分类总览，再按分类查询具体工具列表。之后必须用 get 加载你需要的工具定义，才能用 invoke 调用。

**get —— 批量获取多个工具的完整 schema（一次性加载本次任务要用的）**

- 参数（两种方式，可同时传）：
  - `{ "tools": ["github_search_code", "weather_query"] }`：按工具名批量取；
  - `{ "category": "浏览器" }`：按分类一次性取该分类**全部工具**的完整定义（对应"只加载本次任务用到的分类"）；
- 返回：每个工具的**完整 JSON Schema**（inputSchema）+ description + 来源 + 调用名（invoke 时用的名字）；
- 技能工具特殊：返回该技能 `SKILL.md` 的**全文**（截断上限 `SkillBodyMaxChars`）；
- 已加载的定义保留在对话历史中，本次任务内无需重复 get；
- 工具描述写死：
  > 批量加载工具的完整定义。必须先用 status 确认工具存在，再调用本工具一次性加载本次任务需要的所有工具定义；未加载定义的工具 invoke 时无法正确传参。禁止跳过本步骤直接 invoke。

**invoke —— 调用具体工具**

- 参数：`{ "tool": "github_search_code", "arguments": { ... } }`（arguments 结构 = 该工具 inputSchema）
- 行为：校验工具在**当前端点视图**内可见且已启用 → 转发给所属上游 MCP → 返回 MCP 标准结果（content + isError）；
- 结果超长按 `MaxToolResultChars` 截断并标注"结果已截断"；
- 工具描述写死：
  > 调用一个具体工具。必须先 status 后 get，确认工具存在并已加载其完整定义，再严格按定义里的参数格式调用。禁止在未 get 的情况下猜测参数直接调用。

### 8.4 省 token 的三个手段（落地）

1. **索引（status 结果）是工具返回值，用到才进上下文，不是常驻提示词**：聚合工具的 schema 从不注册为 MCP 工具，客户端上下文里的工具定义永远只有 3 个；
2. **get 批量加载，只加载本次任务用到的（工具或分类），加载过的才留在历史里**：其余工具的 schema 不进上下文；
3. **索引放在对话最前面、内容保持稳定，命中 prompt cache**：
   - "最前面"由使用方提示词保证（见 8.8 模板），服务端负责"内容稳定"：
   - 确定性序列化：工具按 name 排序、JSON 字段顺序固定，两次 status 字节级一致；
   - **append-only 纪律**：新增工具排在组内末尾，不重排、不插队（借鉴 DeepSeek Harness 的 KV Cache 纪律）；
   - 任何变更（增删工具/开关/改分组）使 `index_version` +1，客户端可感知、旧缓存自然失效；
   - 已发布的工具描述**不修改**（改描述 = 缓存作废），新增说明用新字段。

### 8.5 分类体系（status 按分类查询的数据来源）

- 每个工具一个**主分类**（必填）+ 可选多个标签；
- 默认分类 = 来源 MCP 名称（如 `github`、`weather`），装好即可用；
- 后台可改分类/加标签（存 tools_state.json，见 5.7）；
- 技能工具的分类固定为 `技能`。

### 8.6 上游管理与开关（支持多个 MCP 连接）

- 支持多个上游 MCP 同时连接：stdio（npx / uvx / 本地命令）与 streamable HTTP；
- 懒连接：某端点首次 invoke 到该上游时才拉起进程/建连；心跳保活；崩溃自动重启（指数退避）；
- **两级开关**（已确认）：
  - MCP 级：`mcp_servers.enabled` 关掉整个上游 → 其工具从所有端点视图消失；
  - 工具级：`tools_state.enabled` 关掉单个工具 → 从所有端点视图消失；
- 第三维度 = 组内可见性（分组勾选）；
- 同名工具冲突：自动加 `来源_` 前缀（如 `github_search_code`），status/get/invoke 一律使用索引里给出的名字，客户端无感知。

### 8.7 仿 Skills 加载：技能进索引（$smart 组）

- skills 适配器扫描 `~/.loadout/skills/` 下每个技能的 `SKILL.md`，解析 frontmatter；
- 技能在索引中的条目：`name` = 技能名、`description` = frontmatter 的 description、`category` = `技能`、`source` = `skills`；
- **get 技能条目 → 返回 SKILL.md 全文**（技能正文按需加载——"用 MCP 的壳装 skills 的灵魂"）；
- invoke 技能条目 → 同样返回 SKILL.md 全文（v1 不执行技能脚本；脚本执行留 v1.1，接口已预留）；
- 技能只出现在 `/mcp/$smart` 视图。

### 8.8 使用方提示词模板（"提示词锁死"的落地文本，供复制）

管理员可在后台查看/复制以下模板，建议作为使用方 agent 的 system prompt 开头：

```text
你连接的是一个聚合 MCP 网关，它只提供 3 个工具：status、get、invoke。
使用任何能力的流程被锁定为三步，禁止跳步：
1. 先用 status 查看可用工具（工具多时先无参数调用看分类总览，再按分类查询）；
2. 分析任务需要哪些工具，用 get 一次性批量加载这些工具的完整定义；
3. 严格按 get 返回的参数定义调用 invoke。
- 禁止在未 get 的情况下猜测参数直接 invoke。
- status 的结果要放在对话历史最靠前的位置，且保持内容不变（可命中服务商 prompt cache，降低费用）。
```

### 8.9 与 mcphub 的对比与代价（明示）

| | mcphub | Loadout |
|---|---|---|
| 路由方式 | 向量检索、语义匹配（可能选错） | 模型自己读索引选择，确定性 |
| 延迟 | 检索有延迟坑 | 无检索环节 |
| 依赖 | 向量库 | 无额外依赖 |
| 代价 | — | 每任务多 1~2 轮往返（status + get）；模型可能跳步猜参数，靠提示词强约束 |

---

## 9. 日志规范（用户要求）

- 实现：Go 标准库 `slog` + `lumberjack` 轮转；
- 输出：文件（`~/.loadout/logs/loadout.log`）+ 控制台，JSON 结构化；
- 固定格式：`时间 [等级] [源码相对路径:行号] 消息`

```
2026-08-15 15:00:01 [INFO] [plugins/vision/plugin.go:42] 视觉描述完成，缓存命中，耗时 3ms
```

- 源码路径为**仓库相对路径**（裁掉本机前缀，保证各机器一致）；
- 说明：Go 运行时只能取到行号，**列号无法获取**（已向用户说明）；
- 脱敏：所有 key、token、密码在任何日志输出前替换为 `sk-***` 形式，logger 层统一处理；
- 请求关联：每个请求生成 request_id，日志与响应头携带，方便定位。

---

## 10. skills 预设管理（已确认流程）

```
~/.loadout/skills/          ← 完整仓库（所有技能真实文件，永不删除）
.agents/skills/             ← 目标目录（固定 ~/.agents/skills，只放链接）
```

1. **技能安装**：后台搜索（`npx skills find`）→ 安装（`npx skills add <owner/repo> -y`）→ 把文件同步进 `~/.loadout/skills/<技能名>/`；备用模式：`git clone --depth 1`（config.go 可切换）；
2. **预设**：只存 JSON（名称 + 技能清单），创建/编辑/删除都在后台；
3. **切换预设**（settings.json 的 active_preset）：
   - 读取 `~/.agents/skills/.loadout-manifest.json`（Loadout 自己创建的条目清单）；
   - **只删除清单里记录的、且仍是链接类型的条目**——手动放进去的东西一律不动（已确认保守策略）；
   - 按预设技能清单，从仓库逐个链接到目标目录；
   - 写回新的 manifest。
4. **跨平台链接**（已确认）：
   - Linux：symlink（软链接）；
   - Windows：junction（目录联接，免管理员权限）；
   - 两者都失败：降级为复制目录；
5. 目标目录不存在时自动创建（含父目录）。

---

## 11. 管理后台（Vue 3 + shadcn-vue-cdn）

- 组件库：`shadcn-vue-cdn`（v0.0.4，UMD/ES 双格式），**Vite 构建时打进产物**，离线可用；
- 页面清单：
  1. 登录页（admin）；
  2. 概览：插件数量与自检状态、渠道状态、当前预设、最近错误；
  3. 模型渠道：channels.json 的增删改查、连通性测试；
  4. 能力路由：capability_routes.json 的可视化配置（模型 × 能力矩阵）；
  5. MCP 管理：上游服务器、单工具开关、分组勾选、endpoint key 管理（单 MCP/分组/$smart 三种路由方式）；
  6. Skills：仓库列表、搜索（npx skills find）、安装/更新/删除、预设管理、切换预设；
  7. 密钥：sk- key 创建（仅显示一次）、MCP endpoint key；
  8. 插件：列表 + 自检结果 + 手动触发自检；
  9. 设置：修改密码、日志级别、运行时设置。

---

## 12. 测试策略（TDD，用户硬性要求）

### 12.1 测试基建（第一个要写的东西）

| 组件 | 作用 |
|---|---|
| `testkit/fake-llm` | 可编程的 OpenAI 兼容假上游：httptest server，支持脚本化 SSE 回放、记录收到的请求、模拟失败 |
| `testkit/fake-mcp` | 内存 MCP 假上游：用官方 go-sdk 起内存 server，注册任意工具、记录调用 |
| `testkit/fixtures` | 样例消息、SSE 片段、图片 base64 等素材 |

### 12.2 TDD 流程（每个功能固定四步）

1. **RED**：先写 `*_test.go`，定义行为契约（正常流 + 每个失败分支）；
2. 运行测试，确认失败且失败原因是"功能未实现"；
3. **GREEN**：写最小实现让测试通过；
4. **REFACTOR**：整理代码，重跑测试保持绿色。

### 12.3 分层与门槛

- 单元测试：每插件核心逻辑，覆盖率 ≥ 80%；
- 集成测试：httptest 全链路（请求 → 管线 → fake-llm → 响应流解析）；
- 契约测试：SSE 序列化/解析、MCP 协议消息、JSON 数据模型读写；
- CI：GitHub Actions，Linux + Windows 双平台跑 `go test ./...` + `go vet` + 前端构建。

---

## 13. 打包与发布

| 平台 | 形态 |
|---|---|
| Linux | 单二进制（前端 go:embed）+ systemd 单元文件 + Dockerfile（可选） |
| Windows | Wails v3 桌面壳：托盘 + 窗口（内嵌管理页）+ 单实例锁，参考 go-tools-app；桌面模式只监听 127.0.0.1 |

- 服务化：Linux systemd；Windows 随桌面应用启动；
- 升级：数据目录与二进制解耦，替换二进制即升级；JSON 数据带 schema_version 字段用于迁移。

---

## 14. 里程碑

| 里程碑 | 内容 | 交付物 |
|---|---|---|
| M0 | 设计文档 + 仓库骨架 + testkit | 本文档、目录结构、fake-llm/fake-mcp |
| M1 | core：插件框架 + logger + store + config.go + linkfs | 带测试的核心框架 |
| M2 | admin-auth + admin-api + web 骨架 | 可登录的空后台 |
| M3 | model-gateway + vision 插件（TDD） | 视觉附加能力可用 |
| M4 | mcp-hub 插件（TDD） | status/get/invoke + 单 MCP/分组/$smart 三种路由方式 |
| M5 | skills 插件（TDD） | 仓库管理 + 预设切换 |
| M6 | 打包发布 | Linux 二进制 + Windows 安装包 |
| M7 | 文档完善 + 开源发布 | README、插件开发指南 |
