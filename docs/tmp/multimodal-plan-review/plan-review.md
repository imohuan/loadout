# 多模态 MCP 插件计划 · 审核报告

审核人：code-developer（PenguinHarness）
审核对象：`docs/plan/multimodal-mcp-plugin-plan.md`
审核方式：计划全文阅读 + codegraph MCP 逐条验证代码接口
索引：`D:/Code/Git/loadout`（450 文件 / 7506 节点）

---

## 一、接口验证结论（6 项）

| # | 计划引用的接口 | 是否存在 | 签名/用法核对 | 结论 |
|---|---|---|---|---|
| 1 | `mcpkit.NewServer(name, tools)` | ✅ | `NewServer(name string, tools []ServerTool) *mcp.Server`（core/mcpkit/mcpkit.go:453）| 正确 |
| 1 | `mcpkit.ServerTool{Name, Description, InputSchema, Handler}` | ✅ | `Handler func(ctx, args map[string]any) (*ToolResult, error)`（mcpkit.go:440）| 正确，Handler 收 map 参数 |
| 1 | `mcpkit.ToolResult` | ✅ | `{Content []ContentPart, IsError bool}`（mcpkit.go:49）| 正确 |
| 2 | `HandleProxy(w, r)`（方案 B 内部调用）| ✅ | `HandleProxy(w,r) → proxyHandle(w,r,"v1")`（proxy.go:30/45）| **可用但有更优解（见下）** |
| 3 | `store.Read(FileChannels)` | ✅ | `Read(name string, v any) error`（store.go:83）；`FileChannels="channels.json"`；admin-api 即用此法读渠道 | 正确 |
| 3 | `st.Decrypt(APIKeyCipher)` | ✅ | `Decrypt(ciphertext string)(string,error)`（store.go:167），AES-GCM | 正确 |
| 4 | `NormalizeBaseURL` | ✅ | `strings.TrimRight(s,"/")`（expand.go:17）——**仅去尾斜杠，不做域名归一** | ⚠️ 用法需注意 |
| 5 | `ctx.Get("model-gateway").(*modelgateway.Service)` | ✅ | vision_v2/plugin.go:36 正是这样取，再 `SetGateway` | 正确 |
| 6 | `ManagementView.vue` 的 `<Tabs>` | ✅ | `<Tabs v-model="activeTab">` + `<TabsTrigger value>` + `<TabsContent value>`（ManagementView.vue:245-249）| 正确，加 tab 很简单 |

### 重点问题：方案 B 直接调 `HandleProxy` 是"能用但绕远"

**结论：计划引用的 `HandleProxy` 真实存在、可被内部调用、且确实会按模型 failover，但计划漏掉了现成的 `ForwardSubRequest`，而它才是 vision_v2 实际在用的、专门为"内部子请求"设计的通道。**

验证到的关键事实：

1. **HandleProxy 只读 `r.Body` / `r.URL.Path` / `r.Header`**（proxy.go:46-77），不依赖外部认证中间件注入的状态。key 白名单校验是**可选的**：`if key, ok := gatewaykeys.APIKeyFromContext(r.Context()); ok && ...`（proxy.go:103）——内部调用时 context 里没有 key，直接跳过白名单，走转发。出站 key 从渠道解出（`resolveChannels → st.Decrypt(channel.APIKeyCipher)`）。**所以"绕过认证中间件、无需 sk-key"的断言成立。**
2. `httptest.NewRecorder() + http.NewRequest()` 构造可行——`ForwardSubRequest` 内部正是这么做的（proxy.go:678, 712）。
3. failover 链路确实存在：`proxyHandle → proxyForward → resolveProxyChannels → 候选循环 + tryProxyAggregateFailover`（proxy.go:509 起）。
4. **但**直接调 `HandleProxy` 有几个坑，而 `ForwardSubRequest` 都已处理：
   - `ForwardSubRequest` 会给 pipe 打 `__sub_request = true` 和 `__sub_request_skip_security = true`（proxy.go:671-672），让 vision/sensitive/field-filter 等输入 hook **早退跳过安检**，还能防递归。直接调 `HandleProxy` 时这些标记**不会设置**，安检 hook 会照常跑，且视觉/安全插件可能被误触发。
   - `ForwardSubRequest` 已封装 recorder、非流式 4xx 转 error、流式 streamWriter 回调、sub-request 日志（proxy.go:661-732）。直接调 HandleProxy 需要自己再造一遍这套。
   - `HandleProxy` 的请求路径得手动拼 `/v1/chat/completions`；`ForwardSubRequest` 用 `pipe.Request.Path` 更干净。

