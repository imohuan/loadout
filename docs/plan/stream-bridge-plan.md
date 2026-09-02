# 流桥接能力实施计划（修订版·源码核实后）

> 状态：已实现（feat: force_stream，commit 39db705 / 76e3f4c / d883550）
> 目标：新增一个能力，让「客户端发非流式请求（stream:false/省略）、但渠道/平台只支持（或只接受）流式」的渠道+模型组合可用：网关以上游流式请求 → 缓冲整段 SSE → 还原成一份完整非流式 OpenAI JSON → 一次性写回客户端。
>
> 本文档所有「现状」均逐行核对过源码（proxy.go / service.go / types.go / plugins/types/types.go / sensitive-filter / field-filter / message-inject / request-log / admin-api / frontend CapabilityRouteEditor.vue），不再依据原计划的二手说法。

---

## 0. 一句话结论（先给评审拍板用）

- **计划 §2.1 的架构判断正确**：客户端「非流式」与「上游强制流式」目前共用 `pipe.Request.Stream` 一个变量，必须拆开；`proxyStream` 一旦开始写客户端，插件就拿不到、也不该碰 `w`。
- **计划 §3 的「纯插件做不到」结论正确，但方案归属判断要修正**：`proxyStream` 从第一行就开始 `w.WriteHeader`+逐行 flush，缓冲必须在 model-gateway 核心新增路径；但能力「命中判断」应放进独立插件（对标其它能力），核心只认一个 metadata 标记、完全不感知能力名——**推荐方案 B，而非原计划的 A**。理由见 §4。
- **能力名沿用蛇形常量惯例**（`sensitive_filter/field_filter/message_inject/request_log`）：推荐 token 用 `force_stream`；首版**只做 `chat/completions`**（OpenAI SSE 协议），正确、支持充分。
- **风险项与拼包算法是真正的难点**：完整「SSE delta → 整包非流式 JSON」还原项目里**不存在**，需新写；但可参考 `translate/service.go`（content 累积）与 `vision_v2/stream.go`（OpenAI/Claude/responses 三套 chunk 结构）。

---

## 1. 需求理解（对齐，避免做错方向）

触发：某「渠道 + 模型」组合命中 `capability_routes` 里的一条 `capability=force_stream`（`route=proxy`）路由。
网关做：
1. 识别命中的渠道+模型（每次渠道尝试都按当前渠道上下文重新匹配——与 sensitive-filter 一致）；
2. 把**转发给上游**的请求体里 `stream` 改成 `true`（**注意：只改请求体 JSON，不改 `pipe.Request.Stream`**）；
3. 读完整段上游 SSE 流；
4. 等 `[DONE]` 后把所有 delta 拼成一份完整非流式 OpenAI JSON（`content`/`reasoning_content`/`tool_calls`/`usage`/`finish_reason`/`id`/`object`/`created` 齐备）；
5. 一次性写回客户端（`Content-Type: application/json`，非流式）。

边界确认（无歧义项）：
- 客户端本来 `stream:true` → 不受本能力影响，照常走 `proxyStream` SSE 透传。
- 命中但客户端是其它 path（非 chat/completions）→ 原样透传（不误伤），首版只对 `chat/completions` 生效。
- 缓冲途中上游中断/出错 → 走 OpenAI 错误语义，不吐半包。
- **failover 语义不变**：缓冲失败按该渠道 attempt 失败处理，可触发聚合切换下一目标。

---

## 2. 现状梳理（逐行核实的准确结论，含对原计划的修正）

### 2.1 透明代理管线与插入点（`plugins/model-gateway/proxy.go`）

管线：`proxyHandle → proxy:before-upstream → proxyForward →（每渠道）proxyAttempt`。

