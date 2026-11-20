# AGENTS.md — 插件开发避坑红线（给 AI / 开发者的速查）

> 本文件是**踩坑红线清单**，来自真实事故（2026-10 的「免费额度误拦」「force-stream
> 对子请求不生效」「限流规则失效」等）。系统性的介绍看 `docs/guide/`（权威），
> 这里只列「写插件前必须知道、违反必出隐蔽事故」的约束。

## 子请求体系：两个标记，语义严格区分

插件内部调上游（视觉识别、MCP 图片识别等）必须走
`modelgateway.Service.ForwardSubRequest`（禁止自建 http.Client 直连）。这条通道
发出的请求带两个 metadata 标记，**语义完全不同，使用前先想清楚自己属于哪类**：

| 标记 | 语义 | 谁可以响应它 |
|---|---|---|
| `__sub_request` | 这是子请求，防止能力插件**递归**处理自己发出的请求 | 只有「改写请求会破坏子请求自身语义」的插件：vision_v2（防递归）、message-inject（注入会毁掉识别） |
| `__sub_request_skip_security` | 跳过**安检类**处理 | 只允许安检类插件：sensitive-filter、field-filter（识别体含数 MB base64，字符串替换会改坏图片） |

**红线**：新插件看到 `__sub_request_skip_security` 时，先问自己——我是安检吗？
不是就不要响应这个标记。force-stream 曾因误把它当「子请求都别碰我」，导致 MCP
图片识别的 force_stream 路由失效、非流式请求打到只支持流式的上游报 400（已修，
回归测试 `TestHandle_SubRequestHit`）。

volc-free-quota 对子请求**不豁免**（额度按账号归属，子请求也消耗真实额度），
防误拦靠「模型归属检查」——见下一条。

## 模糊匹配的误伤面：包名匹配 ≠ 渠道归属

涉及「按模型查账」的插件（额度、限流等），判定前必须先确认**模型确实属于这个
渠道**（用 `channel_models` 表核对），再看账本。账本里的记录名是模糊匹配
（`matchOne` 双向包含 + token 相似），「火山账本里有 glm-5-2 的空记录」不代表
「workbuddy 请求的 glm-5.2 归火山管」。volc-free-quota 曾因此把测 workbuddy 的
glm-5.2 误拦成「免费额度用完」（已修，`checkExhausted` 开头的
归属检查 + 回归测试 `TestHandleProxyBeforeAttemptIgnoresForeignModel`）。

## 额度类拦截：预测「这一次尝试」会失败，别拒绝整个请求

`ProxyBeforeUpstream` 只执行一次，返回 `error` 就等于**整个请求结束**：网关不会向上游
发任何请求，也不会 emit `ProxyUpstreamFailed`，aggregate 的轮询 / failover 永远不触发。
用户看到的就是「一句话报额度用完，其他模型一次都没试」。

额度插件（volc-free-quota）的定位是**单点失败预测**，语义等价于上游返回 429：

- 挂在 `ProxyBeforeAttempt`（每次渠道尝试前），只判定 `__current_channel` + 当前模型；
- 返回 `GatewayError{Retryable: true}`：core 把它当成这次尝试的失败，落一条 failed
  attempt（`failure_class=free_quota_exhausted`）后继续 failover / 切换聚合目标；
- 只有当所有候选都失败时，客户端才会看到 429；
- 其它模型、其它平台、同一账号的其它 Key 使用其它模型时都不受影响。

两条边界（容易被「顺手改成更聪明」而破坏）：

1. **预测只影响这一次尝试**：不要在预测路径里写 `model_states` 冷却。跨请求的冷却由
   请求成功后的余额扣减 + 定时刷新负责；在这里写会让一次预测把后续请求一起改道。
2. **钉定渠道（`X-Loadout-Pin-Channel`）跳过预测**：模型状态页逐 Key「测试」的语义是
   「真的打一次」，必须拿到上游真实结果（与 key-rate-limit 的 `isPinned` 同态度）。

判定入口统一走 core 的 `retryableReject`（等价 `errors.As` + `Retryable`）；日志分类统一走
`gatewayFailureClass`（优先 `FailureClass`，其次 `Type`）。新增这类拒绝时不要自己另写一套判断。

