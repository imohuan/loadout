# 火山免费额度：多资源包聚合判据 + 后台定时刷新重构（rev2，含审计修订）

> 用户决策（2026-08-23）：
> 1. **聚合判据统一用 `local_remaining`**（本地扣减余额为准，billing available 只做首次建底数与恢复检测）。
> 2. **火山免费资源包额度会恢复，但时间不固定**（有的 8 点恢复、有的 11 点左右、最晚 11:30，不是 0 点）——"冷却至次日 0 点"与"已耗尽不复活"都是错的，必须改为「定时刷新检测到 billing 恢复 → 复活余额 + 解除禁用」。

> rev2 修订（sub-agent 审计后）：
> - B：local_remaining 校准改"只减不增 + 跨天恢复"——billing 上升一律视为滞后，只有**跨天（昨天或更早扣完）+ billing available>0** 才算真恢复；下降时用 billing 校准（防本地漏扣）。
> - C：多 quota model 映射同一 API 模型（如 deepseek-v4-flash / deepseek-v4-flash-0731 → deepseek-v4-flash-ga-260731）必须**按 API 模型维度先合并 SUM 再统一判定**，否则同循环随机 disable/re-enable。
> - E：**去掉 debounce，改纯 ticker 无条件定时刷**（in-flight 锁防并发）——持续请求时 debounce 永不触发导致额度恢复永不检测。滞后数据由"只减不增 + 跨天恢复"保护，不致命。
> - F：disableModelForFreeQuota 去掉 fail_count 累加（免费额度不是健康失败，冷却次数无意义，避免无限增长）。
> - H：decrement 路径复用 syncModelStatesByAggregate，不搞两套 disable 逻辑。

> rev4 修订（最终 code review 后）：
> - **P1 修复**：聚合/扣减/拦截三处 SQL 追加到期过滤 `activePackageCond`（`expiry_time = '' OR expiry_time >= now`，RFC3339 字符串比较，实测全非空且格式统一）。原因：billing 客户端只拉 Effective/UsedUp，refreshOne 从不把本地行标 Expired——运行期到期/消失的包残留正余额会永久撑住聚合（既不判耗尽、扣减还吃幽灵余额）。状态过滤只对存量旧行生效，expiry_time 过滤才是运行期防线。
> - re-enable **限定冷却来源**：只解 `last_failure_class='free_quota_exhausted' OR last_error='模型免费额度用完'`，不碰 model-health 的 upstream_error/5xx 冷却——无条件解除会与 model-health 互搏振荡（解除→再冷却→再解除，且抹掉 fail_count 计数）。force_block=1（实际部署）下拦截在请求到上游前就写 free_quota_exhausted，upstream_error 不会出现；force_block=0 场景靠 12:00 兜底 + model-health markSuccess 自愈，不需要 re-enable 解除 upstream_error。
> - 已知不改（记录）：①<45 万日额度模型的中间态死锁（聚合恒落 (0,45万] → 冷却阻断流量 → 无扣减 → 不复用）——由 12:00 兜底 CheckNow 覆盖（实测恢复 8~11:30 < 12:00），用户场景不存在；②Refresh（refreshOne 写余额）与 decrement（请求扣减）并发 read-modify-write 竞态是 v15 历史遗留，修复需动热路径事务结构，本期不做。
> - P2 修复：synced_at 解析失败保守视为"今天"（不触发跨天复活）；oldRows.Err() 检查；sentinel error `errRefreshInFlight`（HandleRefresh errors.Is → 409）；`VolcQuotaMinRemaining` 声明值 200000 → 0 与 Load 默认一致（消除误导）；前端文案同步。
> - 测试补：45 万±1 边界（449999 保持冷却 / 450000 中间态 / 450001 恢复 + disabled_until=NULL）、过期包排除（inst-expired）、upstream_error 不解除、manual_enabled=0 保护。

## 现状（已核实，含 DB 实测）

**DB 实测（`~/.loadout/loadout.db`，账号 b37eb7a58eac46e2，83 个包）**：6 个模型存在"死包 + 活包"并存：