**proxyAttempt 结构（核实）**：
- 先把 `__current_channel`/`__current_channel_base_url` 写进 metadata（L301-304）；
- `proxy:before-attempt` Waterfall 安检（L314-324）——能力插件在此改 Body/打标记，改动在 L326 读 body 之前生效；
- 构造上游 URL 与 headers（L326-364）；`client := &http.Client{Timeout: config.UpstreamTimeout}`，**仅当 `pipe.Request.Stream` 用无超时 client**（L365-368）；
- `client.Do(upReq)`（L369）；
- 非 2xx → failover 失败路径（L391-423）；
- **成功分流（L425-426）**：
  - `if !pipe.Request.Stream`（L426）→ 非流式：`io.ReadAll(resp.Body)` → `extractUsageNonStream(respBody)` → `proxyStreamAttempt(running)` → `ProxyAfterUpstream` → success 事件/健康/attempt 收尾/finish log → `writeProxyResponse`（L427-486）；
  - `else`（L489-503）→ `proxyStream` 逐块 SSE 透传（L496），流结束后收尾。

**proxyStream（L755-856，核实）**：
- 第一件事就是 `copyProxyHeaders`+`w.WriteHeader(resp.StatusCode)`（L759-760）——**从这一刻起响应头已定、Content-Type=text/event-stream，插件不可再改响应形态**；
- 逐行 `reader.ReadString('\n')`，每行触发 `ProxyStreamChunk` hook（L808），写客户端 + Flush（L816-839）；
- `parseUsageLine` 累计 usage（L829-836）、`isSSEDone` 判 `[DONE]`（L843）、客户端断连经 `pipe.HTTPRequest.Context()` 取消（L799-803）、流中断写 `writeSSEError`（L850）。

**结论（插入点，明确位置）**：
- **缓冲路径不能复用 `proxyStream`**（它已向客户端写头/写行，无缓冲语义），必须新写。但因为缓冲函数只需「读上游 body 的 SSE 行、不写客户端」，最干净做法是**新写一个只读不写的 `readBufferedSSE(...)`（返回拼接好的 body + usage），复用 `isSSEDone/parseUsageLine`（同包私有，可直接用）**。
- **改动点全部在 `proxyAttempt` 内、改动最小**：
  1. 在构造上游 client（L365-368）前，读 metadata 标记；若命中 force_stream（且 `!Request.Stream`）→ 用**无超时 client**（否则缓冲长流会被 `UpstreamTimeout` 切断）。
  2. 上游请求体 stream:true 的改写由能力插件在 `proxy:before-attempt` 完成（见 §4 方案 B）；proxyAttempt 不必感知能力名，只需读标记。
  3. 在成功分流 `if !pipe.Request.Stream` 块内（L426-435 的 ReadAll 处）：命中标记 → 改为 `respBody, usage := s.readBufferedSSE(...)`；**未命中 → 保持现状 `io.ReadAll` + `extractUsageNonStream`**。之后**共用同一段成功收尾 tail（L435-486）**（after-hook / success 事件 / 日志 / `writeProxyResponse`），保证 request-log、field-filter 响应钩子、usage 计量语义不变。
- **不要改 `pipe.Request.Stream` 本身**：它驱动日志 `stream` 列、`proxyStream` 分流、client 超时。force_stream 请求客户端就是非流式，`Stream` 必须保持 false（L489 才不误入透传）。用独立 metadata 标记承载「实际转发已强制流式、对外仍报非流式」——印证计划 §6 判断。
- **响应头处理**：上游返回 `Content-Type: text/event-stream`。缓冲完成后构造响应时**不能 clone resp.Header**，须给干净的 `Header` + `Content-Type: application/json` 再 `writeProxyResponse`（`copyProxyHeaders` 会剥 hop-by-hop/`Content-Length`，但 Content-Type 会被上游值污染，需显式覆盖）。这是原计划未提的点。

### 2.2 能力匹配语义（修正原计划 §2.3 / §4 / §5-4 的误导）

**核实：`service.go RouteCapability(model, capability, channelID)`（L58-90）是单渠道旧语义、且只被单测引用（model_gateway_test.go）**，生产能力插件全都不用它。它用 `channelID` 单值构造 `ChannelRequestScope{IDs,BaseURLs}`，**不走 `ChannelScopeFromMetadata`，也不带虚拟模型名**——聚合模型（渠道级/Key 多选目标会写 `__channel_candidates`、`__current_channel=""`）下会匹配不到渠道约束路由（types.go L232-252 注释明确警示这一点）。