**红线**：能力插件不要用「整单拒绝」表达「某个渠道/模型这次会失败」。需要切换就标记
`Retryable`，让 core 走失败切换；内容违规这类与渠道无关的拒绝才用默认（不可重试）。
回归先例：`TestRetryableRejectFailsOverToNextChannel`、
`TestHandleProxyBeforeAttemptPredictsOnlyThisChannelModel`、
`TestAggregateFailoverWhenVolcQuotaExhausted`。

## 工具调用补全：能力路由开关，别在网关核心隐式改写

`plugins/toolcall-repair`（capability=`toolcall_repair`）负责清理空工具名、补全缺失的
tool 结果、合并相邻 assistant。**默认不生效**：只有能力路由里配了 proxy 才改写，
未配置时字节级透传；网关核心不再内置任何工具调用改写逻辑。

**红线**：任何「修复历史」的合成消息都必须带**真实工具名**。给孤立 tool 结果补声明时
写 `name:""` 会被上游直接 400（step-5-preview：code 11133 input_invalid）。借不到
真实名字时宁可丢掉那条孤立结果，也不要造空名声明。回归先例：79ed5ee492c3d825——
原样请求 200，网关补出空名后 400。

## 渠道上下文的时序：你要的信息在哪个阶段才存在

| 阶段 | 管线 metadata 里有什么 |
|---|---|
| `ProxyBeforeUpstream`（入口一次） | 什么渠道信息都没有 |
| `ProxyChannelsResolved`（候选解析完） | 候选渠道列表（在 payload 里，不在 metadata） |
| `ProxyBeforeAttempt`（每次尝试前） | `__current_channel` / `__current_channel_base_url` 已写入 |

**红线**：在 BeforeUpstream / ChannelsResolved 阶段做「按渠道约束」的路由匹配时，
不能只读 metadata（那时是空的）。ChannelsResolved 阶段把 payload 里的候选渠道
转成 scope 参与匹配（参考 key-rate-limit 的 `scopeFromCandidates`）。key-rate-limit
曾因只读 metadata 导致「渠道=workbuddy」的限流规则整体失效（已修）。

若请求可能带 `X-Loadout-Pin-Channel`（模型状态页逐 Key 测试），渠道判定前先看
这个标记——钉定的渠道优先于一切兜底推断（参考 volc-free-quota 的
`HandleProxyBeforeAttempt`：它直接读 `__current_channel`，钉定的渠道在尝试前已写入）。

## 日志写入：一律用与取消解耦的 ctx

`routeLog.Attempt / Finish` 等写库调用，ctx 必须是
`context.WithoutCancel(r.Context())`。子请求的调用方 ctx 在失败/超时后已取消，
直接用 `r.Context()` 会静默写丢（SQLite ExecContext 不报错给上层），表现为日志
详情凭空缺失。model-gateway 的 `proxyAttemptLog` 曾因此丢光子请求的逐渠道尝试
记录（已修）。新增日志点时照抄既有写法，不要自己用裸 ctx。

## 计数与清扫：窗口口径必须一致

滑动窗口计数器的后台清扫口径必须 ≥ 所有生效规则里最长的窗口，否则长窗口规则
（如每 24 小时 N 次）的计数会在清扫时被清零，限流静默失效。key-rate-limit 的
做法：规则生效时记下窗口长度（`noteWindow`），清扫取 `max(下限, 最长窗口)`
（`sweepIdle`）。新增计数类功能时照此办理，清扫口径不许写死常量。

## 测试时间断言：统一 UTC 口径

涉及「今天/昨天」的统计测试，期望日期与请求参数必须同口径。归档桶按 UTC 日界
聚合，若断言用本地日、请求不带 tz，北京 0:00–8:00 之间（本地今天 = UTC 昨天）
测试必挂。参考 `TestStatsModelsMergeArchivedOnClear`：期望日期用
`time.Now().UTC()`，请求带 `tz=UTC`。

## 好习惯：给插件里的每道闸门写态度

插件 handler 开头若有「某种请求跳过我」的逻辑（子请求、pin 钉定、native 豁免），
用注释写清楚：哪些标记我会响应、为什么、谁负责防误伤。volc-free-quota 的
`HandleProxyBeforeAttempt`（子请求态度 + 只判当前渠道）的注释是范本。审阅者
核对子请求体系时，就是靠这些声明逐个确认的——没有声明，下一个改代码的人只能猜。