| model | 包数 | 耗尽包 | 活包 | 聚合 local_remaining |
|---|---|---|---|---|
| deepseek-v4-flash-0731 | 7 | 5 | 2 | 2,016,100 |
| deepseek-v4-pro-0813 | 5 | 3 | 2 | 2,203,199 |
| glm-5-2 | 3 | 2 | 1 | 1,998,146 |
| doubao-seed-2-0-pro / 2-1-pro / 2-0-code | 3 | 1 | 2 | 各 200 万+ |

**误判位置（只看单个包，未聚合）**：
- `service.go:820-845` `refreshOne`：`availAmt := parseAmount(p.AvailableAmount); if availAmt > 0 { continue }` → 单包 `available=0` 即 disable 整个 API 模型。deepseek-v4-flash-0731 有 5 个 UsedUp 包，每次刷新都被误禁用。
- `service.go:247-249` `decrementLocalRemaining`：`if after == 0 && m.before > 0 { syncModelStatesExhausted(...) }` → 单包扣到 0 即禁用模型，且**未按 API 模型合并多 quota model**（deepseek-v4-flash 扣光但 deepseek-v4-flash-0731 还有 201 万时仍会误禁用）。

**已正确（不改）**：`checkAllCandidatesExhausted`（service.go:468-484）已聚合 `accountRemaining[aid] += local_remaining`，有回归测试。注意它按 `matchOne(quotaModel, requestModel)` 逐包匹配累加，天然等价"按 API 模型聚合"（requestModel 是同一个），口径正确。

**后台刷新问题**：
- `scheduleRefresh`（service.go:112-126）debounce：仅请求成功时重置 timer，**无请求则永不刷远程**；持续请求时 timer 被持续重置也**永不刷**——额度恢复永远检测不到。
- `StartBackgroundRefresh` runNow=true 同步执行 Refresh（plugin.go:96），阻塞启动（拉 billing 最多 10 页×2 状态，8 QPS 节流）。
- Refresh 无 in-flight 互斥：手动 + 定时可能并发拉同一账号（billing 双 QPS 429、local_remaining 互相覆盖）。

**复活逻辑缺失**：`refreshOne` UPSERT（service.go:890-895）`WHEN volc_quota_packages.local_remaining <= 0 THEN 0`——本地耗尽后 billing 恢复 available>0 也不复活，模型永久禁用。

**cooldown 时间错误**：`untilNextDayMidnightLocal`（service.go:1237-1246）冷却到次日 0 点；实际恢复时间 8:00~11:30。

**fail_count 无限增长**：`disableModelForFreeQuota`（service.go:1227）`fail_count=model_states.fail_count+1`——拦截/刷新反复写冷却时 fail_count 每次 +1，长期运行无限增长。

## 设计

### 1. 新增聚合函数（service.go）

```go
// aggregateLocalRemaining 返回该账号下每个 model 的聚合本地余额（SUM(local_remaining)）。
// 一个模型可能挂多个资源包（billing 按 InstanceNo 逐条返回），判断必须按 model 聚合。
// 口径与 checkAllCandidatesExhausted / decrementLocalRemaining 一致：
//   - 只统计 initial_total > 0（有扣减基准的包）
//   - 排除已过期/失效/退款等非活动状态的包（避免残留余额长期撑住聚合判据）
func (s *Service) aggregateLocalRemaining(accountID string) map[string]int64

// modelExhausted 该账号下某 quota model 聚合余额是否 <= 最低保留阈值。
func (s *Service) modelExhausted(accountID, quotaModel string) bool
```

SQL：`SELECT model, SUM(local_remaining) FROM volc_quota_packages WHERE account_id = ? AND initial_total > 0 AND status NOT IN ('Expired','NotEffective','FailedToCreate','Refunded') GROUP BY model`

### 2. syncModelStatesByAggregate：按 API 模型聚合的双向同步（唯一判定入口）

