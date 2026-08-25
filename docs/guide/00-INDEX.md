# Loadout 学习文档（AI 快速上手指南）

> Loadout 是一个把所有 MCP 工具聚合后只暴露 3 个入口、按需加载工具定义的轻量网关——
> 用 MCP 的壳装 skills 的灵魂；附赠给任何模型附加任何能力（视觉/TTS/图像/视频）的模型网关，
> 以及 skills 预设管理器。
>
> 本文档集（位于 `docs/guide/`）面向**第一次接触本项目的 AI/开发者**，帮助你在最短时间内
> 建立准确心智模型：项目怎么跑、各功能怎么协作、**插件系统怎么扩展**（如何新增一个插件、插件内能做什么）。
> 文档之间用相对路径互相引用，建议按下方阅读路径顺序读。

## 一句话定位

- 语言：Go（后端）+ Vue 3（前端）；平台：Linux 服务版 / Windows 桌面版（Wails）。
- 对外单端口 `:3000`，三类入口按路径分发：

| 路径 | 入口 | 认证 |
|---|---|---|
| `/v1/*` | 模型 API（OpenAI 兼容） | `Authorization: Bearer sk-xxx` |
| `/mcp/*` | MCP 端点（status/get/invoke 三工具） | 自定义 header（默认 `X-Loadout-Key`） |
| 其余 | 管理后台（Vue） | 登录会话（JWT Cookie） |

## 阅读路径

- **新人不求甚解**：[01-架构总览](./01-architecture.md) → [02-插件系统](./02-plugin-system.md) → [03-插件开发指南](./03-plugin-dev-guide.md)
- **要扩展插件（最重要）**：直奔 [03-插件开发指南](./03-plugin-dev-guide.md)（含新增插件 step-by-step + 最小可运行骨架）
- **理解各业务能力**：[04-模型网关](./04-model-gateway.md)、[05-聚合与健康检查](./05-aggregate-health.md)、[06-MCP 聚合](./06-mcp-hub.md)、[07-skills 预设](./07-skills-presets.md)
- **运维 / 部署**：[08-数据存储](./08-data-storage.md)、[09-管理后台 API](./09-admin-api.md)、[10-部署与排错](./10-deployment.md)

## 文档地图

| 文件 | 内容 |
|---|---|
| [00-INDEX.md](./00-INDEX.md) | 本文件：总导航 + 阅读路径 |
| [01-architecture.md](./01-architecture.md) | 总体架构、三入口、core/plugins 分层、启动流程、目录布局 |
| [02-plugin-system.md](./02-plugin-system.md) | 插件框架机制：Manifest、Context、事件总线、拓扑装配、自检、路由 |
| [03-plugin-dev-guide.md](./03-plugin-dev-guide.md) | **如何新增插件**：脚手架步骤 + 两支柱 + 事件订阅 + 自检 + 常见坑 |
| [04-model-gateway.md](./04-model-gateway.md) | /v1 模型转发：chat 管线 + proxy 管线 + 能力路由 + 视觉附加 |
| [05-aggregate-health.md](./05-aggregate-health.md) | 聚合模型轮询、failover、健康检查与故障切换 |
| [06-mcp-hub.md](./06-mcp-hub.md) | MCP 聚合网关：三工具、三种路由、动态分发、日志 |
| [07-skills-presets.md](./07-skills-presets.md) | 技能仓库、预设切换、跨平台链接、监听 |
| [08-data-storage.md](./08-data-storage.md) | 运行时数据：JSON store + SQLite 双轨、~/.loadout 布局 |
| [09-admin-api.md](./09-admin-api.md) | 管理后台 REST API 契约 |
| [10-deployment.md](./10-deployment.md) | 构建、运行、Linux 一键部署、Windows 桌面、排错 |
| [appendix/00-evolution.md](./appendix/00-evolution.md) | 历史演进文档索引（旧 docs 已归档，以本 guide 为准） |

## 原 docs 去哪了

本 `docs/guide/` 是对旧 `docs/` 混乱文档的重新整理。所有旧文档（DESIGN / IMPLEMENTATION /
各类功能计划 / plans / desktop 等）已移至 **`docs/archive/`** 保留历史，不再作为权威参考；
其索引与一句话摘要见 [appendix/00-evolution.md](./appendix/00-evolution.md)。

> 注意：旧文档部分内容已过时（例如曾描述"纯 JSON 存储"，现实已是 JSON + SQLite 双轨），
> 一切以 `docs/guide/` 与当前代码为准。

## 代码事实来源

本指南所有 API、常量、事件名、存储表名均核对自当前代码：
- 插件框架：`core/plugin/`
- 装配入口：`core/servercore/server.go` → `plugin.Load(plugins.All(), …)`
- 插件登记：`plugins/registry.go` 的 `All()`
- 事件/载荷类型：`plugins/model-gateway/types.go`
- 存储表：`core/db/migrate.go`
