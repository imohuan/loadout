# 失败规则引擎（Failure Rule Engine）实现计划

> 状态：已审核修订 → 实现中
>
> 本计划解决「模型禁用后很快自动恢复、反复鞭尸」的核心问题，并把失败处理
> 升级为**可编辑的规则系统 + AI 兜底生成规则**（设计参考 web2api 的规则引擎）。

## 一、现状诊断（基于运行时数据库实锤）

### 数据证据（C:/Users/Administrator/.loadout/loadout.db）

1. codebuddy 平台返回 429 时业务码为 `14018 额度已用尽`（每日额度模型），
   但 `model-health` 的 `classify()` 只看状态码 → 归类 `rate_limit` → 冷却 5 分钟。
2. `CheckNow` 每 3 分钟运行，冷却到期自动恢复 available → 又被打 → 又 429。
   数据库中单 key `fail_count` 最高 **103**，最早 9/16 起持续失败至今。
3. 用户主动取消（context canceled）也被记为失败并冷却。
4. 模型级「没有可用渠道支持模型 X」错误反复累加 fail_count（98/100 次）。
5. `aggregate/strategy.go` 的硬编码正则规则与 `model-health` 的分类是**两套独立逻辑**，
   策略结果（action/cooldown）并没有真正传递给健康状态存储，形同虚设。

### 代码链路

```
请求 → model-gateway proxyForward → 失败
  → 事件 ProxyUpstreamFailed
    → aggregate.HandleProxyUpstreamFailed
      → analyzeProxyFailure（硬编码正则，AI 兜底是 TODO 空壳）
      → health.RecordFailure（model-health 自己又 classify 一遍，写 model_states）
    → selectAvailableTarget（跳过本次失败 key，health.Check 过滤）
      → 全部失败则 502
```

## 二、目标设计

### 2.1 规则模型（兼容 web2api 理念，适配 loadout 场景）

一条规则 = 作用域 + 匹配条件 + 判定动作 + 恢复策略：

```
Rule {
  id, name, enabled, source(manual|ai), confirmed, priority, scope, match, action
}
scope { provider_id?: string; model?: string }   // 空 = 全局
match { any?: Condition[]; all?: Condition[] }
Condition { field: status_code|body_code|message_regex|message_text;
            op: eq|contains|regex; value: string|number }
action {
  verdict: disable_key|disable_model|disable_provider|cooldown|ignore|retry_same|switch_next
  cooldown_seconds?: number        // verdict=cooldown
  recover: never|daily|fixed       // recover=daily → 次日固定时刻(默认12:00)恢复
  daily_reset_hour?: number
  switch_account?: boolean         // 禁 key 时同时整体禁用该 key
}
```

### 2.2 匹配顺序与 AI 兜底

1. priority 升序逐条匹配（首个命中胜出）；
2. 无规则命中 → 调用**指定的小模型**（用户在前端配置模型名，走网关自身
   `/v1/chat/completions`，复用 translate 插件的调用模式）分析错误 → 返回结构化动作；
3. AI 结果按动作立即执行（让本次请求能正确路由），同时生成草稿规则
   (source=ai, confirmed=false) 入库；
4. 草稿可在规则页确认（confirmed=true 转正）或删除；确认后同类错误直接走规则。

### 2.3 路由决策（替代现有硬编码策略）

失败后按规则动作执行，然后决策下一步：

| verdict          | 对 key(model_states) | 下一步                  |
|------------------|----------------------|------------------------|
| disable_key      | disabled(recover)    | 下一个 key/目标        |
| disable_model    | 该模型所有 key 禁用  | 下一个模型             |
| disable_provider | 整个渠道组禁用       | 下一个平台             |
| cooldown         | cooling(until)       | 下一个 key/目标        |
| ignore           | 不写状态             | 重试当前目标           |
| retry_same       | 不写                 | 重试当前目标(计次上限) |
| switch_next      | 不写                 | 直接下一个目标         |