**真正的能力插件查法（sensitive-filter / field-filter / message-inject / request-log 完全一致，逐字核对）**：
```go
scope := types.ChannelScopeFromMetadata(pipe.Metadata, s.requestChannelBaseURLs) // 多渠道/候选语义
virtualModel := types.VirtualModelFromMetadata(pipe.Metadata)
routes, err := DecideRoutesScope(...) // SelectCapabilityRoutesEx(routes, capName, model, virtualModel, scope)
```
读表：`s.repo.ListCapabilityRoutes(ctx)`（`db.Repository`，SQLite `capability_routes` 表，admin_repository.go L20-47），repo 为 nil 或读失败回退 `s.st.Read(types.FileCapabilityRoutes, &routes)`（JSON）。`requestChannelBaseURLs(term)` 支持 key id 精确反查组 base_url / 渠道名返回组内启用 Key 的 base_url（sensitive-filter service.go L62-84）。

**修正**：本能力若要命中「聚合模型 + 渠道约束」也要用 `ChannelScopeFromMetadata + SelectCapabilityRoutesEx + ListCapabilityRoutes`，**绝不能复用 `RouteCapability`**。计划 §2.3「service.go 已有 RouteCapability 可查能力路由」「§5-4 用 ChannelScopeFromMetadata + candidates 复用 RouteCapability」这两句互相矛盾且引用了错误复用源——正确的复用对象是各插件自己的 `DecideRoutesScope`/`requestChannelBaseURLs` 模板（见 §4 方案 B）。

### 2.3 插件模式（插件需要哪些文件/如何 Apply/注册）

核实的完整模板（sensitive-filter 为范本，field-filter/message-inject 同构）：
- 一个独立目录 `plugins/<name>/`，含 `plugin.go`（`New()`/`Manifest()`/`Apply()`）、`service.go`（Service + `capabilityName` 常量 + `DecideRoutesScope` + `requestChannelBaseURLs` + 事件 handler）、测试。
- `Manifest()`：`Inject: ["store","logger","db"]`、`Provide: ["<name>"]`；需要**排序约束**（如 sensitive-filter 依赖 message-inject 先执行）时在 Inject 里额外声明依赖（sensitive-filter plugin.go L46）。
- `Apply()`：取 store/logger → `NewService` → `db` 存在则 `SetRepository`（`db.NewRepository`）→ `ctx.Set("<name>", svc)` → `ctx.On(modelgateway.ProxyBeforeAttempt, svc.HandleProxyBeforeUpstream)`。
- `plugins/registry.go` `All()` 追加一行 + import（L7-29 列表 + L33-53）。
- 子请求（vision_v2 走网关主链路的 `__sub_request`）在 handler 开头按 `__sub_request_skip_security` 早退，避免误伤。

### 2.4 数据落点与 CapabilityRoute 字段（核实）

- `types.FileCapabilityRoutes = "capability_routes.json"`（types/types.go L16）；SQLite 表 `capability_routes`（列 `capability, route, models_json, channel_ids_json, channel_base_urls_json, via_options_json, replacements_json, field_rules_json, injections_json`）。
- `CapabilityRoute`（types/types.go L421-431）字段：`Models/ChannelIDs/ChannelBaseURLs/Capability/Route` + 各能力私有槽 `ViaOptions(视觉)/Replacements(敏感)/FieldRules(字段)/Injections(消息注入)`。
- **`force_stream` 首版纯 on/off，无需在 CapabilityRoute 新增任何字段**（同 `request_log`：capability + `route=proxy` 命中即生效，无槽位）。原计划 §5.1「预留字段」多虑；如需后续自定义（如是否保留 reasoning_content/usage），届时再加 bool 槽。
- 能力 token 均蛇形常量（`sensitive_filter/field_filter/message_inject/request_log`）→ 建议 token=`force_stream`。
- admin-api 的 capability-routes CRUD（service.go L1100+）**不对 capability 名做白名单校验**，读 `types.CapabilityRoute` 直接持久化 → 新增能力名后端可保存，无需改 admin-api。