**核心原则（用户确认）："用完"与"恢复"的判定一律走聚合后的数据，不做单包判定。** 包级账本（§4）只负责记数，不参与"模型可用与否"的裁决；syncModelStatesByAggregate 是唯一判定入口（refreshOne 与 decrementLocalRemaining 统一调用，**废除 syncModelStatesExhausted / disableCandidatesForQuota 的独立判断逻辑**，后者只保留"写状态"职责）。

```go
// syncModelStatesByAggregate 按账号聚合余额三态同步 model_states：
//   - 聚合 SUM(local_remaining) <= minRemaining（默认 0）→ 写冷却（用完）
//   - SUM > config.VolcQuotaReviveThreshold（默认 45 万）→ 解除免费额度冷却（恢复）
//   - 中间态（0 < SUM <= 45 万）→ 保持不动（小额残留不算恢复，也不重复禁用）
// 关键：多 quota model（如 deepseek-v4-flash / deepseek-v4-flash-0731）可能映射到
// 同一 API 模型（deepseek-v4-flash-ga-260731），必须按 API 模型合并 SUM 后统一判定，
// 否则同循环内先 re-enable 后 disable 随机抖动（map 无序迭代）。
func (s *Service) syncModelStatesByAggregate(ctx context.Context, accountID string) (disabled []string) {
    channelIDs := s.channelsForAccount(accountID)
    if len(channelIDs) == 0 { return nil }
    apiModels, err := s.channelsAPIModels(ctx, channelIDs)
    if err != nil || len(apiModels) == 0 { return nil }
    agg := s.aggregateLocalRemaining(accountID)
    // 按 API 模型合并：apiModel -> 所有匹配 quota model 的聚合和
    apiAgg := make(map[string]int64)
    for quotaModel, sum := range agg {
        for _, m := range s.matchQuotaToAPIModels(quotaModel, apiModels) {
            apiAgg[m] += sum
        }
    }
    minRemaining := int64(config.VolcQuotaMinRemaining)
    reviveThreshold := int64(config.VolcQuotaReviveThreshold)
    disabledSet := make(map[string]struct{})
    for m, sum := range apiAgg {
        switch {
        case sum <= minRemaining: // 用完（聚合判据）
            for _, chID := range channelIDs {
                s.disableModelForFreeQuota(ctx, chID, m, untilNextDayRecovery())
            }
            disabledSet[m] = struct{}{}
        case sum > reviveMin: // 恢复（聚合判据，>45 万才解除禁用）
            for _, chID := range channelIDs {
                s.reEnableModelForFreeQuota(ctx, chID, m)
            }
        // 中间态（0 < sum <= 45 万）：不动，避免状态摇摆
        }
    }
    return disabledSorted(disabledSet) // 现有 helper（service.go:909），map→排序 []string，稳定输出
}
```

**refreshOne 接线（重要）**：删除现有单包禁用循环（service.go:818-845，`availAmt > 0 { continue }` 判据 + `untilNextDayMidnightLocal()`），UPSERT 循环保留；UPSERT 完成后调用 `disabled, _ := s.syncModelStatesByAggregate(ctx, aid)`，把返回值作为 `RefreshResult.DisabledModels`（替代原 `disabledSorted(disabled)`，service.go:905）。**必须删除旧循环**，否则同一刷新内旧误判（单包 available=0）与新聚合判据双写冲突。

**checkAllCandidatesExhausted（before-upstream 拦截）口径对齐**：现有逻辑已按 `matchOne(quotaModel, requestModel)` 聚合 `accountRemaining[aid] += local_remaining`（正确，等价按 API 模型聚合），但 SQL（service.go:472）**未排除过期包**，与 aggregateLocalRemaining 口径不一致——过期包残留余额会撑住聚合值导致拦截失效。补状态过滤：

```sql
SELECT account_id, model, initial_total, local_remaining FROM volc_quota_packages
WHERE account_id IN (...) AND initial_total > 0
  AND status NOT IN ('Expired','NotEffective','FailedToCreate','Refunded')
```

