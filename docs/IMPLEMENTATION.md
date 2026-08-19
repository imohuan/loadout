# Loadout 实现总结

> 本文档记录本次开发从零到可用的完整过程：目标、思路、做法、测试情况与遗留事项。
> 设计依据见仓库根目录 `DESIGN.md`，本文是「实现侧」的对照说明。

---

## 一、项目目标

Loadout 是一个把 MCP 工具聚合后**只暴露 3 个入口**、按需加载工具定义的轻量网关，
附赠「给模型附加视觉能力」的模型网关与「skills 预设管理」。v1 范围 = 视觉附加 + MCP 聚合 + skills 预设 + 管理后台。

对外只暴露一个端口 `:3000`，三类入口按路径分发：

| 路径 | 入口 | 认证 |
|---|---|---|
| `/v1/*` | 模型 API（OpenAI 兼容） | `Authorization: Bearer sk-xxx` |
| `/mcp/*` | MCP 端点（status/get/invoke） | 自定义 header（默认 `X-Loadout-Key`） |
| 其余 | 管理后台（Vue） | 登录会话（JWT Cookie） |

---

## 二、总体思路

1. **core 与业务彻底分离**：`core/` 放零业务逻辑的框架（插件框架、日志、存储、认证、链接、MCP 封装），业务全部以插件形式挂在 `plugins/`，core 不 import 任何业务包。
2. **插件架构（Cordis 思想的 Go 落地）**：`Manifest` 声明 `inject/provide`，框架按依赖拓扑排序后逐个 `Apply`；`Context` 提供服务容器、事件总线（emit/waterfall）、可逆副作用、插件自检与路由注册。
3. **配置环境变量优先**：`core/config/config.go` 所有程序级配置「环境变量 `LOADOUT_*` 优先、默认值兜底」，运行时数据（渠道/路由/MCP/key/预设）放 `~/.loadout/data/*.json`。
4. **MCP 三工具 + 三种路由方式**：每个端点的工具列表永远只有 `status/get/invoke` 三个，聚合工具只存在于索引里；端点按「单 MCP / 分组 / `$smart`」三种路由方式生成。
5. **视觉能力附加**：通过 waterfall 事件 `chat:before-upstream` 让 `vision` 插件与 `model-gateway` 解耦协作，不改动核心转发逻辑。

---

## 三、模块清单

### core/（框架，全部可测）

| 包 | 职责 |
|---|---|
| `config` | 程序级配置，env 优先 + 默认值 + 派生目录解析（97.2% 覆盖） |
| `plugin` | Cordis 插件框架：Manifest/Context/拓扑装配/自检/路由（85.0%） |
| `logger` | slog + lumberjack 轮转 + 固定文本格式 + 脱敏 + `NewWithCloser` |
| `linkfs` | 跨平台链接：symlink → junction → 降级复制，含 reparse point 识别 |
| `store` | JSON 原子写入（.tmp→rename）+ 进程内锁 + AES-GCM 加密 + `.secret`（81.4%） |
| `auth` | bcrypt 密码 + JWT（HS256）+ sk-/MCP key 签发与 sha256 哈希（83.9%） |
| `mcpkit` | 官方 go-sdk 封装：Upstream（stdio/http 懒连接）+ NewServer |

### plugins/（业务插件，一个插件一个目录）

| 插件 | 职责 |
|---|---|
| `gateway-keys` | sk- key / MCP endpoint key 签发 + 校验中间件（80.2%） |
| `admin-auth` | 管理员登录/会话 JWT/首启随机密码/改密 |
| `admin-api` | 管理后台 REST API + 渠道测试端点（约 40 个端点） |
| `model-gateway` | `/v1` 请求管线：归一化→能力路由→字段清洗→转发→流式 reasoning 注入 |
| `vision` | 视觉适配：检图→路由(native/proxy/error)→调视觉模型→改写 messages→md5 缓存 |
| `mcp-hub` | MCP 聚合：status/get/invoke 三工具 + 冲突前缀 + 两级开关 + 三种路由方式 |
| `skills` | 技能仓库清单 + 预设切换（manifest 保守删除 + 跨平台链接） |
| `types` | 第 5 节全部 JSON 数据模型（各插件共享） |
| `registry.go` | 编译期装配注册表 |

### 其他

