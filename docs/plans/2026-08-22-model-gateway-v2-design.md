# model-gateway v2 接口设计

> 状态：设计稿（2026-08-22，头脑风暴产出 + 子代理审核修正）
> 目标：新增 /v2 路由族，渠道模型以 `{渠道名}/{模型名}`（ChannelName 组级）命名，客户端可显式选择渠道；v1 完全不动。

---

## 1. 需求（已与用户对齐）

| 决策点 | 结论 |
|---|---|
| v1 是否改动 | **完全不动**。v2 是新增平行路由族 |
| 新增路由 | `GET /v2/models` + `/{Method} /v2/{path...}` |
| 渠道名语义 | **ChannelName（base_url 组级）**，非单 Key 的 Name；同组多 Key 共享同一前缀 |
| model 无前缀 | 完全走 v1 逻辑（普通模型、虚拟模型均如此） |
| model 有前缀 | 锁定该渠道组，其余（转发、hook、failover、日志）复用 v1 |
| /v2/models 输出 | 渠道模型输出 `{ChannelName}/{model}`；虚拟模型原名（不加前缀） |

---

## 2. 核心语义

### 2.1 前缀判定与拆分（最长 ChannelName 前缀匹配）

**不用「按第一个 `/` 切再查表」**——那会误拆 `meta-llama/llama-3.1-8b`，也无法处理 ChannelName 本身含 `/` 的情况。改用：**遍历启用渠道的 ChannelName，看 model 是否以 `{ChannelName}/` 为前缀，取最长命中**。

```
model = "newapi/gpt-4o"
  → ChannelName "newapi" 是前缀 → hint="newapi", realModel="gpt-4o"

model = "team/workbuddy/gpt-4o"（ChannelName 本身是 "team/workbuddy"）
  → 最长前缀 "team/workbuddy" 命中 → hint="team/workbuddy", realModel="gpt-4o"

model = "meta-llama/llama-3.1-8b"（没有叫 meta-llama 的渠道）
  → 无 ChannelName 命中 → 不拆，整体走 v1

model = "gpt-4o"
  → 无 / 前缀 → 走 v1
```

- 最长匹配解决前缀包含问题（如渠道 "a" 和 "ab" 并存时，`ab/xxx` 应命中 "ab" 而非 "a"）。
- **ChannelName 为空或纯空白 → 该渠道不参与前缀匹配**（避免输出 `/gpt-4o` 这种脏名）。

### 2.2 路由差异

- v1：候选 = 所有启用渠道中「Models 含 model」的（未知渠道放最后，failover 换 Key/渠道）
- v2（有前缀）：候选 = **仅 ChannelName == hint 的启用渠道**（组内多 Key 全部纳入），再按 realModel 做模型支持匹配 + 健康检查 + failover

### 2.3 转发 model 改写（body + query 两条路径都要改）

客户端带前缀的 `model` 不能原样透传上游（上游不认识），命中前缀时改写为 `realModel`：

1. **body 路径**：`rewriteModelField(body, hint+"/"+realModel, realModel)`——精确定位顶层 `model` 字段值并做字节替换。
2. **query 路径**：`sniffRequest` 在 body 非 JSON 时从 `?model=` 取 model。此时改写 `pipe.Request.Query`（重建 query 的 `model` 参数），而非 body。
3. 无前缀时两条路径都零改动（纯字节透传，与 v1 完全一致）。

### 2.4 rewriteModelField 的保真策略（明确取舍）

`stripCopilotClientMetadata` 那种 `map[string]json.RawMessage` + `json.Marshal` 会重排顶层 key、丢空白/BOM，**不能直接复用**。改用：

- 首选：`json.Decoder` 的 `InputOffset()` 定位顶层 `model` 值的起止偏移，`body[start:end]` 字节替换——**不动其余任何字节**。
- 兜底：定位失败（BOM/前导空白导致 token 偏移异常）时，先 `bytes.TrimLeft` 去掉 BOM 与前导空白判断 `{`，再走 `map[string]json.RawMessage` 改写；仍失败则放弃改写、原样透传（让上游报错，不静默坏数据）。
- 说明：`json.RawMessage` 保留原始字节，**不解析数值，不会丢大数精度**（子代理担心的精度问题不成立）；真正的代价是 key 顺序与空白，仅发生在「带前缀」请求，无前缀请求仍是逐字节透传。

---

## 3. 实现方案

### 3.1 路由注册（plugin.go）