**建议：计划 2.3 / call.go 改为"通过 `model-gateway.Service.ForwardSubRequest(ctx, pipe, streamWriter)` 走子请求通道"，而不是裸调 `HandleProxy`。** 这既符合"内部走主链路、网关 failover"的意图，又是 vision_v2 已在用的成熟路径。若坚持方案 B 裸调 HandleProxy，需在 call.go 自行补 `__sub_request`/`__sub_request_skip_security` 标记并处理流式与 recorder，属重复造轮子。

---

## 二、逻辑问题

1. **【高】内置模型名 → 渠道路由不可控。** 计划让网关按内置模型名自动 failover（2.3），但 `resolveChannels` 在模型不被任何渠道明确支持时，会**回退到 `Models` 为空的"未知渠道"**（ResolveChannelsForModel, service.go:134-135）。也就是说：如果内置模型名（如 `doubao-seed-2-1-pro-260628`）不在方舟渠道的 models 列表里，网关可能把它路由到其他非方舟渠道，识别必然失败或乱路由。**计划缺一道"内置模型名 → 方舟渠道"的绑定校验/提示**（未进计划文件清单，也未在测试里覆盖）。

2. **【中】`NormalizeBaseURL` 不足以识别"方舟平台"。** 它只去尾斜杠（`TrimRight(s,"/")`）。方舟 base_url 常带 `/api/v3` 之类的路径后缀，不同渠道写法（`https://ark.cn-beijing.volces.com/api/v3` vs `.../api/v3/`）归一后能比，但**"识别域名是方舟"需要的是 host 匹配，不是整串 URL 等值比较**。计划把"按 Base URL 识别方舟平台（`ark.cn-beijing.volces.com`，归一化比较）"写得太含糊——到底按 host 前缀匹配还是整串精确匹配，恰是待确认决策点 #1 没定死。**upload.go 需要额外做 host 解析，不能只靠 NormalizeBaseURL。**

3. **【低】渠道数据源双轨。** `FileChannels`(channels.json) 仍在用（admin-api service.go 大量读写），但 model-gateway 的 `resolveChannels` 在启用 routing(SQLite) 时走 `s.routing.ListChannels(ctx)`（service.go:449-453），JSON 只在 routing 为 nil 时兜底。计划 upload.go 用 `store.Read(FileChannels)` 读渠道**与 admin-api 一致，能用**，但要意识到在 SQLite 模式下可能存在 JSON 与 DB 不同步的风险——上传取 key 读 JSON，识别路由读 DB，两边渠道列表可能不一致。首版可接受，但应写进风险。

4. **【低】配置页"只配模型名"在 SQLite/聚合模型场景下表述不准。** 计划 2.4 说"内置模型名可直接指向聚合模型名"——聚合模型 failover 逻辑在 `tryProxyAggregateFailover`，主链路已处理，这一点成立；但"只读渠道配置、不新增 key"与"上传要取渠道 key"之间，计划 2.4 表格已说明，逻辑闭环。无硬伤。

---

## 三、缺漏清单

### 3.1 已有可参考（计划没点名，但对实现很重要）
- **payload 构造参考**：`test_proxy.go` 的 `buildChatPayload`（OpenAI chat/completions，原样透传，含 image_url 图片块）/ `buildResponsesPayload` / `buildClaudePayload`（test_proxy.go:160-199）——**图片的 payload 构造有现成参考**。vision_v2 的 payload 构造（tool_loop.go）也可参考。
- **视频/音频 payload 无现成参考**：`video_url` 块、`input_audio` 块、`audio task + instructions` 这几种结构在当前代码里**没有先例**（vision_v2 只处理 image_url/data URI），必须从三份火山文档新写。**plan 未明示这一点，应标注"视频/音频 payload 是净新增、风险较高"。**
- **音频 task 模板来源**：三份火山文档 `docs/archive/火山 图片理解.md / 火山 视觉理解.md / 火山 音频理解.md` 均真实存在（各 ~90-100KB），作为 instructions 模板来源**充分**。✅