所有目标穷尽后仍失败 → 502 返回最后错误（现状保持）。
每次失败/切换决策写入 route_attempts（metadata 记录 matched_rule / ai_decision），
前端路由日志可直接看到「规则路由」或「AI 路由」的判定依据。

### 2.4 数据库（migration 35）

```sql
CREATE TABLE failure_rules (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  source TEXT NOT NULL DEFAULT 'manual',      -- manual | ai
  confirmed INTEGER NOT NULL DEFAULT 1,       -- AI 草稿为 0
  priority INTEGER NOT NULL DEFAULT 100,
  provider_id TEXT DEFAULT '',                -- 渠道组（平台）过滤
  model TEXT DEFAULT '',                      -- 模型过滤
  match_json TEXT NOT NULL,                   -- {any|all:[{field,op,value}]}
  action_json TEXT NOT NULL,                  -- {verdict,cooldown_seconds,recover,...}
  hit_count INTEGER NOT NULL DEFAULT 0,       -- 命中统计
  last_hit_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX idx_failure_rules_priority ON failure_rules(priority, enabled);
CREATE TABLE rule_decisions (                            -- AI 判定日志
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  request_id TEXT, model TEXT, provider_id TEXT,
  status_code INTEGER, error_excerpt TEXT,
  matched_rule_id TEXT, matched_rule_name TEXT,
  ai_model TEXT, ai_raw TEXT, verdict TEXT,
  next_action TEXT, created_at TEXT NOT NULL
);
CREATE INDEX idx_rule_decisions_created ON rule_decisions(created_at DESC);
```

默认规则以硬编码种子（`plugins/failure-rules/defaults/*.go`）在迁移/启动时写入
（ON CONFLICT 不覆盖用户修改，与 web2api 种子策略一致）。

### 2.5 默认规则（种子，修复鞭尸的关键）

| # | 名称 | 匹配 | 动作 |
|---|------|------|------|
| 1 | 请求被客户端取消 | message_regex `context canceled` | ignore（不计失败不冷却） |
| 2 | 每日额度用尽(codebuddy) | body_code `14018` 或 额度已用尽 | disable_key + recover=daily |
| 3 | 余额不足(402) | status_code=402 | disable_key + recover=never |
| 4 | 无效API密钥 | status_code=401 | disable_key + recover=never（沿用现渠道连坐） |
| 5 | 限速(非额度) | status_code=429 且 body 无「额度/quota exhausted」 | cooldown 120s，连续 5 次升级为 daily |
| 6 | 服务过载 | status_code=503 / overloaded | cooldown 60s |
| 7 | 上下文超长 | context length/maximum context | switch_next（不禁用） |
| 8 | 网络超时 | timeout/connection reset | cooldown 30s |
| 9 | 模型不存在 | status_code=404 或 model not found | disable_model + recover=never |

现有 `shouldIgnoreFailure`、`failureState` 等硬编码逻辑迁移为规则引擎执行结果，
保持向后兼容语义。

## 三、实现拆解（TDD，每步先测试后实现）

### M1 数据层
- [ ] migration 35：failure_rules + rule_decisions（core/db/migrate.go）
- [ ] types：Rule/Condition/Action/Match（plugins/failure-rules/types.go）
- [ ] repo：CRUD + 种子写入（plugins/failure-rules/store.go）
### M2 引擎
- [ ] engine.go：优先级匹配、condition 求值、verify（对样本做 dry-run）
- [ ] action.go：verdict → model_states/channel_states 写入 + 冷却恢复计算
       （daily → 次日 daily_reset_hour；fixed → now+cooldown_seconds；never → NULL）