```go
ctx.RegisterRoute(plugin.RouteSpec{Method: "GET", Pattern: "/v2/models", Auth: plugin.AuthSkKey, Handler: http.HandlerFunc(svc.HandleModelsV2)})
ctx.RegisterRoute(plugin.RouteSpec{Method: "", Pattern: "/v2/{path...}", Auth: plugin.AuthSkKey, Handler: http.HandlerFunc(svc.HandleProxyV2)})
```

### 3.2 抽离：proxyHandle(w, r, version)

`HandleProxy` 与 `HandleProxyV2` 抽成薄包装，核心管线收敛到一个函数，差异点用 `version` 隔离：

```go
func (s *Service) HandleProxy(w, r)  { s.proxyHandle(w, r, "v1") }
func (s *Service) HandleProxyV2(w, r) { s.proxyHandle(w, r, "v2") }
```

`proxyHandle` 内三个差异点（**version=="v1" 时必须完全走原路径**）：

1. `subPath = TrimPrefix(r.URL.Path, "/v"+version+"/")`，保留现有防御逻辑。
2. 仅 v2：sniff 出 model 后 `splitV2Model(model, s.isChannelName)` → 命中则 `Request.Model = realModel`、写 `pipe.Metadata["__channel_hint"] = hint`。
3. 仅 v2 且 hint 命中：转发前改写 body/query 的 model。

### 3.3 splitV2Model 签名（非纯函数，注入渠道名判断）

```go
// splitV2Model 按最长 ChannelName 前缀拆分 model；ok=false 表示不拆。
// isChannel 由调用方注入（复用一次渠道表扫描的结果），保持函数可单测。
func splitV2Model(model string, isChannel func(string) bool) (hint, realModel string, ok bool)
```

- `isChannel` 的实现在 Service 层：一次 `ListChannels` 后建 `map[ChannelName]bool`（只收启用、ChannelName 非空者）。
- 每次请求多一次渠道表扫描，成本可接受（与现有 resolveChannels 同量级）。

### 3.4 hint 生命周期（关键，防丢失/防残留）

`__channel_hint` 存 metadata（与 `__current_channel` 约定一致），但必须显式管理：

1. **hook 重建 pipe 会清 metadata**：`HandleProxy` 在 `Waterfall(ProxyBeforeUpstream)` 后 `pipe = rewritten`（proxy.go:88-94）。因此在 hook 之前把 hint 抽到**局部变量**，hook 之后无条件写回 `rewritten.Metadata["__channel_hint"]`（若 hint 非空）。这样插件重建 pipe 也不丢。
2. **aggregate failover 递归会残留 hint**：`tryProxyAggregateFailover` 切换目标后，新目标不应被旧 hint 锁死。在 `proxyForward` 递归入口（或 failover 成功后）`delete(pipe.Metadata, "__channel_hint")`——聚合模型是虚拟语义，不受前缀锁定。
3. 递归 `proxyForward` 时**不重复 split**，只读 metadata 里的 hint。

### 3.5 resolveProxyChannels 增强（hint 过滤加在早退之前）

`resolveProxyChannels`（proxy.go:433）当前 `model==""` 时直接早退 `allEnabledChannels`。hint 过滤**必须加在这个早退判断之前**，否则「有 hint 但 model 为空」会绕过锁定：

```go
func (s *Service) resolveProxyChannels(ctx, model, pipe) []ResolvedChannel {
    if hint, _ := pipe.Metadata["__channel_hint"].(string); hint != "" {
        // 先按 ChannelName 过滤，再走原有 model 匹配 / allEnabledChannels 逻辑
        return s.resolveChannelsByHint(ctx, model, hint, pipe.Metadata)
    }
    // ... 原逻辑不变
}
```

### 3.6 ResolvedChannel 加 ChannelName 字段（4 处填充点，全部补齐）

`ResolvedChannel` 新增 `ChannelName string`，以下 **4 个构造点全部补上**（漏一处 hint 就匹配不上）：

| # | 位置 | 函数 |
|---|---|---|
| 1 | proxy.go:461 | `allEnabledChannels` SQLite 分支 |
| 2 | proxy.go:478 | `allEnabledChannels` JSON 分支 |
| 3 | service.go:350 | `resolveChannels` |
| 4 | service.go:128 | `ResolveChannelsForModel` |

### 3.7 HandleModelsV2（service.go 新增）

收集逻辑抽 `collectChannelModels(ctx) []channelModelEntry`（含 ChannelID/ChannelName/Model/Virtual），两个 Handler 共用；**输出层各自写，不硬合成一个带 version 参数的函数**。

HandleModelsV2 差异：

