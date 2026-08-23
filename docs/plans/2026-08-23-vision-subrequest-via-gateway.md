# vision_v2 子请求走 model-gateway 主链路（全走 A，沉淀通用通道）

> 用户决策：视觉识别（callVision）与续流（doUpstream）两个内部请求**全走 model-gateway 主链路**，统一收口网关能力（request-log / 安检 / 额度 / failover），日后类似需求也走此通道。不写代码前先定计划。
> 已过两轮 sub-agent 审计（2026-08-23）：第一轮 P0×2+P1×5，第二轮复审 P1×3 修订，见下。

## 审计结论汇总（两轮，修订版）

### 第一轮（v1 硬伤，已闭环）
- **P0-1 流式 vs ResponseRecorder**：修订为自定义 ResponseWriter（Write→逐行回调 + Flush no-op），recorder 仅非流式。✅ proxyStream 对 http.Flusher nil 安全断言（proxy.go:637/710），writer 只需 Write/Header/WriteHeader，Flush 应 no-op
- **P0-2 子请求 RequestID/step 污染**：独立 `sub-` RequestID，step 命名空间天然隔离。✅ proxyAttemptLog 写 pipe.RequestID=sub-，(request_id,step) 隔离，request-log COALESCE 首写落在子请求行
- **P1-3 metadata 浅拷贝泄漏**：子 pipe 深拷贝（见 P1-3-rev 修订）
- **P1-4 安检副作用**：识别 body 含数 MB base64，sensitive 反复整体替换=灾难、base64 误命中改坏图。修订：视觉子请求跳过安检（`__sub_request_skip_security=true`，sensitive/field 检测到透传）；request-log/额度照常
- **P1-5 request_log_id 语义错位**：见 P1-5-rev
- **P1-6 三 hook 防递归**：BeforeUpstream（实为 BeforeAttempt）+ StreamChunk + AfterUpstream 都检测 `__sub_request` 早退。✅ 必需——否则视觉子请求 body 被占位符改写+工具注入毁掉图片
- **P1-7 ctx/取消**：子请求 WithContext(主请求 ctx)，断连同步取消。✅ 三类日志写库已 WithoutCancel

### 第二轮（复审修订，必修）
- **P1-5-rev ForwardSubRequest 必须返回最终 pipe**：入口深拷贝 metadata → request-log 的 attemptMetadataKey 写进副本，调用方读自己传入的 pipe 得 nil。**签名改为 `ForwardSubRequest(ctx, pipe, streamWriter) (*ProxyPipeline, []byte, error)`**，vision 从返回值读 `__request_log_attempt_id` 回填视觉 attempt。failover 成功后该值=最后 attempt=成功候选，语义 OK
- **P1-3-rev 识别子请求必须新建 pipe，不复用主渠道 metadata**：深拷贝会带上主 pipe 的 `__current_channel`，proxyForward 第三层过滤（proxy.go:558）把视觉候选锁死到主渠道，via_options failover 静默失效。**仅续流允许复用渠道 metadata**（显式约定 + 回归测试）。识别子 pipe：只带 `__channel_candidates`/`__current_channel_base_url`（来自 via_options 展开），**不设 `__current_channel`**
- **P1-8 via_options 跨模型 failover 约束**：网关候选循环固定 model 换 channel，原 describeWithFailover 可 per-option 换 viaModel。若 viaModel 不同，候选 B 会用 A 的 model。**声明约束：同一路由的 via_options 必须同 viaModel（仅渠道不同）**；跨模型由多条路由表达（每条路由=一个模型）。实现时加校验：via_options 出现不同 viaModel → 外层 per-option 循环（每个 option 一次 ForwardSubRequest），内层走网关换渠道

## 现状问题

| 内部请求 | 现状 | 缺失能力 |
|---|---|---|
| 视觉识别 callVision | `http.Client.Do` 直连视觉渠道 | request-log ❌ / 安检 ❌ / 额度 ❌ / failover 自手搓 |
| 续流 doUpstream | `http.Client.Do` 直连主渠道 | request-log ❌ / 安检 ❌ / 额度 ❌ |

根因：vision_v2 在网关旁开了"后门"（绕过 ProxyBeforeAttempt），网关统一能力全部失效。

## 目标架构

```
vision_v2 工具循环
  ├── 视觉识别：新建 pipe（独立 sub-RequestID, model=viaModel, candidates=via_options 展开, 无 __current_channel）
  │       └── modelgateway.ForwardSubRequest(ctx, pipe, streamWriter) → (finalPipe, body, err)
  │               ├── ProxyBeforeAttempt → request-log 记录 ✅（安检跳过 ✅）
  │               ├── candidates failover ✅
  │               └── 返回最终 pipe（读 __request_log_attempt_id 回填）
  └── 续流：复用原渠道 metadata（独立 sub-RequestID, body=带工具结果）
          └── modelgateway.ForwardSubRequest(ctx, pipe, streamWriter) → (finalPipe, body, err)
                  └── 同上全能力
```

