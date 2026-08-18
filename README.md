# Loadout

> 一个把所有 MCP 工具聚合后只暴露 3 个入口、按需加载工具定义的轻量网关——用 MCP 的壳装 skills 的灵魂。
> 附赠：给任何模型附加任何能力（视觉/TTS/图像/视频）的模型网关，以及 skills 预设管理器。

Loadout 对外只暴露一个端口 `:3000`：

| 路径 | 入口 | 认证 |
|---|---|---|
| `/v1/*` | 模型 API（OpenAI 兼容） | `Authorization: Bearer sk-xxx` |
| `/mcp/*` | MCP 端点（status/get/invoke 三工具） | 自定义 header（默认 `X-Loadout-Key`） |
| 其余 | 管理后台（Vue） | 登录会话（JWT Cookie） |

## 核心特性

- **MCP 聚合网关**：聚合进来的所有工具（上游 MCP 工具 + 已安装技能）只存在于索引里，客户端看到的工具永远只有 3 个——`status`、`get`、`invoke`。工具定义按需加载，工具越多不会越贵越慢。
- **模型网关 + 视觉附加**：给不支持视觉的模型附加视觉能力（拦截 `/v1/chat/completions` → 抽取图片 → 调视觉模型 → 用文字描述替换图片 → 流式注入 reasoning 块）。
- **skills 预设管理**：技能仓库（`~/.loadout/skills/`）与目标目录（`~/.agents/skills/`）分离，一键切换预设、跨平台链接（symlink / junction / 降级复制）。
- **插件架构**：Cordis 思想的 Go 实现，一切皆插件，core 不 import 任何业务。

## 快速开始

```bash
# 构建
go build -o loadout ./apps/server

# 运行（首次启动会在 ~/.loadout/admin-password 生成随机管理员密码）
./loadout

# 登录管理后台（浏览器打开 http://127.0.0.1:3000）
# 账号 admin，密码见 ~/.loadout/admin-password
```

数据目录 `~/.loadout/`：

```
~/.loadout/
├── data/            # JSON 数据（渠道、路由、MCP、key、预设…），原子写入
│   └── .secret      # 本地密钥（0600，AES 加密 + JWT 签名）
├── skills/          # 技能完整仓库（永不删除）
├── logs/            # loadout.log（轮转）
├── backups/         # 备份
└── admin-password   # 首启随机密码（改密后自动删除）
```

## 配置

程序级配置在 `core/config/config.go`，全部支持**环境变量优先、默认值兜底**（前缀 `LOADOUT_`）：

```bash
LOADOUT_SERVER_ADDR=:4000 \
LOADOUT_LOG_LEVEL=debug \
LOADOUT_UPSTREAM_BASE_URL=http://127.0.0.1:3001/v1 \
./loadout
```

常用项：`LOADOUT_SERVER_ADDR`、`LOADOUT_LOG_LEVEL`、`LOADOUT_UPSTREAM_BASE_URL`、`LOADOUT_DEFAULT_VISION_MODEL`、`LOADOUT_HOME_DIR`、`LOADOUT_RUN_MODE`（server/desktop）、`LOADOUT_MAX_TOOL_RESULT_CHARS` 等。完整清单见 `core/config/config.go`。

## 三个 MCP 工具（提示词锁死）

使用方 agent 的 system prompt 建议加上：

```text
你连接的是一个聚合 MCP 网关，它只提供 3 个工具：status、get、invoke。
使用任何能力的流程被锁定为三步，禁止跳步：
1. 先用 status 查看可用工具（工具多时先无参数调用看分类总览，再按分类查询）；
2. 分析任务需要哪些工具，用 get 一次性批量加载这些工具的完整定义；
3. 严格按 get 返回的参数定义调用 invoke。
- 禁止在未 get 的情况下猜测参数直接 invoke。
- status 的结果要放在对话历史最靠前的位置，且保持内容不变（可命中服务商 prompt cache，降低费用）。
```

## 文档

- [设计文档](./docs/DESIGN.md) - 总体架构与设计理念
- [实现细节](./docs/IMPLEMENTATION.md) - 代码实现说明
- [API 文档](./docs/API.md) - HTTP API 接口文档
- [健康检查与故障切换](./docs/HEALTH-CHECK.md) - 智能聚合转发机制详解

## 开发

```bash
# 测试（Linux + Windows 均支持）
go test ./...

# 静态检查
go vet ./...

# 前端（可选，构建后覆盖 web/dist）
cd web && npm install && npm run build
```

## 目录结构

```
core/       # 核心框架（config/plugin/logger/store/auth/linkfs/mcpkit，零业务逻辑）
plugins/    # 业务插件（gateway-keys/admin-auth/admin-api/model-gateway/vision/mcp-hub/skills）
apps/server # Linux 入口（单二进制 + go:embed 前端）
web/        # Vue 3 + Vite 管理后台
testkit/    # 测试基建（fake-llm / fake-mcp）
scripts/    # 构建脚本
```

## 许可证

[MIT](LICENSE)