### 3. 新增 re-enable（解除免费额度冷却）

**判定依据（rev3 最终决策，code review 修订）**：聚合 > 45 万 = 额度恢复的硬证据，解除该模型的**免费额度冷却**。**限定冷却来源**：只解 `last_failure_class='free_quota_exhausted' OR last_error='模型免费额度用完'`——不碰 model-health 因真实上游故障（upstream_error/5xx）写的冷却，避免"解除→再冷却→再解除"振荡且抹掉其 fail_count。force_block=1（实际部署）下拦截发生在请求到上游前（写 free_quota_exhausted），upstream_error 不会出现；force_block=0 场景靠 12:00 兜底 CheckNow + model-health markSuccess 自愈。

```go
// reEnableModelForFreeQuota 额度恢复（聚合>阈值）后解除该模型的免费额度冷却：
//  - 只解 last_failure_class='free_quota_exhausted'（或旧数据 last_error 匹配），
//    不碰 model-health 的真实故障冷却（upstream_error/5xx）——防互搏振荡
//  - 不碰用户手动禁用（manual_enabled=0 行不动）
//  - fail_count 清零：额度恢复后旧冷却计数无意义（disableModelForFreeQuota 已不再累加）
func (s *Service) reEnableModelForFreeQuota(ctx context.Context, channelID, model string) error {
    now := time.Now().UTC().Format(time.RFC3339Nano)
    _, err := s.db.ExecContext(ctx, `
        UPDATE model_states SET status='available', disabled_until=NULL, last_error='', last_failure_class='', fail_count=0, updated_at=?
        WHERE channel_id=? AND model=? AND manual_enabled=1 AND status != 'available'
          AND (last_failure_class='free_quota_exhausted' OR last_error='模型免费额度用完')`,
        now, channelID, model)
    return err
}
```

### 4. local_remaining 校准：包级账本只记数，不判定（rev2，含 45 万恢复门槛）

**边界划分（用户确认）**：
- **包级 `computeLocalRemaining` 只做账本**：跟随 billing 记数（billing 有值就写），**不做"模型是否恢复"的裁决**。
- **恢复/用完判定一律在聚合层**（§2 syncModelStatesByAggregate）：聚合 SUM 用 `config.VolcQuotaReviveThreshold`（默认 45 万）判恢复、用 `minRemaining`（默认 0）判用完。
- 理由：50 万日额度可能分散在多个包（30 万 + 20 万），若 45 万门槛放在单包级会漏判（单包都不超 45 万 → 永不恢复）。

**新增业务常量（用户确认，2026-08-23，rev2 审计 H 修订：config 单源，删掉代码内 const 防双源漂移）**：火山免费额度**每天恢复 50 万**，恢复判定要求**聚合额度 > 45 万**（排除结算滞后的残留小额——昨天剩几万时 billing 也会显示，但那不是"今日恢复"）。

config.go 新增（intEnv 模式，config.go:250 已有先例）：

```go
// VolcQuotaReviveThreshold 免费额度恢复判定门槛：每日恢复 50 万，聚合余额必须超过
// 该值（默认 45 万）才判定"今日已恢复"并解除禁用。env: LOADOUT_VOLC_QUOTA_REVIVE_THRESHOLD
var VolcQuotaReviveThreshold = 450000 // intEnv("LOADOUT_VOLC_QUOTA_REVIVE_THRESHOLD", 450000)
```

**抽成纯函数** `computeLocalRemaining(oldInitial, oldLocal, oldStatus, oldSyncedAt, avail, now) int64`：

```go
// 逻辑（纯账本，不做模型级裁决）：
//  1. 首次写入（oldInitial == 0）→ = avail（建底数）
//  2. 已耗尽（oldLocal <= 0）：
//       - 扣减发生在今天（sameLocalDay(oldSyncedAt, now)）→ 保持 0（billing 可能滞后，不记）
//       - 扣减发生在昨天或更早（跨天）且 avail > 0 → = avail（billing 有值即记上；
//         是否"算恢复"由聚合层 45 万门槛裁决）
//       - 否则 → 保持 0
//  3. avail < oldLocal → = avail（billing 权威下降校准，防本地漏扣）
//  4. 其余（billing 上升，含结算滞后回升）→ 保持 oldLocal（本地扣减为准，防回弹）
```