### 3.2 计划确实遗漏的边界（按重要性）
1. **【高】超大文件 + 上传失败/超时的处理细节**：计划提到"需处理上传失败/超时/文件已删除"，但没落到实施步骤（upload.go 步骤只写了"上传 + 轮询 active"）。上传轮询的超时上限、失败重试策略、`file_id` 失效时的降级（退回 base64？报错？）都没定义。
2. **【高】流式与取消**：MCP 端点"支持流式透传"写在了 2.2，但工具 Handler（`mcpkit.ServerTool.Handler` 只返回 `*ToolResult`，不支持流式回调）与"识别走 ForwardSubRequest/HandleProxy 流式"之间存在机制差异——**Handler 是同步返回 `*ToolResult` 的，无法真正流式**。计划没讲清楚工具结果如何给客户端流式；识别请求的 SSE 流在工具里怎么透传也没设计。**这是方案层面没闭环的点。**
3. **【中】认证**：端点标了 `AuthMCPHeader`，配置页标了 `AuthSession`，但 MCP key 从哪来、怎么校验（现有 mcp-hub 的 key 体系是否复用？）没说明。2.1 说"只需要一个 SSE URL + MCP key"，但 `mcp-hub` 的 key 体系是否能直接给内置端点用未交代。
4. **【中】资源大小阈值自动分流**：计划步骤 3 写了"大小阈值（图10/50/25MB）自动分流"，但对 `file://` 路径、URL、base64 三态的判定顺序和"base64 判定大小"的方式（base64 后字节 vs 原始字节）没定。细节待定。
5. **【中】并发**：多个 MCP 调用并发上传同一/多个文件时，上传状态、轮询、file_id 缓存/复用（计划提到"文件默认存 7 天可复用"但没设计缓存）都没写。
6. **【低】方舟 `responses` vs `chat/completions`**：图片表里写了"chat/completions 或 responses"，视频写"chat/completions"，音频写"responses（input_audio 块）"。三种资源请求格式不同（chat vs responses），而网关 failover 链路对 responses 的处理与 chat 不同（vision_v2 做了三格式适配）。**计划没说明每种资源最终走哪种协议、网关链路是否兼容**——音频走 responses 的话，需确认网关对 `/responses` 路径的代理与 `repairToolCallSequence`（仅 chat/completions）等逻辑是否适用。

### 3.3 测试缺漏
- 无针对 `ForwardSubRequest`/HandleProxy 内部调用的**集成测试**（用 mock 网关验证 failover 行为），计划单测只覆盖 schema/payload/模板/配置/路由。
- 无**上传 + 轮询 active** 的测试（mock 火山 /v3/files 端点）。
- 无 **failover 到下一渠道** 的识别失败测试。
- 无**内置模型名未被渠道支持时**的路由测试（对应 2.1 逻辑问题 #1）。

---

## 四、风险排序（按严重程度）

1. **【高】音频走 responses + 网关兼容性未验证** —— 三种资源协议不一，网关 failover 链路对 responses 的适配（尤其安检、工具补全、流式）是否覆盖音频场景，计划未核实，可能实施期才发现断点。
2. **【高】工具 Handler 无法真正流式** —— MCP 工具 Handler 同步返回 `ToolResult`，"流式透传"在工具粒度上不成立，方案未闭环。
3. **【高】内置模型名 → 方舟渠道绑定不可控** —— 模型名不在渠道 models 时回退未知渠道，识别可能路由到错误渠道。
4. **【中】upload.go 是净新增、无先例** —— 全库无 multipart 上传 / /v3/files / 轮询的现成代码（仅 config transfer），等于从零写，且 `NormalizeBaseURL` 不足以做方舟域名识别，需自己解析 host。风险集中在步骤 3。
5. **【中】渠道双轨（JSON vs SQLite）** —— 上传取 key 读 JSON，识别路由读 DB，可能不同步。
6. **【低】认证/并发/阈值细节未定** —— MCP key 来源、并发上传、三态判定顺序待定，属设计完善项。

---

## 五、总体结论

**计划可进入实施，但需先吸收 3 处修正再动手：**

1. **改调用方式**：识别子请求优先用 `model-gateway.Service.ForwardSubRequest(ctx, pipe, streamWriter)`（vision_v2 已在用、自带子请求语义/安检跳过/recorder/流式），而不是裸调 `HandleProxy` 自己再造一遍。这是最重要的技术修正。
2. **补"内置模型名→方舟渠道"校验**，并把它加进测试。
3. **明确三种资源的请求协议（chat vs responses）与网关兼容性**，尤其音频 responses 场景；补上传轮询/失败/并发/流式取消的边界设计（至少落进实施步骤或单测）。

其余接口假设（mcpkit、store.Read/Decrypt、NormalizeBaseURL、ctx.Get model-gateway、ManagementView Tabs）经 codegraph 验证全部真实存在、用法正确。三份火山文档齐全，音频 instructions 来源充分。步骤拆分基本可独立验证，唯步骤 3（上传取 key）为净新增高风险，建议单独拆分更细。

**结论：方向正确、接口基本可信，修正上述 3 点后即可进入实施。**
