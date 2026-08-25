# vision_v2 渠道匹配 + 视觉识别独立日志 修复计划

> **根因锁定**：loadout.db 真值 + 代码跟踪得出。两 bug 看似独立，实则共享 `vision_v2` 的 hook 时序问题。

## 真值（已查 loadout.db）

| 表 | 字段 | 值 |
|---|---|---|
| `channels` (workbuddy 15122841305) | id | `df3f297543aebb94` |
| | base_url | `https://copilot.tencent.com/v2` |
| `capability_routes` pos=0（用户配的"原生透传"） | models | `["*"]` |
| | channel_base_urls | `["https://copilot.tencent.com/v2"]` |
| | route | `native` |
| `capability_routes` pos=4（用户配的"附加代理"） | models | `["deepseek-*","hy*","glm-*"]` |
| | channel_ids / channel_base_urls | 都 null（全渠道） |
| | route | `proxy` |
| 截图请求 `c368bc9c7335233c` 最终 attempt | step=4 → ch=df3f297543aebb94 → success → 视觉识别 step=4.1 → 续流 step=4.2 |

## Bug 1：原生透传未生效

**根因**：`vision_v2.HandleProxyBeforeUpstream` 注册在 `ProxyBeforeUpstream` 钩子上（`plugins/vision_v2/plugin.go:43`）。该钩子在 `plugins/model-gateway/proxy.go:119` 触发——**早于 `proxyAttempt` 在 line 289/292 写 `metadata["__current_channel"]` / `["__current_channel_base_url"]` 的渠道尝试阶段**。

`vision_v2/routes.go:43` `DecideRouteScope` 调 `channelScopeFromMetadata(pipe.Metadata, s.requestChannelBaseURL)` → `types.ChannelScopeFromMetadata`。此时 metadata 只可能有 `__channel_hint`（v2 前缀模式），但 hint 不在 `ChannelScopeFromMetadata` 解析字段内 → `ChannelRequestScope{IDs: [], BaseURLs: []}` **全空**。

`MatchChannelScopeEx` 行为：
- pos=0（workbuddy native，channel_base_urls 非空、channel_ids 空）：req 空时跳到 `if len(req.IDs) == 0 && len(req.BaseURLs) == 0 { return false }` → **永远 false** ❌
- pos=4（附加代理，channel_ids 和 channel_base_urls 都空）：短路 `len(ids)==0 && len(burls)==0 → true` → **永远 true** ✅

视觉 v2 走代理逻辑，4.1 那条视觉识别 attempt 出现。

## Bug 2：视觉识别缺独立日志

**根因**：`vision_v2/describe.go:225` `callVision` 直接 `http.NewRequestWithContext → http.Client.Do` —— 完全不经过 model-gateway 主链路，所以 `routeLog.Start` 没被调用，`route_requests` 表**只有 1 条 hy3 主请求**。

视觉识别 attempt（4.1）只是 `visionAttempt` 写到 `route_attempts`（同 request_id）。视觉模型的 token、headers、完整 body 没被记。

## 修复方案

### Task 1: `types.ChannelScopeFromMetadata` 支持 `__channel_hint` 兜底

**Files**:
- Modify: `plugins/types/types.go`（`ChannelScopeFromMetadata`）
- Modify: `plugins/vision_v2/routes.go`（闭包改为 hint→base_urls 解析）
- Modify: `plugins/sensitive-filter/service.go`、`plugins/field-filter/service.go`、`plugins/request-log/service.go`（同样）
- Test: `plugins/types/types_test.go`（补 case：`hint=workbuddy` + 闭包反查 BaseURL → scope.BaseURLs 含 `https://copilot.tencent.com/v2`）

**改动点**：

`ChannelScopeFromMetadata` 增加 `__channel_hint` 兜底：IDs/BaseURLs 都没解出时，调闭包按 hint 反查该渠道组的所有 base_urls，**全部 append 到 scope.BaseURLs**（一个 ChannelName 通常只有一种 base_url，但防御多 Key 共用 base 的场景）。

