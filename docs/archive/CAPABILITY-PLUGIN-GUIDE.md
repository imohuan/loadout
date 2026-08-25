# 能力插件扩展指南：SelectCapabilityRoutes + ForwardSubRequest

> 面向：在 loadout 网关里新增"能力插件"（capability plugin）的开发者。
> 本文档介绍两个复用支柱——**路由选择函数** `SelectCapabilityRoutes` 与**子请求通道** `ForwardSubRequest`——以及新能力接入的完整步骤与陷阱清单。
> 现状参考：vision_v2（视觉）、sensitive-filter（敏感词）、field-filter（字段过滤）、request-log（完整日志）四个插件已在用。

---

## 1. 两个支柱（30 秒概览）

能力插件解决两类问题：

| 支柱 | 解决什么 | 一句话 |
|---|---|---|
| `SelectCapabilityRoutes` | **查表选路**：请求命中能力路由表里哪条规则（native 透传 / proxy 代理） | 统一"豁免优先 + 代理叠加"的选择语义，五插件共用 |
| `ForwardSubRequest` | **网关内部请求收口**：插件自己要调上游（视觉识别、续流等）时，走网关主链路而非自建 HTTP | 自动获得 request-log / 额度 / failover，不产生顶级日志行 |

**核心原则**：所有上游请求只能从 model-gateway 一个口子出去，不允许插件"走后门"直接 `http.Client.Do`。

---

## 2. SelectCapabilityRoutes：统一路由选择

### 2.1 签名

```go
// plugins/types/types.go
func SelectCapabilityRoutes(routes []CapabilityRoute, capability, model string, scope ChannelRequestScope) []*CapabilityRoute
```

### 2.2 选择语义（关键）

对路由表逐行匹配（`Capability` + `MatchModels` + `MatchChannelScopeEx` 全命中才算匹配）：

1. **非 proxy 路由（native，及历史 error 数据）命中 → 短路返回**：豁免/降级优先，**不依赖 position 排序**。历史 `route="error"` 数据运行时自动按 native（透传）处理（"不支持就不管他"）。
2. **proxy 路由 → 全部收集**：多条 proxy 都命中就都返回（叠加规则，如字段过滤多条规则合并）。
3. **返回 nil = 无匹配** → 调用方按透传处理。

**注意**：native 短路会屏蔽同 model+channel 的其它 proxy 规则。若替换与豁免要并存，豁免行（native）必须精确限定 model/渠道，避免宽通配（`*`）误伤。

### 2.3 匹配函数

```go
// 模型匹配：* 全匹配、prefix* 前缀匹配、精确匹配
MatchModels(models []string, model string) bool

// 渠道作用域匹配（channel_ids / channel_base_urls 与请求上下文求交集）
MatchChannelScopeEx(channelIDs, channelBaseURLs []string, req ChannelRequestScope) bool

// 请求渠道上下文：从 pipe.Metadata 解析
type ChannelRequestScope struct {
    IDs      []string // 实际渠道 key id 集合
    BaseURLs []string // 实际渠道组 base_url（归一化比较）
}
scope := types.ChannelScopeFromMetadata(pipe.Metadata, s.requestChannelBaseURLs)
```

- `ChannelScopeFromMetadata` 解析 `__current_channel`（单 key）/ `__channel_candidates`（候选 key 集）/ `__current_channel_base_url`（渠道组地址），并支持 `__channel_hint`（v2 前缀渠道名）兜底——入口阶段渠道未定时按 hint 反查渠道组。
- `requestChannelBaseURLs(term)` 闭包：按 key id 精确匹配或按 ChannelName 组匹配，返回 base_url 列表。**每个插件自实现**（依赖各自 repo/store），模式见 2.4。

### 2.4 新能力插件接入步骤（路由部分）

1. **定义 capability 名**（如 `"my_capability"`），写入能力路由表 `capability_routes`（capability/models/channel_ids/channel_base_urls/route/via_options 等字段）。
2. **实现 `requestChannelBaseURLs(term string) []string`**：按 id / ChannelName 反查 base_url（含禁用渠道过滤 `ManualEnabled`）。
3. **hook 里组装 scope 并选路**：