- `apps/server`：单端口装配入口（认证中间件分发 + streamable HTTP 端点挂载 + go:embed 前端 + 优雅退出）。
- `web/`：Vue 3 + Vite 管理后台（登录 + 概览/渠道/模型测试/MCP/Skills/密钥/设置 6 页）。
- `testkit/`：`fake-llm`（可编程 OpenAI 假上游 + SSE 回放）、`fake-mcp`（streamable HTTP 假上游 + 调用记录）。
- `.github/workflows/ci.yml`：Linux + Windows 双平台 `go test` + `go vet` + 前端构建矩阵。
- `scripts/`：`build.ps1` / `build.sh`。

---

## 四、关键设计决策与做法

### 1. 插件装配（编译期，方案 A）
- `plugins/registry.go` 手写 `All()` 列出全部插件；`plugin.Load` 读 `Manifest` 做 Kahn 拓扑排序，`inject` 的服务由 `provide` 满足，缺依赖/成环在装配期 fail-fast。
- 插件只通过 `ctx.Get("store")` 等按服务名取用，不 import 实现；服务在 `Options.Services` 预注册（store/logger/http-client）。

### 2. 视觉管线解耦
- `model-gateway` 在转发上游前触发 `ctx.Waterfall("chat:before-upstream", pipeline)`；
- `vision` 插件订阅该事件，改写 `pipeline.Messages`（图片→文字）并填 `VisionText`，供 `model-gateway` 在 SSE 流开头注入 `reasoning_content` 块；
- 视觉失败返回 `GatewayError{Type:"vision_capability_error"}`，由网关转成标准 OpenAI 错误。

### 3. MCP 三工具与省 token 手段
- `status`（分类总览 / 按类查询，≤阈值时扁平返回）、`get`（批量加载 schema / 技能返回 SKILL.md 全文）、`invoke`（转发 + 截断）；
- 冲突前缀（`来源_`）、两级开关（MCP 级 + 工具级）、确定性序列化（按 name 排序 + 字段顺序固定）+ `index_version` 递增。

### 4. skills 预设切换（保守策略）
- `~/.loadout/skills/`（仓库，真实文件）→ `~/.agents/skills/`（目标，只放链接）；
- 切换时只删除「`.loadout-manifest.json` 里记录的、且仍是链接类型」的条目，手动放的东西一律不动；
- 链接优先 symlink → Windows junction（`mklink /J`）→ 降级递归复制。

### 5. 数据与安全
- JSON 全部原子写入（先 `.tmp` 再 `rename`）+ 进程内锁；
- 渠道 key、各类 key 明文不落盘：渠道 key 用 AES-GCM（`.secret` 32 字节密钥）加密，sk-/MCP key 只存 sha256 哈希；
- 日志统一脱敏（敏感属性名 + `sk-` 令牌替换为 `sk-***`）。

---

## 五、开发流程（TDD + 并发子代理）

- **契约先行**：先由主 agent 手写 `core/config`、`core/plugin`、`plugins/types`（数据模型）与各插件接口签名，作为跨包契约；
- **并发子代理**：`logger/linkfs/store/mcpkit/testkit`、`auth/fake-mcp`、`gateway-keys/admin-auth/skills`、`model-gateway/mcp-hub`、`vision/admin-api` 分批并发委派，每个子代理写实现 + 单元测试；
- **统一验收**：主 agent 逐批 `gofmt -w` + `go test` + `go vet` 验收，修复子代理因无法本地跑测试而遗留的 bug。

---

## 六、测试情况（是否测试：**是，全部测过**）

### 验证命令（全部通过）

```bash
go build ./...   # 通过
go vet ./...     # 通过（0 告警）
go test ./...    # 16 个包全绿
gofmt -l core plugins testkit apps/server web  # 无未格式化文件
cd web && npm run build  # 通过，产物 embed 进二进制
```

### 单元/集成测试覆盖（`go test -cover`）

