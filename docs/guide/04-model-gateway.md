# 04 - 模型网关与能力附加

> 本文讲 `/v1`（及 `/v2`）模型转发核心：请求如何归一化、能力插件如何介入、**视觉能力怎么附加**。
> 配套：[02-插件系统](./02-plugin-system.md)、[03-插件开发指南](./03-plugin-dev-guide.md)。

源码：`plugins/model-gateway/`（plugin.go / proxy.go / expand.go / types.go）。

## 1. 三套路由

model-gateway 注册了多组路由，按版本平行存在：

| Pattern | 说明 |
|---|---|
| `GET /v1/models` | v1 模型列表 |
| `/v1/{path...}`（任意方法） | v1 透明代理：原样转发到匹配渠道 |
| `GET /v2/models` | v2 模型列表，返回 `{渠道名}/{模型名}` |
| `/v2/{path...}` | v2 代理，按渠道名前缀锁定渠道组 |

v1 完全不动，v2 为新增能力（前缀路由）平行扩展。

## 2. chat 管线（/v1/chat/completions 归一化路径）

对 chat 请求，model-gateway 先把请求归一化为内部结构，再通过 **waterfall 事件 `chat:before-upstream`** 交给能力插件（如视觉）改写，最后清洗字段并转发上游。

```
请求到达
  → 归一化为 Pipeline（Messages / VisionText）
  → Waterfall(chat:before-upstream)   ← 能力插件（vision）在此改写 Messages
  → 字段清洗
  → 转发上游（带 reasoning 注入）
```

- `Pipeline` 结构（`types.go`）：`RequestID` / `Request` / `Messages` / `StreamWriter` / `ResponseWriter` / `HTTPRequest` / `Metadata`。
- 能力插件通过 `ctx.On(modelgateway.EventBeforeUpstream, handler)` 订阅，返回改写后的 `Pipeline`。
- `GatewayError`：插件在 waterfall 中返回它，网关据此生成 `{"error":{...}}` 响应（含 OpenAI 标准 type/status）。

## 3. proxy 透明代理管线（四事件）

`/v1/{path...}` 对任意路径（如 `responses`、`messages`、`chat/completions`）做**原样透传**：请求体原字节、query、header 都透传，不做字段白名单清洗。插件通过四个 waterfall 事件介入：

| 事件 | 时机 | 改写点 |
|---|---|---|
| `ProxyBeforeUpstream` | 入口一次（聚合改写/额度拦截） | Body / Path / Query / Header |
| `ProxyBeforeAttempt` | **每次渠道尝试前** | 同上（安检类，渠道上下文已就绪） |
| `ProxyAfterUpstream` | 非流式响应返回后 | 状态码 / Header / Body |
| `ProxyStreamChunk` | 流式逐 chunk | 单 chunk 字节（返回 nil = 删除） |

此外还有失败/成功通知事件：`ProxyUpstreamFailed` / `ProxyUpstreamSucceeded` / `ProxyAttemptFailed`（见 [05-聚合与健康检查](./05-aggregate-health.md)）。

> 为什么有 `BeforeUpstream` 和 `BeforeAttempt` 两套？前者只在入口执行一次（如聚合改写、额度拦截），
> 后者在**每次渠道尝试前**都执行，保证切换渠道/切换模型后安检仍然生效（见 [02](./02-plugin-system.md) 与 [03](./03-plugin-dev-guide.md) 的常见坑）。

## 4. 视觉能力附加（vision_v2）

目标：给"不支持视觉的模型"附加视觉能力——拦截 chat 请求 → 抽取图片 → 调视觉模型识别 → 用文字描述替换图片 → 流式把 reasoning 注入客户端。

实现要点（来自 `plugins/vision_v2/`）：

1. **抽取图片**：从请求 Messages 的 `image_url` 分段抽出。
2. **选路**：用 `SelectCapabilityRoutes(routes, "vision", model, scope)` 判断该 model+渠道是否要附加视觉、走哪个视觉模型（`ViaOptions`）。
3. **调视觉模型**：通过 `model-gateway.ForwardSubRequest` 走网关主链路（自动获得日志/额度/failover，不产生顶级日志行）。
4. **替换图片为文字**：把 `image_url` 分段替换为视觉模型返回的文字描述。
5. **流式 reasoning 注入**：通过 `Pipeline.StreamWriter` 把视觉识别的 reasoning delta 实时输出到客户端；非流式时只写进 Messages。
6. **事件订阅**：挂 `ProxyBeforeAttempt`（渠道上下文就绪后才匹配渠道约束）+ `ProxyStreamChunk`（流式逐块）+ `ProxyAfterUpstream`（非流式收尾）。
   - 注意：vision 对 `__sub_request` 标记的请求**早退**，避免识别视觉模型自身时递归触发。

能力路由表（`capability_routes.json`，结构见 `plugins/types` 的 `CapabilityRoute`）示例：

```json
{
  "capability": "vision",
  "models": ["gpt-4o", "claude-*"],
  "channel_ids": ["ch-openai"],
  "route": "proxy",
  "via_options": [{ "via_model": "qwen-vl-max", "channel_id": "ch-vision" }]
}
```

## 5. 字段清洗与请求模型匹配

- `Expand`/`expand.go`：处理模型名展开、聚合目标解析。
- 字段过滤（`plugins/field-filter`）：订阅 `ProxyBeforeAttempt`（请求方向）与 `ProxyAfterUpstream`（响应方向），按能力路由表的 `FieldRules` 剔留字段；流式响应不做字段级处理。
- 敏感词过滤（`plugins/sensitive-filter`）：类似机制，按 `Replacements` 替换；fail-open（识别失败不拒绝请求，按透传处理）。

## 6. 能力路由语义（重点）

`SelectCapabilityRoutes`（`plugins/types`）统一了所有能力插件的选择策略：

- **native（及历史 error）命中即短路返回** —— 豁免/降级优先，**不依赖表内顺序**。
- **proxy 全部收集** —— 多条代理规则叠加（字段过滤多条规则合并）。
- 返回 `nil` = 无匹配 → 调用方按透传处理。

> 这意味着"豁免行"若用宽通配 `*` 会屏蔽同 model+渠道的 proxy 替换规则。设计能力路由表时要精确限定 model/渠道。

## 下一步

- 看聚合如何订阅失败/成功事件做 failover → [05-聚合与健康检查](./05-aggregate-health.md)
- 看能力路由表字段 → [08-数据存储](./08-data-storage.md)