- [ ] ai.go：调用小模型生成候选规则/直接判定（prompt + JSON schema 解析 + 超时 30s + 失败回退默认 cooldown 120s）
- [ ] 集成点：aggregate.HandleProxyUpstreamFailed 改调 engine.Evaluate()，
      删除 analyzeProxyFailure/failureRules；model-health.RecordFailure 改为接收
      engine 裁决结果（或由 engine 直接写库），删除双轨分类
### M3 API
- [ ] admin-api 新增：GET/POST/PUT/DELETE /api/failure-rules、
      POST /api/failure-rules/verify（样本校验）、POST /api/failure-rules/{id}/confirm
      （AI 草稿转正）、GET /api/rule-decisions（判定日志列表）
- [ ] 插件注册：plugins/registry.go 增加 failure-rules；admin-api 路由挂载
### M4 前端（侧边栏新菜单「失败规则」，非设置页）
- [ ] router + 侧边栏入口（RulesView 新页面，参考 ModelStatusView 布局）
- [ ] 规则列表：启停开关、优先级、来源徽标（manual/ai/草稿）、命中统计、编辑/删除/确认
- [ ] 规则编辑弹窗：scope 选择（平台/模型）、条件行编辑（field/op/value）、动作配置
      （verdict/冷却秒数/每日恢复时刻）、校验提示
- [ ] verify 样本测试：输入 status_code/body/error → 展示命中结果（复用引擎 dry-run）
- [ ] AI 判定日志 tab：rule_decisions 列表（时间/模型/命中规则/AI 结论/动作）
### M5 清理与验证
- [ ] 移除 aggregate/strategy.go 硬编码表与 model-health 双轨分类（保留兼容测试改写）
- [ ] route_attempts metadata 写入 matched_rule/ai_decision；RouteLogDetail 展示
- [ ] go test ./... 全绿 + 前端 build 通过
- [ ] 运行时验证：模拟 14018 错误 → key 禁用至次日；context canceled → 不禁用

## 四、验收标准

1. 429+14018 → key 禁用到次日 12:00，期间绝不再被选中；
2. context canceled 不产生任何禁用/冷却；
3. 未匹配规则的新错误 → AI 判定并生成草稿规则，确认后下次直接命中；
4. 规则页可增删改查/启停/校验/排序，AI 草稿需人工确认才生效；
5. 路由日志可见每次失败的路由依据（规则名或 AI 结论）；
6. 现有测试全绿，无回归。

## 五、风险与兼容

- 迁移使用新版本号 35，不动历史 checksum；
- 引擎替换期间保留 model-health 原有接口签名（RecordFailure 内部改为规则驱动），
  volc-free-quota 的 free_quota_exhausted 特殊标记保持不冲突；
- AI 调用失败不影响主链路（回退默认 cooldown），无模型配置时跳过 AI 直接默认动作；
- 前端「失败规则」为一级菜单，不影响现有设置页。

## 六、审核修订（2026-09-19，子代理审核后采纳）

### 阻塞项处置

1. **恢复机制**：时间限定的禁用（daily/fixed）一律写 `status='cooling'` +
   `disabled_until`，复用 CheckNow 现有过期恢复语义；`status='disabled'`
   仅表示永久禁用（recover=never）。channel_states 不新增自动恢复（渠道连坐
   仅 auth 永久场景，与现状一致）。
2. **集成点**：引擎挂在 **model-health.RecordFailure 内部**（唯一公共咽喉，
   聚合/非聚合路径都经过），aggregate.HandleProxyUpstreamFailed 中的
   RecordFailure 直调删除（修复聚合路径 fail_count 双写 bug）。
   aggregate 侧只消费引擎返回的裁决决定下一步路由（追加/不追加 failed_targets）。
3. **向后兼容语义**：现有 shouldIgnoreFailure 的 ignore 行为全部转为内置
   ignore 种子规则（400/404/405 状态码、dial tcp/no such host/connection
   refused/eof/lookup 网络错误、客户端取消 context canceled）。
   404 不再新增 disable_model 规则（保持忽略，模型不存在另有 404+model
   not found 组合规则，需 all 条件同时命中才禁用）。
   timeout/connection reset 从忽略改为 cooldown 30s 是**有意变更**（原忽略
   导致瞬时网络抖动完全没有熔断保护），在规则名中注明。