```go
func (s *Service) HandleProxyBeforeUpstream(payload any) (any, error) {
    pipe, ok := payload.(*modelgateway.ProxyPipeline)
    if !ok || pipe == nil || pipe.Request == nil || len(pipe.Request.Body) == 0 {
        return payload, nil
    }
    // 子请求（ForwardSubRequest 强制打标）：跳过自身处理，防递归
    if pipe.Metadata != nil {
        if v, _ := pipe.Metadata["__sub_request"].(bool); v {
            return payload, nil
        }
    }
    // 只处理自己的 capability
    routes, err := s.repo.ListCapabilityRoutes(ctx) // 或 store JSON 兜底
    if err != nil { return payload, nil }          // fail-open
    scope := types.ChannelScopeFromMetadata(pipe.Metadata, s.requestChannelBaseURLs)
    matched := types.SelectCapabilityRoutes(routes, capabilityName, pipe.Request.Model, scope)
    if len(matched) == 0 { return payload, nil }
    route := matched[0]
    if route.Route != types.RouteProxy {
        return payload, nil // native / 历史 error：透传
    }
    // proxy：按 route.ViaOptions 加工 body ...
}
```

4. **注册 hook**：挂在 **`ProxyBeforeAttempt`**（每次渠道尝试前，渠道已写），不是 `ProxyBeforeUpstream`（入口渠道未定，渠道级路由匹配不到）。

```go
// plugins/vision_v2/plugin.go
ctx.On(modelgateway.ProxyBeforeAttempt, svc.HandleProxyBeforeUpstream)
```

> 历史教训：vision_v2 曾挂在 `ProxyBeforeUpstream`，导致 channel_base_urls 约束的路由永远匹配不上（scope 全空），workbuddy 原生透传失效。渠道匹配必须等渠道确定。

---

## 3. ForwardSubRequest：子请求通道

### 3.1 签名

```go
// plugins/model-gateway/proxy.go
func (s *Service) ForwardSubRequest(ctx context.Context, pipe *ProxyPipeline,
    streamWriter func(line []byte) error) (*ProxyPipeline, []byte, error)
```

- `pipe`：调用方构造的子请求（Request.Method/Path/Body/Model/Stream + Metadata）。
- `streamWriter`：非 nil = 流式（上游 SSE 逐行回调）；nil = 非流式（返回完整响应 body）。
- 返回：处理完的 pipe（**含最终渠道、`__request_log_attempt_id` 等 metadata**）+ 响应 body + error。

### 3.2 自动获得的能力

子请求走网关主链路，自动获得：

- **request-log 完整日志**（request-log 插件在 ProxyBeforeAttempt 钩子写独立 `request_logs` 表，UUID 写入 `pipe.Metadata[MetadataRequestLogAttemptID]`）
- **渠道 failover**（`__channel_candidates` 候选循环，失败自动换下一个）
- **额度统计**（volc-free-quota 走 ProxyUpstreamSucceeded）
- **安检**（sensitive/field-filter 照常，除非跳过——见 3.3）

### 3.3 强制标记（ForwardSubRequest 自动设置）

| metadata key | 值 | 作用 |
|---|---|---|
| `__sub_request` | `true` | 能力插件检测到后跳过自身处理（防递归）；同时 model-gateway 日志函数 `isSubRequest` 早退（不写顶级日志行） |
| `__sub_request_skip_security` | `true` | sensitive/field-filter 跳过安检（内部请求体可能是 base64 图片等，不能被替换/删字段） |
| `__parent_request_id` | 主请求 ID | 调用方构造 pipe 时写入，供 request-log / UI 关联主请求 |

调用方构造 pipe 时还应写：`__channel_candidates`（候选渠道 id 列表，视觉识别用）/ `__current_channel`（续流锁定主渠道用）、`__parent_request_id`（主请求 ID）。

### 3.4 日志归属（不产生顶级行）

`ForwardSubRequest` 强制 `__sub_request`，model-gateway 的 `proxyBeginLog` / `proxyFinishLog` / `proxyAttemptLog` / `proxyRejectedLog` / `proxyStreamAttempt` 全部 `isSubRequest` 早退——**子请求不创建顶级 `route_requests` 行、不写重复 attempt**。

调用方自己负责在主请求折叠下写子步骤 attempt（如视觉识别=1.1、续流=1.2），并回填 `request_log_id`：

```go
final, body, err := s.gw.ForwardSubRequest(ctx, pipe, subWriter)
reqLogID := ""
if final != nil {
    if v, _ := final.Metadata[modelgateway.MetadataRequestLogAttemptID].(string); v != "" {
        reqLogID = v
    }
}
// 把 reqLogID 填进自己写的 attempt（RouteAttempt.RequestLogID），前端"完整日志"按钮依赖它
```

> 常见遗漏：**失败路径也要回填 reqLogID**（request_logs 已有失败行，前端失败场景按钮不能丢）。`ForwardSubRequest` 返回 error 时 final 可能非 nil，先读 metadata 再判断 err。

### 3.5 流式 vs 非流式