### 2.5 SSE→非流式拼装：不存在现成整包还原（核实，含两处可参考实现）

- 全项目**没有**「收集 delta → 还原 content/tool_calls/reasoning → 构造整包非流式 chat.completion」的实现。计划 §2.4 结论成立。
- 但有两处**局部参考**（原计划遗漏）：
  1. `translate/service.go` L296-360：`bufio.Scanner` 逐行读 SSE，把 `choices[0].delta.content` 累进 `strings.Builder`——content 累积的最小范式，但没有 id/object/multi-choice/tool_calls/reasoning/usage 还原。
  2. `vision_v2/stream.go`：OpenAI 块结构 `Choices[].Delta.ToolCalls`（含 `index/type/id/function.name+arguments` 分段）、Claude `content_block_start/delta/stop`、responses `response.function_call_arguments.delta`+按 `item_id` 归并——三套协议的 chunk 结构最全参考。
- **每块长什么样（核实）**：proxyStream 里上游 SSE 每块 = 一整行 `data: {...}\n`（含 `data: ` 前缀与行尾）。OpenAI chat SSE：`data: {"choices":[{"index":0,"delta":{...},"finish_reason":null}]}`；`finish_reason` 通常在**倒数第二块**（非 null），usage 通常在**最后一块**（部分厂商首块就带仅 prompt_tokens 的 usage，proxy.go L831-835 注释有警示）；`data: [DONE]` 是最后一行。`[DONE]` 后上游 keep-alive 不主动关 body，**必须靠 `isSSEDone` 主动退出**（proxy.go L840-845 注释），缓冲路径同样要处理，否则阻塞到连接 idle 超时。

---

## 3. 架构关键差异（与用户对齐，方案正确性的前提）

**sensitive-filter 为何纯插件即可**：它只改请求体（`Request.Body`），不改响应路径——响应仍由 model-gateway 的 `pipe.Request.Stream` 分支决定，逻辑闭合。

**本能力做不到纯插件**：缓冲拼包要接管「读上游 SSE」且不能提前写客户端，`proxyStream` 一开始就写头/flush，所以**缓冲读取 + 整包写回必须在 model-gateway 核心新增路径**（proxyAttempt 内），`proxy:before-attempt` 拿不到也不该碰 `w`。

**但「能力命中判断」仍应放插件**（修正点，见 §4）。

---

## 4. 实现归属：推荐方案 B（修正原计划的方案 A 倾向）

### 方案 A（原计划推荐）：model-gateway 核心内置
- proxyAttempt 内自己查 force_stream 路由 + 改 body stream:true + 缓冲。
- **弊**：核心要复制一份 scope 式能力路由查询（`requestChannelBaseURLs` 多渠道反查 + `SelectCapabilityRoutesEx`），否则只能退回单渠道的 `RouteCapability`（错）；且核心要硬编码 `force_stream` 能力名——**代码库刻意让核心只发事件、能力全是插件**（types.go L175-183 注释、sensitive-filter 等），A 破坏该分工，改动面并不比 B 小。

### 方案 B（推荐）：薄插件做匹配 + 核心只认标记
1. 新建 `plugins/force-stream/`（对标 sensitive-filter/message-inject）：
   - `capabilityName = "force_stream"`；
   - `Apply` 订阅 `ProxyBeforeAttempt`；
   - handler：子请求早退 → `ChannelScopeFromMetadata + VirtualModelFromMetadata + SelectCapabilityRoutesEx` 查路由 → 命中且 `route=proxy` **且 `pipe.Request.Stream==false`**（客户端非流式）且 `pipe.Request.Path=="chat/completions"` → 把 `pipe.Request.Body` 里 `stream` 字段改 true（复用 model-gateway 现有局部改写模式，见下）→ 写标记 `pipe.Metadata["__force_stream"]=true` → 返回 pipe。
   - 未命中/native/其它 path/本来流式 → 不动（原样透传，绝不误伤）。