**UPSERT 调用处**（refreshOne 的 SQL，service.go:868-896）把现有三分支 CASE 替换为 `?` 传参（newLocal 由 Go 算好），或保留 SQL 但分支改为：

```sql
local_remaining = CASE
    WHEN volc_quota_packages.initial_total = 0 THEN excluded.local_remaining
    WHEN volc_quota_packages.local_remaining <= 0 AND substr(volc_quota_packages.synced_at,1,10) = ? AND excluded.available_amount > 0 THEN 0
    WHEN volc_quota_packages.local_remaining <= 0 AND excluded.available_amount > 0 THEN excluded.available_amount
    WHEN excluded.available_amount < volc_quota_packages.local_remaining THEN excluded.available_amount
    ELSE volc_quota_packages.local_remaining
END
```

**⚠️ 关键修复（rev2 审计 B，P0）**：现有 UPSERT `synced_at = excluded.synced_at`（service.go:896）**每次刷新都把 synced_at 覆盖为"本次刷新时间"**。ticker 每 15 分钟刷一次 → synced_at 恒为今天 → 跨天判定 `substr(synced_at,1,10) = 今天` 恒真 → 永远记 0 → **模型永不复活，rev2 核心目标失效**。必须改为**仅首次写入设置，刷新保留**：

```sql
synced_at = CASE
    WHEN volc_quota_packages.initial_total = 0 THEN excluded.synced_at  -- 首次写入（建底数）
    ELSE volc_quota_packages.synced_at                                   -- 刷新保留；只有扣减（decrementLocalRemaining service.go:229）才更新
END
```

语义：`synced_at` = "最近一次本地扣减时间"（或首次建底时间），刷新绝不触碰。跨天判定依赖它，不可被刷新污染。

> 说明：`synced_at` 复用记录"最近一次本地扣减时间"（service.go:229 已有此语义）。**跨天判定用北京时间**（用户明确要求）：
> - 常量 `beijingTZ = time.FixedZone("Asia/Shanghai", 8*3600)`（中国无夏令时，固定 +8 零依赖，Windows 不依赖系统 tzdata）
> - 把 `synced_at`（UTC RFC3339Nano）`Parse(...).In(beijingTZ)` 取北京日期，与 `time.Now().In(beijingTZ)` 的今天日期比较
> - **推荐走纯函数 + 参数传值**，SQL 分支留在代码里可单测（审计 G）。

### 5. decrementLocalRemaining 改聚合判据（复用 §2）

扣减循环不变（按包分摊扣减）。**扣减查询补状态过滤**（审计 C，service.go:181）：

```sql
-- 现状：WHERE account_id = ? AND initial_total > 0（会扣 Expired 包）
-- 改为与聚合同口径：排除非活动状态
SELECT instance_no, model, configuration_name, local_remaining FROM volc_quota_packages
WHERE account_id = ? AND initial_total > 0
  AND status NOT IN ('Expired','NotEffective','FailedToCreate','Refunded')
ORDER BY local_remaining DESC
```

循环后**统一调用 syncModelStatesByAggregate**（按 API 模型聚合，天然覆盖多 quota model 映射）：

```go
// 循环内：删除 if after == 0 && m.before > 0 { syncModelStatesExhausted(...) } 整段（service.go:247-248）
// 循环后（无论本次是否扣到 0，都做一次聚合同步；幂等，SQLite 快）：
s.syncModelStatesByAggregate(context.Background(), accountID)
```

> 每次请求成功多一次聚合查询 + 至多 N 次 UPSERT/UPDATE（幂等，状态无变化时 UPDATE 影响 0 行），热路径可接受。

### 6. 后台刷新重构：纯 ticker + in-flight 锁（rev2，去 debounce）