## 关键设计

### 1. model-gateway 导出子请求入口

**Files**: `plugins/model-gateway/service.go` / `proxy.go`

```go
// ForwardSubRequest 供内部插件（vision_v2 等）发起"子请求"：走主链路完整管线
// （ProxyBeforeUpstream → 渠道解析 → 每 attempt ProxyBeforeAttempt → failover），
// 返回 (响应体, 最终渠道ID, error)。非流式场景专用（视觉识别/续流当前非流式）。
// 内部请求标记 __sub_request=true 防递归。
func (s *Service) ForwardSubRequest(ctx context.Context, pipe *ProxyPipeline) ([]byte, string, error)
```

- 内部实现：复用 `proxyHandle` 的核心（sniff → BeforeUpstream → proxyForward），但**不写 ResponseWriter**，而是用一个 buffer 捕获响应（子请求无客户端连接）
- 简化：构造内部 `httptest.ResponseRecorder` 包住，复用现有 `proxyForward(w, r, pipe, model, started)` 全流程
- 需要处理：子请求的 `r *http.Request`（用 `httptest.NewRequest` 构造），`pipe.HTTPRequest` 指向真实 ctx

### 2. 防递归标记

- `ForwardSubRequest` 入口强制 `pipe.Metadata["__sub_request"] = true`
- vision_v2 `HandleProxyBeforeUpstream` 开头：`if pipe.Metadata["__sub_request"] == true { return payload, nil }`（视觉识别调视觉模型时，若视觉模型本身配置了 vision 路由，不二次触发）
- sensitive/field/request-log 不受影响（它们对子请求照常工作，这正是我们要的）

### 3. vision_v2 改造

**Files**: `plugins/vision_v2/describe.go`（callVision）、`plugins/vision_v2/tool_loop.go`（doUpstream）

**callVision → 走子请求通道**：
- 输入：dataURI + prompt + via_options 候选（路由）
- 构造 body：`{"model": viaModel, "messages":[{image_url+text}], "stream": false}`（与现 payload 一致）
- 构造 pipe：**新建**（不深拷贝主 pipe）`Request{Method:POST, Path:"chat/completions", Body, Model:viaModel, Stream:false}`；`Metadata` 只带 `__channel_candidates`（via_options 展开的候选 key ids）/ `__current_channel_base_url`，**不设 `__current_channel`**（否则 proxyForward 第三层过滤锁死到主渠道，via_options failover 静默失效——P1-3-rev）
- 调 `modelgateway.ForwardSubRequest(ctx, pipe, streamWriter)` → 返回 `(finalPipe, body, err)`，从 `finalPipe.Metadata["__request_log_attempt_id"]` 读 UUID 回填视觉 attempt
- **describeWithFailover 的候选循环删除**：failover 交给网关 candidates 循环（候选渠道失败自动换下一个）
- **describeWithFailover 缓存命中分支保留**（缓存不进网关）
- **via_options 跨模型约束（P1-8）**：同路由 via_options 若出现不同 viaModel → 外层 per-option 循环（每个 option 一次 ForwardSubRequest），内层走网关换渠道；同 viaModel 则一次性展开 candidates

**doUpstream → 走子请求通道**：
- 输入：pipe（原主请求）+ 续流 body（带工具结果）
- 构造子 pipe：**仅续流允许复用渠道 metadata**（`__current_channel` / `__current_channel_base_url` 从主 pipe 拷贝），body = 续流 body，独立 sub-RequestID
- 调 `modelgateway.ForwardSubRequest(ctx, pipe, streamWriter)`
- 返回值：非流式续流直接拿完整 body；流式续流走流式回调（见设计 4）

### 4. 流式子请求

续流当前可能流式（主请求 stream=true 时）。`ForwardSubRequest` 非流式签名不够：
- 方案：`ForwardSubRequest(ctx, pipe, streamWriter func(line []byte) error) (*ProxyPipeline, []byte, error)`——流式子请求时把上游 SSE 行逐行喂给回调（vision_v2 的 executeToolLoop 已有转发逻辑 `fmt.Fprint(pipe.ResponseWriter, line)`，改为喂回调）；nil = 非流式返回完整 body
- 或：子请求一律非流式（视觉识别 + 续流都改非流式，最终响应在工具循环里组装）——**但用户明确要视觉识别流输出到客户端**，所以视觉识别必须流式
- 结论：保留流式回调，签名含 streamWriter

### 5. 视觉识别流输出保持（用户需求：思考区）