2. model-gateway 核心只做**机制性改动**、不认识能力名：
   - proxyAttempt L365-368：`!Request.Stream && metadata["__force_stream"]==true` → 用无超时 client；
   - proxyAttempt L426 非流式分支：命中标记 → `readBufferedSSE` 读+拼整包（返回 body+usage），否则保持 `io.ReadAll`；之后共用同一段成功收尾 tail。
3. `registry.go` `All()` + import 加一行。

**为什么 B 更干净**：能力匹配复用每个插件都有的现成模板（路由查法、子请求早退、request_log/message-inject 的「原始 body 快照」处理都可参考）；核心只认一个 bool 标记，保持「核心=事件机制、能力=插件」分工。B 的唯一代价是 registry 一行 + 插件与核心约定标记 key，远小于 A 在核心复制能力查询+硬编码能力名的耦合。

> 补充（关键细节）：插件的 body stream 改写要**基于原始 body**（参考 message-inject 的 `__message_inject_orig_body` 快照思想），防止 failover 多次触发 before-attempt 时把已改 true 的 body 再处理或叠加——本项目 before-attempt 在每次渠道尝试都触发（proxyAttempt L314），这点必须防。

---

## 5. 开放问题（需你确认）

1. **能力 token**：`force_stream`（蛇形，与现有一致）？插件目录 `plugins/force-stream/`。
2. **实现归属**：方案 B（薄插件匹配 + 核心机制，推荐）vs 方案 A（核心内置）。
3. **首版 path 范围**：只做 `chat/completions`（推荐）——OpenAI SSE 协议单一、参考 vision_v2 结构成熟；`responses`（按 `item_id` 归并 tool args）与 Claude `messages`（content_block 事件）协议完全不同，留待后续，未命中原样透传不误伤。
4. **字段保留策略**（v1 默认，可后续加配置）：还原时保留 `reasoning_content`、保留 `usage`、保留多 `choice` 与 `tool_calls`？建议全部保留（忠实还原），如个别平台要剔除再后置加 bool 配置。

---

## 6. 拟定实现步骤（方案 B，每步独立可验证、原子 commit）

1. **types/types.go**：无需改动（无新字段）。可加常量 `MetadataForceStream="__force_stream"` 统一标记 key（放 model-gateway types.go 或 force-stream 包，建议放 model-gateway/types.go 便于核心引用、避免字符串散落，与 `MetadataRequestLogID` 同风格）。
2. **plugins/force-stream/service.go + plugin.go + _test.go**：
   - `capabilityName="force_stream"`；
   - `requestChannelBaseURLs`（照抄 sensitive-filter L62-84）；
   - `DecideRoutesScope`（照抄）；handler：早退 → 查路由 → 命中且非流式且 path==chat/completions → 改 body `stream:true`（对**原始 body**）→ 设标记。
   - body 改写：参考 `model-gateway` 的 `rewriteModelField`（按字节区间改，保序无损，service.go L530-613）思路实现一个 `setStreamTrue(body)`（或复用其 JSON key 定位技巧）；body 非 JSON / 无 stream 字段需补 `"stream":true`。
3. **registry.go**：import + `All()` 追加 `forcestream.New()`。
4. **model-gateway/proxy.go**：
   - `proxyAttempt`：选 client 处（L365-368）+ 非流式成功分支（L426）读标记；
   - 新增 `readBufferedSSE(resp, pipe) ([]byte, contracts.TokenUsage)`：bufio 逐行（**不写客户端**），`data:` 载荷 unmarshal → 累 content / reasoning_content / tool_calls(按 index+function 分段拼) / 抓 finish_reason / `parseUsageLine` 累计 usage；`isSSEDone` 主动退出；返回**干净 header**（Content-Type: application/json）的 ProxyResponse + usage。
   - **复用**：`isSSEDone`/`parseUsageLine`/`estimateTokens`（同包）直接可用；构造的 ProxyResponse 走既有成功收尾 tail（ProxyAfterUpstream / success 事件 / attempt 两阶段日志 / `writeProxyResponse`），不重复写这套日志逻辑。
