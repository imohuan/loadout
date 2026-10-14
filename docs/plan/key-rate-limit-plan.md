# 「Key 限流 + 账号轮换」能力方案

> 状态：**方案已确认，待开发**
> 已定口径：限流对象 = 上游渠道 Key（上游账号）；滑动窗口；全部账号都用满时**默认返回 429**（可配置为继续使用）；
> 计数只放内存（重启清零），规则配置仍随能力路由表持久化。

## 一、问题与现状

现在网关对渠道 Key 完全没有用量约束。`proxyForward` 按渠道表顺序取候选 Key，
第一个成功就返回（`plugins/model-gateway/proxy.go:539`）。只有**失败**时才换下一个
Key（failover，`proxyAttempt` 失败 → 循环继续）。

后果：只要排在前面的那个账号 Key 一直成功，所有流量就都压在它身上，容易被上游
风控/限速。用户想要的是「按次数均摊」：到量就换下一个账号，而不是等到报错才换。

关键点：**换 Key 的机制已经存在**（候选列表 + 依次尝试），缺的只是
「谁排前面 / 谁的配额用完了」这一层可控的候选筛选。

## 二、需求拆解

| 需求 | 落点 |
|---|---|
| 适用范围：某个平台 / 某个模型 / 全部模型 | 直接复用能力路由的 `models` + `channel_base_urls`（平台级）+ `channel_ids`（Key 级）三档作用域 |
| 限制单个账号在窗口内的请求次数 | 内存里按「渠道 Key + 作用域签名」记最近请求时刻，滑动窗口内计数 |
| 超出后自动换另一个账号的 Key | 候选解析后做一次筛选：把已达上限的 Key 从候选里剔除，剩下的仍按渠道表顺序尝试 |
| 所有账号都用满时 | `on_exhausted` 配置：默认 `reject`（返回 429），可选 `reuse`（继续使用，宁可超额） |
| 其他设置保持不变 | 不命中规则的请求、未达上限的 Key 走原路径，零行为变化 |

## 三、总体设计（推荐方案）

### 3.1 新增能力 `key_rate_limit`

复用现有的能力路由表（`capability_routes`），一行 = 一条限流规则：

```
capability = "key_rate_limit"
route      = "proxy"（限流）| "native"（豁免，不限流）
models            = ["*"]（全部模型）或 ["gpt-4o"]
channel_base_urls = ["https://api.xxx.com/v1"]   ← 平台级（整个渠道组）
channel_ids       = ["ch-abc", "ch-def"]        ← 可选，精确到某个账号
rate_limit = {
  "max_requests": 20,
  "window_seconds": 60,
  "on_exhausted": "reject"
}
```

参数含义：

- `max_requests`：滑动窗口内单个 Key 允许的请求数。
- `window_seconds`：往回看多久（60=每分钟，3600=每小时，86400=每天）。
- `on_exhausted`：**所有**候选 Key 都达上限时的动作。`reject`（默认）= 返回 429；
  `reuse` = 忽略限流继续用，宁可让某个账号稍微超额，也不让请求失败。

用法举例：平台 A 配了 3 个账号 Key，规则设「每 60 秒每 Key 最多 20 次」→ 第一个账号
用满 20 次后，第 21 次自动落到第二个账号，再满落到第三个；三个都满则按 `on_exhausted`
返回 429。等第一个账号的最早那 20 次滑出 60 秒窗口，它就自动重新可用。

### 3.2 计数口径（滑动窗口）

计数桶 = `(渠道 Key, 作用域签名)`；窗口是**滑动**的，即「看从此刻往回数 N 秒」。

实现：每个桶只保存最近若干次请求的时刻（最多留 `max_requests + 1` 个），判定时先丢掉
滑出窗口的旧时刻，再看剩下的个数是否达到 `max_requests`。

- 判定准：不会出现「整点前一秒打满、整点后又打满」的 2 倍突刺（固定窗口的经典漏洞）；
- 内存有界：每个桶最多 `max_requests + 1` 个时间戳，够判断「超了」即可，不需要无限记录；
- 清理：后台每隔一段时间（例如 1 分钟）扫一遍，丢掉「最后一条记录也已滑出窗口」的空桶，
  防止内存随渠道数量无限增长。

