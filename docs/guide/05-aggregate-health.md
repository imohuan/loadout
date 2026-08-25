# 05 - 聚合模型与健康检查

> 本文讲 Loadout 如何把"多个真实模型+渠道"聚合成一个虚拟模型，并在失败时自动切换（failover），
> 以及背后的健康检查与故障切换机制。配套：[04-模型网关](./04-model-gateway.md)、[03-插件开发指南](./03-plugin-dev-guide.md)。

源码：`plugins/aggregate/`（plugin.go / proxy.go / strategy.go / health.go / checker.go）、`plugins/model-health/`。

## 1. 聚合模型是什么

聚合模型（aggregate）对外暴露一个**虚拟模型名**（如 `auto`），背后按顺序挂多个真实模型+渠道。
请求打到 `auto` 时，按 `Targets` 数组顺序尝试，某目标失败（非 2xx）就换下一个，任一成功即返回。
配置在 `aggregates.json`（结构见 `plugins/types` 的 `AggregateModel`）。

```json
{
  "name": "auto",
  "targets": [
    { "model": "gpt-4o",        "channel_id": "ch-openai" },
    { "model": "claude-3.5",    "channel_id": "ch-anthropic" },
    { "channel_base_url": "https://api.deepseek.com/v1" }
  ]
}
```

目标粒度优先级（高→低）：`ChannelBaseURL`（渠道级，按 base_url 组轮询 Key）> `ChannelIDs`（Key 多选）> `ChannelID`（单 Key）。

## 2. 它怎么介入转发

aggregate 订阅 model-gateway 的事件（见 `plugin.go`）：

```go
ctx.On(modelgateway.ProxyBeforeUpstream, svc.HandleProxyBeforeUpstream)   // 主力链路
ctx.On(modelgateway.ProxyUpstreamFailed, svc.HandleProxyUpstreamFailed)   // failover
ctx.On(modelgateway.ProxyUpstreamSucceeded, svc.HandleProxyUpstreamSucceeded) // 恢复
// 过渡期也保留旧 chat 事件
ctx.On(modelgateway.EventBeforeUpstream, svc.HandleBeforeUpstream)
ctx.On(modelgateway.EventUpstreamFailed, svc.HandleUpstreamFailed)
ctx.On(modelgateway.EventUpstreamSucceeded, svc.HandleUpstreamSucceeded)
```

- `ProxyBeforeUpstream`：检测到当前模型是聚合虚拟模型 → 拦截请求，自己循环转发到 `Targets`，不依赖 model-gateway 的渠道解析。
- 失败后通过 `ForwardSubRequest` 走网关主链路打到下一个目标（同样获得日志/额度）。

## 3. 失败策略分析（strategy.go）

`analyzeFailure` / `analyzeProxyFailure` 根据错误特征决定对该目标/渠道的处理：

| 错误特征（正则，不区分大小写） | 动作 | 说明 |
|---|---|---|
| `401` / `unauthorized` / `invalid api key` | **disable** | 鉴权失效，禁用该渠道 |
| `402` / `insufficient quota` / `balance` | **disable** | 额度/余额不足，禁用 |
| `429` / `rate limit` | **cooldown 5s** | 限流，冷却后重试 |
| `503` / `service unavailable` / `overload` | **cooldown 2s** | 服务不可用，冷却 |
| `timeout` | **cooldown 1s** | 超时，冷却 |
| `connection refused` / `network` | **cooldown 1s** | 网络错误，冷却 |
| 其它（含 AI 分析） | cooldown | 兜底冷却 |

- **disable**：该渠道标记为不可用，不再尝试（需人工或后台健康检查恢复）。
- **cooldown**：临时冷却 N 秒，期间跳过该目标，避免无效重试。

## 4. 健康检查与故障切换（health.go / model-health）

- **智能目标选择**：`selectAvailableTarget` 优先选"健康"的目标，跳过手动禁用与自动熔断（冷却中）的 Key；不可用目标不发起真实请求。
- **后台健康检查**：定时测试冷却中的渠道，自动恢复可用状态（`checker.go`）。
- **状态持久化**：健康状态保存到磁盘（`model_health` 表 / `model_health.json`），服务重启后保持，避免重启后重复踩坑。
- **模型级失败处理**：模型整体无可用渠道时记为 `model@`（无具体 Key），后续跳过该模型所有 Key，防止死循环反复选中同一不可用目标。

## 5. 性能收益（为何要做）

聚合 + 健康检查让"优先选健康渠道、失败透明切换、冷却避免无效重试"成为可能：
- 避免把请求打到已知不可用的渠道，减少上游 API 无用调用。
- 用户无感知切换，不影响业务逻辑。
- 健康状态跨重启保持，冷启动即具备"避坑"能力。

## 下一步

- 看 MCP 聚合（三工具、三种路由）→ [06-MCP 聚合](./06-mcp-hub.md)
- 看路由/健康数据的存储位置 → [08-数据存储](./08-data-storage.md)