5. **path 白名单**：仅 chat/completions（核心与插件都只对 `Request.Path=="chat/completions"` 放行；插件侧判断已够，核心对标记本身在非 chat 路径不设即可）。
6. **测试**（`plugins/model-gateway/` + `plugins/force-stream/`）：
   - 复用 `newEchoServer(t, resp, status, sse []string)`（proxy_test.go L34-66，已支持 SSE 逐段 flush）构造假上游：返回多块 `data:{...}`（含 tool_calls 分段、reasoning_content、usage 块、`data:[DONE]`）。
   - 断言：客户端收到 `application/json` 单包；`content` 拼接正确；`tool_calls[i].function.arguments` 各段正确合并；`reasoning_content`、`usage`、`finish_reason` 回填；上游收到 body 的 `stream==true`；未命中路由时原样透传（stream:false 不动）；异常中断 → 错误响应不吐半包；本来就 stream:true 的流式请求不受影响。
   - 单测用 `newTestService`（model_gateway_test.go L69-76，`database=nil` → `SetRoutingServices` 不建 repo 走 JSON 兜底，或建临时 db 走 SQLite——参考 proxy_test L485/L540 的建库写法）。
7. **前端能力路由 UI**（对标其它能力，改动集中在 `frontend/src/components/capability-routes/CapabilityRouteEditor.vue`）：
   - 加 `const CAP_FORCE_STREAM='force_stream'`；`capabilityOptions` 数组加一项；
   - `submit()` 里像 `CAP_REQUEST_LOG` 一样加**无配置放行**分支（否则会落到 `else if (!form.viaOptions.some(...))` 被误拦，见 L316-320）——**这是纯 on/off 能力最容易漏的一处**；
   - `routeOptions` 已默认 proxy/native（非 sensitive 分支即可），无需改；`routeHint` 建议加一句文案；
   - 无独立 List 子组件（对标 request_log，无需 SensitiveWordList 类兄弟文件）。
   - **是否本轮做前端可后置**：合理——后端加 capability + 核心缓冲后，可先用 JSON/API 直接写 capability_routes 验证；前端只是补个下拉选项，风险低、可后置单独 commit。
8. **文档/示例**：能力路由表可配 `force_stream` 的示例数据。
9. **git commit**：每步原子提交（见第 10 节）。

---

## 7. 拼装算法要点（务必按此做，多坑）

输入：`data:` 行（带前缀+行尾），OpenAI chat SSE（`chat/completions`）。

1. **拆行**：`bufio.Reader.ReadString('\n')` 循环；只处理 `data: ` 开头且非 `[DONE]` 的行（参考 `trimSSEDataPrefix`/`isSSEDone`）；跳过 `event:`/空行/注释行。
2. **每块结构**（OpenAI）：`{"choices":[{"index":N,"delta":{...},"finish_reason":null|"stop"|...}],"usage":{...},"id":...}`。**delta 内容字段**：`content`（字符串，可直接累）、`reasoning_content`（火山等）或 `reasoning` 里嵌套、`tool_calls`（数组，元素带 `index`；每个 tool_call 的 `function.name` 只在**首块**给、`function.arguments` 是**逐块字符串增量**要按 index 拼接）、`role`（首块 assistant）。
3. **按 choice index 建累加器**：`content: strings.Builder`、`reasoning: strings.Builder`、`toolCalls: map[index]{id,name,args Builder}`。**tool_calls.arguments 必须逐块 `args.WriteString(delta.Arguments)`**（不做整段替换，否则丢字）；name 首块写一次即可。
4. **多 choice**：遍历每块 `choices[]`，不要只取 index 0（translate 只取 0 是特例）。
5. **finish_reason**：非 null 时记到对应 choice；usage 抓最后 usage 块（`parseUsageLine` 复用，注意「首块带 usage 只有 prompt_tokens」别提前锁定——proxy.go L831-835）。
6. **终止**：`data: [DONE]` 主动 break（上游 keep-alive 不关 body，等 EOF 会卡 90s）；缓冲途中上游 body EOF 无 [DONE] 视为异常中断。
7. **构造整包**：`{"id":<用首块 id 或自生成>,"object":"chat.completion","created":<首块/now>,"model":<pipe.Request.Model>,"choices":[{...message:{role:"assistant",content,reasoning_content?,tool_calls?},finish_reason}],"usage":{...}}`。忠实还原内容与 usage；`tool_calls` 转成消息形态（`{id,name,type:"function",arguments:<拼接串>}`）。
8. **响应头**：`Content-Type: application/json`（覆盖上游 text/event-stream），不 clone 上游 SSE header；`Transfer-Encoding: chunked` 不写（整包 body 由 Go 自动算 Content-Length）。
9. **客户端断连**：读流前/读流中监听 `pipe.HTTPRequest.Context()`，断了直接 abort（不留半包、不写客户端）。
10. **错误语义**：异常 → 复用 `writeGatewayError`/`writeOpenAIError` 写错误 JSON，不吐半包（对应 §1 边界）。