```go
// 兜底：BeforeUpstream 阶段未写 __current_channel/__channel_candidates，
// 但 v2 hint 已写入（__channel_hint）。按 hint 反查渠道组补 base_urls。
if len(scope.IDs) == 0 && len(scope.BaseURLs) == 0 {
    if hint, ok := md["__channel_hint"].(string); ok && hint != "" && resolveBaseURL != nil {
        for _, bu := range resolveBaseURL(hint) {
            scope.BaseURLs = append(scope.BaseURLs, bu)
        }
    }
}
```

闭包签名（`resolveBaseURL`）从 `(string) string` 改成 `(string) []string`。vision_v2 `requestChannelBaseURL` 改写为：当 ID 命中返回 `[base_url]`，否则按 ChannelName 遍历 channels 返回所有匹配 base_urls。

**验证**：单测覆盖；真实请求：hy3 + v2 前缀 workbuddy 触发时 pos=0 命中 native、pos=4 不命中。

### Task 2: `vision_v2.callVision` 写独立 request 日志

**Files**:
- Modify: `plugins/vision_v2/describe.go`（callVision 前后 Start/Finish）
- Modify: `plugins/vision_v2/tool_loop.go`（executeToolLoop 同步改：视觉识别 attempt 仍写，但 route_request 行独立）
- Test: `plugins/vision_v2/describe_test.go` + `tool_loop_test.go`（mock routeLog.Start/Finish）

**改动点**：

`callVision` 在调模型前 `routeLog.Start(RequestID=vision_<新 ID>, RequestedModel=viaModel)`，调用 `routeLog.Attempt(ChannelID)`（逐步改 success/failed），调完后 `routeLog.Finish` 关闭。

视觉识别的新 `request_id` 由 vision_v2 自己生成（`crypto/rand` 16 字节 hex），与主请求 request_id **不同**。这样 `route_requests` 表会有 2 条：1 条 hy3 + 1 条 vision 识别（同时两条都有完整 attempt）。

UI 现状是 `step=4` 下嵌套 `4.1`/`4.2`，改为视觉识别成为 top-level request，不再嵌套。这个 UX 变动需前端配合：4.1 视觉识别作为独立 top-level request 显示。

**验收**：
- 同一次 hy3 请求后 route_requests 出现 2 条：主请求 hy3@workbuddy、视觉识别 @ volcengine
- 主请求 attempts 含 step=1..N、4.2（续流首次尝试）
- 视觉识别 request 含 1 条 attempt：model=doubao-seed-2-0-mini-260428, channel=ada333dbda7499c0, action=视觉识别

### Task 3: 真机回归

**验证步骤**：
1. 跑测试：`go test ./plugins/types/... ./plugins/vision_v2/...`
2. 重启 loadout，触发 hy3 + 图（v2 workbuddy 前缀）+ 双图（混合）
3. UI 看 route_log：应出现 2 条请求（hy3 主 + 视觉识别）
4. 切换 workbuddy 渠道为不透明 workbuddy 路线，**期望**：原生透传生效（4.1 视觉识别消失）

## 风险

- **闭包签名改动**：vision/sensitive/field/request-log 都改 resolveBaseURL 签名，全局 trace `requestChannelBaseURL` 一次。
- **UI 嵌套变动**：4.1/4.2 从 nested 改成 top-level，UI 需评估是否影响 RouteLogTable.vue 折叠分组。前端可能要做兼容（新结构：视觉识别自己一行，主请求的 attempts 里有 step=4.2 续流）。
- **并发**：vision_v2 改 Start 在并发请求下不冲突（每个生成自己的 crypto rand req id）。

## 优先级

P0：原生透传不生效——用户已经因为这个看到视觉插件误触发；正确性必须修。
P1：视觉识别缺独立日志——可观测性，影响调试。