Service 新增字段：

```go
bgMu       sync.Mutex
lastRefresh time.Time   // 最近一次成功刷新时间（仅用于日志/观测，不参与调度）
refreshing bool         // in-flight 标记
bgCancel   context.CancelFunc
```

**StartBackgroundRefresh**（语义：纯定时，无条件每 interval 刷一次）：
- runNow=true → `go s.safeRefresh()`（**异步，不阻塞启动**）
- 启动 ticker goroutine：每 interval → safeRefresh；`select` 监听 bgCancel.Done() 退出
- **删除 scheduleRefresh / refreshTimer / refreshInterval**（service.go:161 是唯一调用点，HandleProxyUpstreamSucceeded 里的 scheduleRefresh() 调用一并删，确认无其他引用）
- Disposer：`cancel()`（停 ticker goroutine）即可；safeRefresh 内部 Refresh 用 context.Background() 不受影响

**Refresh 入口加 in-flight 互斥**（手动 HTTP 与后台共用）：

```go
func (s *Service) Refresh(ctx context.Context, channelID string) (RefreshResult, error) {
    s.bgMu.Lock()
    if s.refreshing {
        s.bgMu.Unlock()
        return result, errors.New("刷新进行中，请稍后再试")
    }
    s.refreshing = true
    s.bgMu.Unlock()
    defer func() { s.bgMu.Lock(); s.refreshing = false; s.bgMu.Unlock() }()
    ...现有逻辑...
    // 成功路径末尾（有账号实际刷新成功）更新 lastRefresh
}
```

**safeRefresh**：
```go
func (s *Service) safeRefresh() {
    if _, err := s.Refresh(context.Background(), ""); err != nil {
        s.lg.Warn("volc-free-quota: 后台刷新失败", "err", err)
    }
}
```
（并发由 Refresh 内 refreshing 锁保证，safeRefresh 无需额外锁；ticker 下周期自动重试。）

### 7. fail_count 去累加（service.go:1227）

`disableModelForFreeQuota` 的 UPSERT 删掉 `fail_count=model_states.fail_count+1`（免费额度不是健康失败，冷却次数无意义且无限增长）。

### 7b. 拦截路径冷却时间统一（审计 G）

`disableCandidatesForQuota`（before-upstream 拦截写冷却，service.go:359/374）目前用 `untilNextDayMidnightLocal()` 作 disabled_until，需一并改为 `untilNextDayRecovery()`（与 §2 disableModelForFreeQuota 调用一致）。该函数保留"纯写状态"职责，判断逻辑不在此处。

### 8. cooldown 时间：`untilNextDayMidnightLocal` → `untilNextDayRecovery`

```go
// beijingTZ 固定 +8 时区（中国无夏令时，零依赖，Windows 不依赖系统 tzdata）
var beijingTZ = time.FixedZone("Asia/Shanghai", 8*3600)

// untilNextDayRecovery 返回额度恢复兜底时间：次日 12:00（北京时间）。
// 实测恢复时间不固定（8:00~11:30），12:00 留 30min 缓冲；正常由 15min 定时刷新
// 检测 billing 恢复后提前 re-enable，此值只是刷新全挂时的兜底。
// 实现：now.In(beijingTZ) 取北京日期，下一天 12:00 转回 time.Time 返回。
func untilNextDayRecovery() time.Time
```

> 交互说明（审计 F）：次日 12:00 CheckNow 自动恢复 available 时，若 billing 仍未恢复（聚合=0），before-upstream 会拦截。因 fail_count 不再累加 + 拦截路径同步 model_states（幂等写同一冷却），不会无限增长；下次刷新检测到恢复即解除。抖动窗口最多半天，可接受。

### 9. 前端文案（VolcQuotaCard.vue:359）

"额度耗尽后自动禁用该模型（冷却至次日 0 点，错误信息「模型免费额度用完」）"
→ "额度耗尽后自动禁用该模型（每 15 分钟检测恢复，错误信息「模型免费额度用完」）"

