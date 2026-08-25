# 附录 - 历史演进文档索引

> 本文件是**旧 `docs/` 文档的索引与一句话摘要**。这些文档已统一归档到 `docs/archive/`，
> 内容可能**过时**（例如曾描述"纯 JSON 存储"，现实已是 JSON + SQLite 双轨）。
> **一切以 `docs/guide/`（本文集）与当前代码为准**；本附录仅供追溯决策与历史背景。

## 设计 / 实现类

| 原文件 | 摘要 | 现状 |
|---|---|---|
| `archive/DESIGN.md` | v1 初始设计：三入口、插件架构、MCP 聚合、视觉附加、skills 预设 | 总体方向仍有效；存储/部分细节已演进 |
| `archive/DESIGN-REVIEW.md` | 对 DESIGN 的疏漏审查（缺 API 契约、默认渠道语义、热更新等） | 多数已由实现与本文集补齐 |
| `archive/IMPLEMENTATION.md` | 从零到可用的实现对照说明 | 参考实现思路；部分模块已重构 |
| `archive/REFACTOR-PLAN.md` | 模型路由与数据层全面重构（迁 SQLite） | 已落地——见 [08-数据存储](./08-data-storage.md) |

## 功能类

| 原文件 | 摘要 | 对应本文集 |
|---|---|---|
| `archive/API.md` | 管理后台 REST API 契约（早期） | 已整合进 [09-管理后台 API](./09-admin-api.md) |
| `archive/CAPABILITY-PLUGIN-GUIDE.md` | 能力插件扩展指南（SelectCapabilityRoutes + ForwardSubRequest） | 已整合进 [03-插件开发指南](./03-plugin-dev-guide.md) |
| `archive/HEALTH-CHECK.md` | 健康检查与故障切换机制 | 已整合进 [05-聚合与健康检查](./05-aggregate-health.md) |
| `archive/ROUTE-LOG-ARCHITECTURE.md` | 转发日志架构 | 对应 [08](./08-data-storage.md) 的 route_requests/route_attempts |
| `archive/ROUTE-LOG-CHANNEL-REF.md` | 路由日志渠道引用重构 | 同上 |
| `archive/VISION-ROUTE-LOG-PLAN.md` | 视觉路由日志计划 | 已被 vision_v2 实现，见 [04-模型网关](./04-model-gateway.md) |
| `archive/SENSITIVE-FILTER-PLAN.md` | 敏感词过滤计划 | 已实现 sensitive-filter 插件，见 [04](./04-model-gateway.md) |
| `archive/STARTUP-ARCHITECTURE.md` | 启动架构（早期） | 已整合进 [01-架构总览](./01-architecture.md) |
| `archive/LLM提示缓存机制.md` | LLM 提示缓存原理（通用，厂商对比） | 背景知识，不随本项目变 |

## 桌面 / 计划类

| 原文件 | 摘要 |
|---|---|
| `archive/desktop/sso-login.md` | 桌面端 SSO 登录 |
| `archive/desktop/troubleshooting-README.md` | 桌面端排错 |
| `archive/plans/*.md`（20+ 篇） | 按日期的能力方案草稿（MCP 分析面板、渠道多 key、聚合、视觉 v2、route-log、volc 免额等） |
| `archive/错误修复计划.md` / `archive/问题.md` | 中文零散问题/修复记录 |

> 计划类文档是"决策当时的思考"，落地后以代码和本文集为准；如需追溯某功能的来龙去脉，可在 `archive/plans/` 按文件名日期检索。