1. **输出 id**：渠道模型 `{ChannelName}/{model}`；虚拟模型原名。
2. **去重维度**：按 `{ChannelName}/{model}` 去重，同名模型跨渠道**全部保留**。
3. **可用性聚合到 ChannelName 级**（非天然满足）：v1 的可用性判断是 Key 级 `disabled[channelID+"\x00"+model]`（service.go:206-213）。v2 需建 `ChannelID→ChannelName` 映射，改为「**该 ChannelName 组下至少一个 Key 的该模型可用**才保留」。同组多 Key 同模型只输出一次。
4. **白名单双形态命中**：校验时 `AllowedModel(key.Models, realModel) || AllowedModel(key.Models, hint+"/"+realModel)` 任一命中即放行——用户白名单存裸名或带前缀名都能匹配。**（待确认：用户白名单实际存哪种形态；默认双形态兼容，无需二选一。）**

---

## 4. 边界与错误

| 场景 | 行为 |
|---|---|
| `newapi/gpt-4o`，newapi 渠道被禁用/不存在 | 400 `no_available_channel` |
| `newapi/gpt-4o`，渠道存在但组内无 Key 支持 gpt-4o | 400 `no_available_channel` |
| 组内多 Key 支持 | 按渠道表顺序 failover（v1 既有逻辑） |
| 无前缀 | 与 v1 完全一致（含虚拟模型） |
| 模型名自带 `/`（如 meta-llama/llama-3.1-8b） | 无 ChannelName 前缀命中 → 不拆，走 v1 |
| ChannelName 为空/纯空白 | 不参与前缀匹配，该渠道模型在 /v2/models 里输出裸名或跳过（不输出 `/gpt-4o`） |
| ChannelName 含 `/`（如 team/workbuddy） | 最长前缀匹配天然支持，正确拆分 |
| /v2/models 虚拟模型 | 原名返回，不加前缀 |
| aggregate 模型切换目标 | hint 在切换后清除，不被旧渠道锁死 |

---

## 5. 测试计划

| 层 | 用例 |
|---|---|
| splitV2Model | 注入 mock isChannel：`("newapi/gpt-4o")`→(newapi,gpt-4o,true)；`("gpt-4o")`→ok=false；`("meta-llama/llama-3.1-8b")` 无该渠道→ok=false；`("ab/xxx")` 且渠道 "a"+"ab" 并存→命中 "ab"；ChannelName 空→不参与 |
| rewriteModelField | body 含 `"model":"newapi/gpt-4o"` → 仅该值被替换为 `gpt-4o`，其余字节（缩进/顺序/大数）不变；BOM/前导空白 → 兜底路径正确 |
| HandleModelsV2 | 输出含 `newapi/gpt-4o`；同模型跨渠道全保留；虚拟模型原名；同组多 Key 可用性聚合正确；白名单裸名/带前缀名均命中 |
| HandleProxyV2 | 前缀路由锁定渠道组；body+query 两条路径 model 均改写；无前缀请求与 v1 字节级一致；hook 重建 pipe 后 hint 不丢；aggregate 切换后 hint 被清除 |
| 回归 | v1 全部现有测试通过（`go test ./plugins/model-gateway/...`）——这是 v1 零回归的唯一硬保证，不能靠口头 |

---

## 6. 已识别风险清单（子代理审核，全部并入上文）

| # | 风险 | 修正 |
|---|---|---|
| 1 | hint 被 hook 重建 pipe 清掉 / 被 aggregate 递归残留 | §3.4 局部变量桥接 + 切换后 delete |
| 2 | query 参数 model 不被改写 | §2.3 改写 query 路径 |
| 3 | ResolvedChannel 填充点实为 4 处 | §3.6 逐一列出 |
| 4 | splitV2Model 非纯函数，依赖渠道表 | §3.3 注入 isChannel |
| 5 | rewriteModelField 字节级透传不成立 | §2.4 InputOffset 定位替换 + 兜底 |
| 6 | model=="" 早退绕过 hint 过滤 | §3.5 过滤加在早退前 |
| 7 | ChannelName 空/含 `/` 边界 | §2.1 最长前缀匹配 + 空值排除 |
| 8 | HandleModelsV2 同组多 Key 去重非天然 | §3.7 可用性聚合到 ChannelName 级 |
| 9 | 白名单存裸名还是带前缀名未定死 | §3.7 双形态兼容（默认） |
| 10 | 抽离后 v1 零回归只能靠测试兜 | §5 回归用例 + 三个差异点 version 隔离 |