---

## 8. 潜在风险与注意点（修正+补充）

- `pipe.Request.Stream` 被日志（L264/265/340/373/401/515/636/1393）、client 超时（L366）、分流（L426）多处读取——**绝不改它**，用独立 metadata 标记。此点原计划已对。
- **client 超时**（原计划漏）：force_stream 且 `!Stream` 会落到有 `Timeout` 的 client（L365），缓冲长流会被 `config.UpstreamTimeout` 切断——必须让标记命中也用无超时 client（等同流式）。
- **响应头/Content-Type**（原计划漏）：缓冲后要返回 application/json，不能透传上游 text/event-stream；且不能提前写头（所以不能复用 proxyStream）。
- **failover 多次 before-attempt**：body 改写必须基于原始 body 快照（防叠加/重复改 true），参考 message-inject。
- **usage 提取时机**：在整包构造前完成（proxyAttempt 非流式 tail 的「先提取再 hook」约定同理——field-filter 响应钩子会剔除 usage，事后提取得 0，导致 volc-free-quota 不扣、route_log 为 0；见 proxy.go L436-438）。缓冲路径天然在构造时已拿到 usage，注意别在 after-hook 之后再 extract。
- **拼包顺序/并发**：单请求顺序读，无并发拼装问题；但 tool_calls arguments 跨块拼接必须按序 append。
- 聚合/多渠道命中：必须用 scope 式匹配（ChannelScopeFromMetadata），否则聚合流量匹配不到渠道约束路由。
- 熔断/failover：缓冲失败按渠道失败返回 `(pipe,false)`，天然触发既有 failover（勿 return handled=true）。
- **别新增第三方依赖**：拼装用标准库 `bufio/encoding/json` 即可（项目零额外依赖风格）。

---

## 9. 交付物清单

- 后端：`plugins/force-stream/`（plugin/service/测试）能力插件（方案 B）；model-gateway `readBufferedSSE` + proxyAttempt 标记分流；registry 一行。
- 能力路由表可配 `force_stream`（示例数据；后端 CRUD 无需改）。
- 前端（可后置）：`CapabilityRouteEditor.vue` 加 `force_stream` 下拉项 + 无配置放行。
- 本轮交付前展示：实现思路、git 历史、改动文件、测试结果（httptest 假上游 SSE 单测）。

---

## 10. 建议 git commit 序列（每步可独立验证）

1. `test: force-stream 能力 + 缓冲拼装的 SSE 假上游单测骨架`（先红）；
2. `feat: model-gateway 新增 readBufferedSSE 缓冲拼装（chat/completions）+ proxyAttempt 标记分流`；
3. `feat: 新增 force-stream 能力插件（路由匹配 + body stream:true + 标记）并注册`；
4. `feat(ui): CapabilityRouteEditor 增加 force_stream 能力选项`（可后置）；
5. `docs: 能力路由表 force_stream 示例`。