## 测试

1. **TestAggregateLocalRemaining**：多包求和、initial_total=0 排除、Expired 状态排除、无包返回空。
2. **TestModelExhausted**：聚合 ≤ 阈值 true / > false。
3. **TestComputeLocalRemaining**（纯函数，账本级）：
   - 首次（oldInitial=0）→ avail
   - 今天扣完（oldLocal=0, syncedAt=今天, avail=500000）→ 保持 0（滞后不记）
   - 昨天扣完（oldLocal=0, syncedAt=昨天, avail=30000 > 0）→ 30000（账本跟随 billing；是否算恢复由聚合层裁决）
   - 昨天扣完 + avail=0 → 保持 0
   - avail < oldLocal（正常消耗下降）→ avail
   - avail > oldLocal（滞后回升）→ 保持 oldLocal
4. **TestSyncModelStatesByAggregate**（聚合判定层，三态）：
   - 同 model 多包聚合耗尽（SUM<=0）→ disable 对应 API 模型（如 deepseek-v4-flash-0731 → deepseek-v4-flash-ga-260731）
   - **多 quota model 映射同一 API 模型**（deepseek-v4-flash + deepseek-v4-flash-0731 → 同一 API 模型）：合并 SUM 判定，不抖动
   - **50 万分散多包**（30 万 + 20 万）：单包都不超 45 万，聚合 = 50 万 > 45 万 → re-enable（防漏判）
   - 聚合 SUM=30 万（0 < 30 万 <= 45 万）中间态 → 不动（已冷却的保持冷却，可用的保持可用）
   - re-enable 覆盖：free_quota_exhausted 冷却解除（disabled_until=NULL、fail_count 清零）；**upstream_error（真实故障）不被解除**（限定来源）；manual_enabled=0 行不动
   - **45 万边界**：449999 保持冷却 / 450000 中间态不动 / 450001 恢复
5. **TestRefreshOneSyncedAtPreserved**（审计 B，P0 回归）：首次 UPSERT 后 synced_at=刷新时间；**第二次刷新（跨天）后 synced_at 仍为首次值**（不被刷新覆盖）；decrementLocalRemaining 扣减后 synced_at=扣减时间；随后刷新 synced_at 不变。
6. **TestDecrementSkipsExpired**（审计 C）：Expired 状态包不参与扣减、不被聚合、不被分摊。
7. **TestComputeLocalRemaining**（纯函数，账本级）：
   - 首次（oldInitial=0）→ avail
   - 今天扣完（oldLocal=0, syncedAt=今天, avail=500000）→ 保持 0（滞后不记）
   - 昨天扣完（oldLocal=0, syncedAt=昨天, avail=30000 > 0）→ 30000（账本跟随 billing；是否算恢复由聚合层裁决）
   - 昨天扣完 + avail=0 → 保持 0
   - avail < oldLocal（正常消耗下降）→ avail
   - avail > oldLocal（滞后回升）→ 保持 oldLocal
   - **北京时间跨天边界**：syncedAt = 今天 23:59 / 00:01（北京时间）两例，验证"今天/昨天"切分正确（beijingTZ 转换）
8. **TestUntilNextDayRecovery**：次日 12:00（北京时间）、晚于当前、距当前 ≤ 48h。
9. **TestRefreshInFlight**：并发调用 Refresh，第二个返回"刷新进行中"（注入刷新耗时或直接断言锁状态）；HTTP 手动刷新在 ticker 占用时返回 409 而非 500。
10. **TestDisableModelForFreeQuota**：重复调用 fail_count 不增长、manual_enabled 不被覆盖、disabled_until=untilNextDayRecovery。
11. 回归：现有 TestDecrementLocalRemainingSharesAcrossPackages / TestCheckAllCandidatesExhausted* 全部保持通过（decrement 扣减逻辑不变，仅禁用触发改为聚合版）。
12. 构建验证：`go build ./...` + `go vet ./plugins/volc-free-quota/...` + 前端 `vue-tsc` + `vite build`。

