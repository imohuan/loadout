# field-filter 字段过滤能力

按能力路由表（capability_routes）配置，对**请求 / 响应方向**的 **header 与 body** 做剔除或白名单保留，按四个象限覆盖常见 agent 兼容性问题。

## 用途

agent 客户端（Codex、元宝等）会在请求体 / 请求头携带自定义字段，部分上游（如腾讯 copilot 网关）严格解析导致 400/401：
- 请求体：`client_metadata` 触发 `DisallowUnknownFields` 报 `json: unknown field`
- 请求头：`x-api-key` / `api-key` 覆盖渠道 `Authorization` 导致 401
- 响应体 / 响应头：上游返回客户端不认识的字段或敏感内部头

## 腾讯 copilot 网关示例（请求体+请求头都配）

> ⚠️ `channel_base_urls` 必须与**渠道表里的 base_url 精确一致**（含 `/v1`、`/v2` 等版本段，仅忽略尾斜杠）。
> 渠道 base_url 若是 `https://copilot.tencent.com/v1`，路由里写 `https://copilot.tencent.com`（漏版本段）会**静默不命中**。
> 不确定时在管理后台「能力路由」用渠道选择器选渠道级，保存的即为精确值。

```json
[{
  "capability": "field_filter",
  "models": ["*"],
  "channel_base_urls": ["https://copilot.tencent.com/v1"],
  "route": "proxy",
  "field_rules": {
    "request_strip": ["client_metadata"],
    "request_header_strip": ["X-Api-Key", "Api-Key"]
  }
}]
```

`models` 匹配的是**请求体里的 model 字段**（聚合模型请求时是该聚合虚拟名）；`*` 通配全命中，前缀 `gpt-*` 按前缀匹配。

## 字段规则（field_rules）四象限

| 字段 | 作用 |
|---|---|
| `request_strip` | 请求体剔除的字段路径（点路径支持嵌套，如 `a.b.c`） |
| `request_keep` | 请求体白名单：只保留这些字段（顶层 key） |
| `request_header_strip` | 请求头剔除（大小写不敏感） |
| `response_strip` | 非流式响应体剔除的字段路径 |
| `response_keep` | 非流式响应体白名单（顶层 key） |
| `response_header_strip` | 响应头剔除（大小写不敏感） |

规则：

- `request_keep` / `response_keep` 非空时走白名单（只保留），忽略同方向 `strip`。
- body 字段路径支持顶层 key 与点路径嵌套（`a.b.c`）；Keep 白名单仅支持顶层 key。
- 无字段命中时原字节透传（不重写 body）；非 JSON body 不处理。
- `request_header_strip` 在 `proxy:before-upstream` hook 改 `pipe.Request.Header`（之后 model-gateway 复制 headers 到上游请求时自动生效）。
- `response_header_strip` 在 `proxy:after-upstream` hook 改 `Response.Header`。
- **流式响应（SSE）不做字段级处理**（增量 delta 无法删字段），请勿对流式模型配置响应过滤。
- 未命中路由 / route=native / error / 未配置 field_rules → 原样透传，绝不拒绝请求（fail-open）。

## 替代 model-gateway 写死逻辑

此前 `proxy.go` 写死了腾讯 copilot 网关的 `stripAltAuth`（剔 X-Api-Key/Api-Key）——该定向规则现可通过本插件配置通用，渠道级匹配由 `channel_base_urls` 限定，不需再在网关硬编码。

## 管理后台

能力路由走既有 capability_routes 体系（SQLite `field_rules_json` 列 + JSON 文件兜底），`field_rules` 字段由管理后台 CRUD 直接透传。