作用域签名（决定几个模型共用一个桶）：

- `models: ["*"]` → 签名 `*`，该 Key 的**所有模型请求共用一个桶**；
- `models: ["a", "b"]` → 签名 `a|b`（排序归一化），该规则覆盖的模型共用一个桶；
- `models: ["a"]` → 签名 `a`。

即「配额跟着规则走」：一条规则 = 一份配额。同一 Key 被多条规则覆盖时各自独立计数，
更严的那条先触发。

### 3.3 换 Key 与 429 的落点（核心改动点）

在 `proxyForward` 解析并过滤完候选之后、进入尝试循环之前，新增一个 waterfall 事件：

```
事件：proxy:channels-resolved
载荷：*ChannelCandidatesPayload{ Pipe, Model, VirtualModel, Candidates []ResolvedChannel }
语义：插件把 Candidates 改写成「过滤后」的候选列表；
      返回 *GatewayError 表示直接拒绝（用于 429）；
      返回 nil 载荷或空列表 = 不改动，沿用原候选。
```

`model-gateway` 里只加一个 emit + 读回，约 15 行。精确位置：`proxyForward`
（`plugins/model-gateway/proxy.go:539`）里三处候选过滤（`__channel_candidates` /
`__current_channel_base_url` / `__current_channel`）全部完成之后、
`for _, ch := range candidates` 之前。拒绝分支复用现成的 `rejectBySecurity`
（写一条 failed attempt + finish 日志，再输出 429 JSON），转发日志里能直接看到原因。

为什么放在这里（而不是入口的 `proxy:before-upstream`）：

- 平台前缀（v2 hint）、聚合目标候选（`__channel_candidates`）、
  `channel_states` / `model_states` 熔断过滤都已经生效，插件只需要做「减法」；
- 在入口自己复刻一遍候选解析（模型匹配 + 健康检查 + base_url 归一化）会与核心
  逻辑分叉，日后核心一改就悄悄失效。

达上限的 Key 从候选里**剔除**而不是「排到后面」，是因为剔除天然保留了原来的轮换语义：
剩下的 Key 仍按渠道表顺序（= 用户配置顺序）尝试，用满就轮到下一个。

### 3.4 计数时机与并发

「消费一次」记在 `proxy:before-attempt`（每次渠道尝试前，
`plugins/model-gateway/proxy.go:292`），此处 `pipe.Metadata["__current_channel"]`
就是本次真实要用的 Key，语义最准：

- 同一请求在同一个 Key 上重试（failover 回退）会记多次 —— 正确，它确实访问了该账号；
- 聚合模型的中间失败 attempt 也记 —— 与「实际访问次数」一致；
- 视觉识别子请求（`ForwardSubRequest`，同样消耗上游账号额度）默认也记。

并发下是**软上限**：多个请求可能同时读到「还差 1 次」，都放行，实际略微超额。
要严格不许超额，得把「消费」提前到 3.3 的候选筛选阶段做原子抢占；默认不做
（简单，且不会因为抢占失败误伤正常请求）。

### 3.5 计数只放内存，且不写 `model_states`

- **内存**：计数器是一个带锁的 map，进程重启清零。用户已确认可接受，因此**不需要**新建
  计数表、不需要迁移计数结构。
- **规则配置仍持久化**：`rate_limit` 参数随能力路由表存库（见 4.1），否则重启后规则丢失。
- **不写 `model_states`**：现有插件（volc-free-quota）用 `model_states` 冷却实现「跳过某 Key」，
  但那是「上游不可用」语义，会出现在「模型状态」页、也会被「恢复全部」按钮清掉。
  限流是**正常周转**，不是故障，只写在插件自己的内存计数里，窗口滑出即自动恢复，
  无副作用、无定时任务。

## 四、代码改动清单

后端：

1. `plugins/types/types.go`（`CapabilityRoute`，:423）加字段
   `RateLimit *RateLimitRule`（json: `rate_limit`），
   并定义 `RateLimitRule{MaxRequests, WindowSeconds, OnExhausted}`。