## 风险

- **跨天恢复的日期边界**：跨天判定统一走 `beijingTZ` 固定 +8 时区（北京时间），synced_at 存 UTC 但比较前转北京日期，无时差问题。边界：若某包恰好在北京时间 23:59 扣光、次日 00:01 额度恢复，判定为"跨天" → 立即复活（正确）；若今天扣光后当天 billing 又短暂恢复（罕见，billing 反向修正），会被判"不复活"保持 0，下次刷新再评——最多延迟 15 分钟，可接受。
- **re-enable 放宽的风险**（rev2 审计 F）：聚合 >45 万即解除 manual_enabled=1 的任何冷却（不限来源）。误解除场景：上游真实故障冷却的模型，额度恢复时被解除 → model-health 检测到故障会重新冷却，自愈。收益：避免"网络故障期间 12:00 兜底恢复 → 上游报错 upstream_error → 额度恢复后永不解除"的卡死。
- **synced_at 语义（rev2 审计 B，P0）**：UPSERT 只首次写 synced_at，刷新保留，仅扣减更新。任何新代码不得在刷新路径触碰 synced_at，否则跨天判定失效（模型永不复活）。
- **45 万门槛的适用性**：恢复判定要求聚合 > 45 万（每日 50 万额度）。一次性协作奖励包（200 万）用完时 billing available=0，聚合不会虚高；若火山调整免费额度（如 50 万→100 万），阈值失效——已做成 config 变量 `LOADOUT_VOLC_QUOTA_REVIVE_THRESHOLD` 可配。
- **中间态状态保持**：聚合 SUM 落在 (0, 45 万] 时不动——已禁用的保持禁用（小额残留不算恢复），可用的保持可用（还有余额不算耗尽）。边界情况：若某模型长期只恢复小额（<45 万）且已禁用，会一直禁用直到聚合超 45 万；正常每日恢复 50 万不触发。
- **checkAllCandidatesExhausted 与聚合判定的一致性**：拦截 SQL 补状态过滤后，与 syncModelStatesByAggregate 同口径（同批包、同判据），不会出现"刷新说耗尽、拦截说有余量"或反向矛盾。
- **re-enable 误解除**：限定 `last_failure_class='free_quota_exhausted' OR last_error='模型免费额度用完'` + `manual_enabled=1`，不碰用户手动禁用与上游报错冷却。
- **拦截路径与刷新双写**：before-upstream 拦截仍调用 disableCandidatesForQuota（写冷却，幂等，fail_count 不再增长）；刷新 re-enable 与之互斥（状态由最后写入者决定，但双方判据同源=聚合余额，不会长期打架）。
- **纯 ticker 每 15min 拉 billing**：请求活动期间拉到的可能是结算中数据，但"只减不增 + 跨天恢复"保证不误判；billing 8 QPS 限流下 15min 一次无压力。
- **Refresh 失败不更新 lastRefresh**：ticker 下周期重试（interval 节流，不无限重试）。

## 优先级

| 级别 | 内容 |
|---|---|
| P0 | 聚合判据（§1/§2/§3/§5）+ 复活逻辑（§4）——误禁用与永不恢复 |
| P0 | synced_at 刷新覆盖修复（§4）——否则跨天判定失效，核心目标不成立 |
| P0 | refreshOne 旧禁用循环删除接线 + disabledSorted 类型修正（§2）——否则编译错/双写冲突 |
| P0 | 纯 ticker 去 debounce（§6）——额度恢复永不检测 |
| P1 | 按 API 模型合并 SUM（§2）+ re-enable 放宽覆盖 model-health 冷却（§3）+ fail_count 去累加（§7）+ 拦截路径冷却时间统一（§7b）+ cooldown 12:00 北京时间（§8）+ 前端文案（§9） |
| P1 | decrement/拦截 SQL 状态过滤对齐（§5/§2） |
| P2 | 测试补齐（含 synced_at 保留回归、过期包扣减、跨天边界、并发锁超时、类覆盖交互） |