- 视觉识别子请求走网关时，网关的 `ProxyBeforeAttempt` 等不输出流；视觉识别的 reasoning delta 仍由 vision_v2 的 `toolStreamWriter` 写客户端（sseDelta → reasoning_content）
- 实现：`ForwardSubRequest` 的 streamWriter 回调内部接 `callVision` 的现有 `readVisionStream` 解析 → `toolStreamWriter` 逐 delta 输出（保持现状行为）
- 注意：流式子请求的 SSE 解析（choices[].delta.content）与网关流式转发是两个层次，需在 vision_v2 侧解析（readVisionStream 保留）；回调内须 `acc.Feed` 检测续流 newCalls（P2 补测）

### 6. request-log / 安检 / 额度自动生效

- 子请求走 `ProxyBeforeAttempt` → request-log 的 `HandleBeforeAttempt` 命中 request_log 能力路由（按子请求的 model=视觉模型）→ 生成 UUID + 写 request_logs
- 视觉识别 attempt（route_attempts）的 `request_log_id`：vision_v2 在 `ForwardSubRequest` **返回的 finalPipe.Metadata** 读 `__request_log_attempt_id`（request-log 覆写的 key；failover 成功后=最后 attempt=成功候选）回填到视觉识别 attempt（P1-5-rev）
- 额度（volc-free-quota）：子请求走 `ProxyUpstreamSucceeded` → 自动统计 ✅
- 安检：视觉子请求跳过 sensitive/field-filter（`__sub_request_skip_security=true`）；**field-filter 的 HandleProxyAfterUpstream（响应方向）也要检测 skip**（P2）

### 7. failover 语义迁移（重要）

现状 `describeWithFailover` 手动循环候选渠道。迁移后：
- via_options 展开为 candidates（`__channel_candidates` + `__current_channel_base_url`）交给网关
- 网关 candidates 循环失败换下一个（现成逻辑）
- **失败日志**：网关的 proxyAttemptLog 会对每个失败候选写 attempt（Action=切换渠道），用户能看到视觉识别候选的切换过程（顺带解决"failover 不可见"问题）
- describeWithFailover 删除，缓存命中分支移入 callVision 子请求前

### 8. 工具循环编排适配

`executeToolLoop` / `toolLoopNonStream`：
- 视觉识别：`describeWithFailover` → `callVisionSubRequest`（内部走 ForwardSubRequest）
- 续流：`doUpstream` → `ForwardSubRequest`（复用原渠道 metadata）
- 流式/非流式分支保持（流式：streamWriter 回调 → 客户端；非流式：拿完整 body）

## 风险

- **递归**：视觉模型自身配置 vision 路由 → 子请求标记防递归；测试必须覆盖
- **failover 语义**：via_options 展开 → candidates 的映射准确性（ChannelBaseURL > ChannelIDs > ChannelID 粒度已有 ExpandCandidateKeys 可复用）
- **流式子请求**：网关流式转发与 vision_v2 的 SSE 解析两层，小心别重复解析/双重转发
- **子请求的 r *http.Request**：httptest.NewRequest 构造，注意 ctx 传递（HTTPRequest.Context 用于取消）
- **回归**：工具循环测试（tool_loop_test/after_test）大量 mock 直连，需适配 ForwardSubRequest

## 实施顺序

1. model-gateway：`ForwardSubRequest(ctx, pipe, streamWriter) (*ProxyPipeline, []byte, error)` 导出 + `__sub_request`/`__sub_request_skip_security` 标记 + 流式回调支持 + 独立 RequestID + ctx 继承 + 单测（**request-log 回填依赖返回 pipe，接口签名在设计第 1 步就定死**）
2. vision_v2：三个 hook（BeforeAttempt/StreamChunk/AfterUpstream）防递归早退 + callVision/doUpstream 改造（识别新建 pipe 不带渠道、续流复用渠道）+ describeWithFailover 迁移（缓存分支保留）
3. executeToolLoop / toolLoopNonStream 编排适配（回调内 acc.Feed 检测续流 newCalls）
4. request-log 回填 `request_log_id`（从 finalPipe 读）+ 测试
5. 测试：递归防死循环、failover 迁移（深拷贝隔离 + `__current_channel` 锁定回归）、流式输出保持、request-log 自动记录、多候选失败取最后 UUID
6. 真机验证：hy3 + 图 → 视觉识别流输出到思考区 + 视觉识别/续流完整日志 + failover 切换可见
5. 测试：递归防死循环、failover 迁移、流式输出保持、request-log 自动记录
6. 真机验证：hy3 + 图 → 视觉识别流输出到思考区 + 视觉识别/续流完整日志 + failover 切换可见

## 验收标准

- 视觉识别 + 续流在 route_requests/route_attempts 有完整记录，request_log_id 非空
- 视觉识别流（reasoning_content）实时到客户端（保持现状）
- 视觉候选渠道失败自动切换，日志可见切换过程
- 视觉模型配置 vision 路由时不递归
- 额度统计对视觉子请求生效