2. `core/db/migrate.go` 新增 v50：
   `ALTER TABLE capability_routes ADD COLUMN rate_limit_json TEXT NOT NULL DEFAULT '{}'`
   —— 只加规则配置列，**不加计数表**。
3. `core/db/admin_repository.go`（:20 / :50）能力路由读写补 `rate_limit_json`；
   `core/db/import_admin.go:172` 的导入同步补列。
4. `plugins/model-gateway/proxy.go`：新增 `proxy:channels-resolved` 事件常量
   （放 `plugins/model-gateway/types.go`，与 `ProxyBeforeAttempt` 等同处），
   `proxyForward` 解析候选后 emit 并读回；Payload 类型定义在 `types.go`。
5. 新插件 `plugins/key-rate-limit/`（照 `plugins/force-stream/` 的骨架写）：
   - `service.go`：规则选择（`types.SelectCapabilityRoutesEx`，与 sensitive-filter 一致）
     + 平台/Key 作用域解析 + 候选过滤 + 超限拒绝；
   - `counter.go`：滑动窗口内存计数器 + 后台清扫 goroutine；
   - `plugin.go`：`Inject: store/logger/db`，订阅两个事件；
   - 读表失败、计数失败一律 **fail-open**（不限流），绝不因为限流误伤请求。
6. `plugins/registry.go` 的 `All()` 追加一行（顺序放在 `aggregate` 之后，保证聚合候选已写入）。

前端：

7. `frontend/src/lib/types.ts`（`CapabilityRoute`，:346）加 `rate_limit?` 与 `RateLimitRule`。
8. `CapabilityRouteEditor.vue`：能力下拉加 `key_rate_limit（Key 限流·账号轮换）`、
   `capabilityOptions`（:247）与 `routeHint`（:269）文案、三个输入项
   （窗口内最大次数 / 窗口秒数 / 全部用完时：返回 429 或 继续使用）。
9. `CapabilityRouteTable.vue`：代理配置列加摘要（如「每 Key 60s 最多 20 次，超额换 Key」）。
10. 平台/模型作用域不用新写组件：目标模型用现成 `TargetModelPicker`，渠道用现成
    `ChannelGroupPicker`（渠道级 = 平台，Key 级 = 指定账号）。这是选「复用能力路由」最大的好处。

## 五、验证计划

- 单测（纯逻辑）：滑动窗口边界（第 20 次放行、第 21 次拒绝；时刻前移后旧记录到期重新可用）；
  作用域签名归一化；到期空桶被清扫回收。
- 集成测试：`testkit/fake-llm` 起两个 Key（同一 base_url 两个账号），规则设 `max=1`；
  第一次请求落到 Key1，第二次落到 Key2，第三次 `reject` 模式返回 429、`reuse` 模式正常放行。
- 回归：不配规则时 `/v1` 全部现有测试不变；配 `native` 豁免行时该模型不限流。
- 手工：能力路由页新建/编辑/删除规则，转发日志里确认 attempt 落到不同 Key、429 时原因可读。
- 收尾：`go vet ./...`、`go test ./...`、前端 `pnpm test` + `pnpm build`。

## 六、备选方案（不推荐）

不动核心，插件在 `proxy:before-upstream` 里自己算候选并写 `__channel_candidates`
（核心已有该机制的消费逻辑）。优点：核心零改动。缺点：插件要复刻 `resolveChannels`
的模型匹配 + 健康检查 + base_url 归一化；一旦核心改动就会静默失配，且会与聚合写入的
候选语义互相干扰。除非明确要求「核心不动」，否则走 3.3 的事件扩展点。

## 七、已确认的决策与遗留口子

已确认：

1. 限流对象 = **上游渠道 Key**（上游账号），不是网关签发的 `sk-` 调用密钥。
2. 窗口 = **滑动窗口**（看过去 N 秒），不是固定整分钟窗口。
3. 所有账号都用满时 = **默认返回 429**，可配置为「继续使用」。
4. 计数**只放内存**，重启清零；规则配置仍落在能力路由表。

遗留（实现时按此默认，后续可调）：

- 并发下计数为软上限，允许小幅超额。
- 视觉识别等子请求默认计入限流（也消耗上游额度）。
- 429 使用 `type: "key_rate_limited"`，响应体沿用 OpenAI 错误格式。