- **流式**（streamWriter 非 nil）：网关逐行回调上游 SSE 行 → 调用方自行解析（如视觉识别解析 `choices[].delta.content` 输出到思考区 + 累积文本）。
- **非流式**（streamWriter nil）：返回完整响应 body，调用方解析 JSON。
- 内部实现：流式用自定义 `subRequestStreamWriter`（Write→逐行回调），非流式用 `httptest.ResponseRecorder`（Code≥400 转 error 返回）。

### 3.6 防递归（三个 hook 都要早退）

子请求走网关会再次触发所有能力插件 hook。**每个能力插件必须在自己的所有 hook 入口检测 `__sub_request` 早退**：

```go
// 请求侧
if pipe.Metadata != nil { if v, _ := pipe.Metadata["__sub_request"].(bool); v { return payload, nil } }
// 流式（HandleProxyStreamChunk）
if sp.Pipe.Metadata != nil { if v, _ := sp.Pipe.Metadata["__sub_request"].(bool); v { return sp, nil } }
// 非流式（HandleProxyAfterUpstream）
if ap.Pipe.Metadata != nil { if v, _ := ap.Pipe.Metadata["__sub_request"].(bool); v { return payload, nil } }
```

> 只拦请求侧不够——漏掉 StreamChunk/AfterUpstream 会导致子请求流被占位符过滤/工具解析双重处理。

### 3.7 调用示例（vision_v2）

**视觉识别**（候选渠道 + 流式思考区）：

```go
pipe := &modelgateway.ProxyPipeline{
    Request: &modelgateway.ProxyRequest{
        Method: "POST", Path: "chat/completions",
        Header: http.Header{"Content-Type": {"application/json"}},
        Body:   body, Model: viaModel, Stream: streamWriter != nil,
    },
    Metadata: map[string]any{},
}
// 不设 __current_channel（防锁死单渠道，via_options failover 失效）
if len(channelIDs) > 0 { pipe.Metadata["__channel_candidates"] = channelIDs }
if parentID != "" { pipe.Metadata["__parent_request_id"] = parentID }
final, _, err := s.gw.ForwardSubRequest(ctx, pipe, subWriter)
```

**续流**（锁定主渠道）：

```go
md := map[string]any{"__parent_request_id": pipe.RequestID}
// 复用主渠道：__current_channel / __current_channel_base_url / __last_tried_channel
sub := &modelgateway.ProxyPipeline{Request: ..., Metadata: md}
return s.gw.ForwardSubRequest(ctx, sub, streamWriter)
```

---

## 4. 新能力插件完整接入清单

- [ ] 定义 capability 名，写入能力路由表（capability_routes，SQLite + store JSON 兜底）
- [ ] 实现 `requestChannelBaseURLs(term) []string`（id 精确 + ChannelName 组匹配 + 禁用过滤）
- [ ] hook 里 `ChannelScopeFromMetadata` → `SelectCapabilityRoutes` 选路
- [ ] native / 无匹配 → 透传；proxy → 按 ViaOptions 加工
- [ ] 注册 `ProxyBeforeAttempt`（渠道已定再匹配），**不是** `ProxyBeforeUpstream`
- [ ] 若插件要发内部上游请求：构造 pipe 走 `ForwardSubRequest`（不 `http.Client.Do`）
- [ ] 三个 hook（请求/流式/非流式）入口 `__sub_request` 早退
- [ ] 子请求 attempt 挂主请求折叠下，回填 `__request_log_attempt_id`（成功 + 失败路径）
- [ ] 测试：路由匹配单测 + 子请求防递归测试 + skip_security 测试 + request_log_id 回填断言

---

## 5. 常见陷阱（踩过的坑）

| 坑 | 说明 |
|---|---|
| 挂在 ProxyBeforeUpstream | 渠道未定，channel_base_urls 路由永不命中 → 透传失效 |
| 内部请求 http.Client.Do | 绕过网关：无日志、无额度、无 failover（后门）|
| 防递归只拦请求侧 | StreamChunk/AfterUpstream 漏掉 → 子请求流双重处理 |
| 子请求不写 attempt 归属 | 顶级 route_requests 出现 sub-xxx 行污染列表（isSubRequest 早退解决）|
| 续流/识别 attempt 忘回填 reqLogID | 前端"完整日志"按钮缺失 |
| 视觉子请求过安检 | base64 图片被整体替换/删字段 → 图坏（`__sub_request_skip_security`）|
| 显式渠道但 Models 不匹配被丢 | `resolveChannels` 显式指定渠道（candidates/base_url/单 key）时保留进 unknown，Models 只约束自动路由 |
| 流式子请求用 ResponseRecorder | 被缓冲到结束 → "实时流"退化（流式必须走逐行回调）|