4. **AI 递归防护**：
   - 引擎发起的 AI 请求带 `X-Loadout-Rule-AI: 1` header + metadata
     `__rule_ai` 标记，RecordFailure 求值时检测到标记直接跳过引擎（避免
     AI 请求失败再次进引擎）；
   - AI 模型 == 当前失败模型时跳过 AI（回退默认动作）；
   - 按错误指纹（status_code+body_code+错误前 200 字符 hash）singleflight
     在飞去重 + 5 分钟结果缓存；
   - AI 超时降到 8s，先执行默认动作保证本次路由，草稿规则异步生成。
5. **route_attempts metadata**：引擎只写 `pipe.Metadata["__rule_decision"]`
   （含 matched_rule/verdict/reason，<1KiB），gateway 的 proxyAttemptLog/
   proxyStreamAttempt 补 Metadata 字段赋值（修复 metadata_json 恒 '{}' 的
   缺陷），由现有日志路径带出；ai_raw 只入 rule_decisions 表。
   注意 route-log sensitive() 过滤与 4KiB metadata 上限。

### 建议项采纳

- **组身份**：规则的 provider 维度存 `base_url`（渠道组身份稳定），前端
  编辑时按 channel_name 展示、提交时解析为 base_url；引擎求值时
  channelID→base_url 联表映射（store 层缓存）。
- **种子规则**：确定性 id（`seed-001` ~ `seed-012`）+ INSERT OR IGNORE，
  用户删除后不复活（hit 记录由应用层维护 deleted_seeds 内存态不可靠，
  改为删除时写 action_json 特殊标记？否——直接物理删除，种子只在新库首次
  迁移时写入一次，之后不做 upsert）。
  修正：种子写入放在 migration 35 的 SQL 中（INSERT OR IGNORE），应用启动
  不再重复写；用户修改种子行不受影响（仅新库初始化）。
- **AI 模型配置**：settings 表加列 `rule_ai_model TEXT NOT NULL DEFAULT ''`
  （migration 35 一并加），空 = AI 兜底关闭走默认动作；SK key 缺失同样降级。
- **verdict 行为矩阵**（M2 实现依据）：

| verdict | 聚合路径下一步 | 非聚合路径下一步 | failed_targets |
|---------|---------------|-----------------|----------------|
| cooldown/disable_* | 换下一个目标 | 换下一个候选渠道 | 追加 |
| ignore/retry_same | 重试当前目标（上限 __retry_count>=10） | 重试当前渠道一次 | 不追加 |
| switch_next | 换下一个目标 | 换下一个候选渠道 | 追加 |

- **内部路由错误**：no-candidates（"没有可用渠道支持模型"）是网关自身
  错误，RecordFailure 入口先识别（error 文本前缀匹配）直接 return，
  不写状态不计失败（修复 98/100 次 fail_count 刷数 bug）。
- **幽灵模型防护**：引擎写 model_states 前复刻 catalogAllows 检查；
  写库用 context.WithoutCancel（客户端取消不影响状态落库）。
- **fail_count 升级**：model_states.fail_count 复用为连续失败计数，
  RecordSuccess 清零；规则 5（限速）动作带 fail_count>=5 时自动升级
  为 daily（action_json.fail_upgrade_count=5）。
- **依赖方向**：failure-rules 作为包内引擎（plugins/failure-rules），
  model-health 依赖它（注入引擎实例），引擎直写 DB 不反向依赖
  model-health，无循环依赖；registry.go 无需新插件条目。
- **AI 路由折叠**：AI 内部请求打 `__sub_request` metadata 标记，
  前端请求日志可识别折叠（M5 可选）。