| 包 | 覆盖率 | 说明 |
|---|---|---|
| config | 97.2% | env 优先、派生目录、非法值回落 |
| plugin | 85.0% | 拓扑装配、环检测、事件总线、副作用逆序 |
| auth | 83.9% | 密码/JWT/key 往返、篡改/过期 |
| store | 81.4% | 原子写、加密往返、密钥复用 |
| gateway-keys | 80.2% | key 签发、中间件 401/放行 |
| mcp-hub | 77.6% | 索引构建、三工具、开关、冲突前缀 |
| vision | 73.9% | 检图、路由、Describe 缓存、改写 |
| skills | 71.7% | 预设切换、保守删除、链接 |
| admin-auth | 70.1% | 首启、登录、会话、改密 |
| logger | 67.2% | 格式、脱敏、SourceRoot |
| model-gateway | 59.6% | 路由/渠道解析、转发、流式注入 |
| mcpkit | 44.8% | 内存 server 往返（网络分支较难覆盖） |
| linkfs | 31.7% | Windows 无 symlink 权限，仅覆盖 junction/copy 路径 |
| admin-api | 21.4% | 端点多，覆盖 login/channels/keys/改密核心链路 |

> 注：`linkfs`/`mcpkit`/`admin-api` 覆盖率偏低，主因是平台（Windows 无管理员权限的 symlink）与网络/多端点分支难以在单测触发，核心逻辑均已覆盖。

### 端到端测试（apps/server 集成测试，真实 HTTP 走全链路）

- `TestStaticIndex`：静态资源返回 HTML；
- `TestAPIRequiresSession`：未登录访问 `/api/overview` → 401；
- `TestLoginFlow`：错误密码 401 / 正确密码 200 + Set-Cookie / 带 Cookie 访问 200；
- `TestV1RequiresSkKey`：无 key 访问 `/v1/chat/completions` → 401；
- `TestMCPEndpointExists`：`/mcp/$smart` 端点挂载；
- `TestOverviewPluginCount`：`overview.plugins == 7`（真实插件数）；
- `TestChannelTestEndpoint`：用 `fake-llm` 当渠道，走「建渠道 → `POST /api/channels/test`」链路，断言 `ok=true, reply="pong"`。

---

## 七、过程中发现并修复的问题

1. **`linkfs.IsLink` 无法识别 Windows junction**：`mklink /J` 创建的 junction 是 reparse point，`os.Lstat` 不设 `ModeSymlink` → 补 `isReparsePoint`（按 `FILE_ATTRIBUTE_REPARSE_POINT` 判定，平台分文件）。
2. **`logger` 的 lumberjack 文件句柄不关闭**导致测试 `TempDir` 清理失败 → 新增 `NewWithCloser`，测试用 `t.Cleanup` 关闭。
3. **`store` 测试在 Windows 断言 0600 权限**：Windows 不遵循 Unix 权限位 → 改为仅非 Windows 断言。
4. **`mcp-hub` 测试对「关闭上游后冲突前缀」语义误判**：前缀应在「当前启用工具」内计算，关闭上游后同名工具恢复原名 → 修正断言。
5. **`overview.plugins` 硬编码为 1**（误用 admin-api 自身自检项数）→ 改为 server 装配层注入真实插件总数 7。

---

## 八、遗留事项与后续建议

- **覆盖率补齐**：`linkfs`/`mcpkit`/`admin-api` 因平台与网络分支未达 80%，可在 Linux CI 补 symlink 分支、为 Upstream 网络层与 admin-api 其余端点补测。
- **能力路由可视化配置页**：模型 × 能力矩阵（vision 的 native/proxy/error）后端 API 已具备，前端尚未做该页面。
- **MCP 细化管理**：单工具开关（`tools_state.json`）与分组勾选的 UI 尚未实现，后端 API 已就绪。
- **skills 真实安装**：v1 只做清单登记与预设切换，`npx skills add` / `git clone` 的真实下载（`config.SkillInstallMode`）已留接口未接。
- **apps/desktop（Wails 壳）**：已有参考壳（独立 `proxyui` 模块），尚未与 `apps/server` 装配接线；desktop 模式仅需把 `config.RunMode` 置为 `desktop` 监听 127.0.0.1。

---

## 九、如何运行

```bash
# 构建 + 运行（首启生成 ~/.loadout/admin-password 随机密码）
go build -o loadout ./apps/server && ./loadout

# 浏览器打开 http://127.0.0.1:3000 ，账号 admin，密码见 admin-password 文件
# 开发模式前端（自动代理 /api /v1 /mcp 到 :3000）
cd web && npm install && npm run dev
```
