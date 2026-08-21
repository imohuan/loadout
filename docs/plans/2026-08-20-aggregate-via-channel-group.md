# 聚合模型 + 视觉候选：支持「渠道级」与「Key 多选」路由

> 状态：已实施（2026-08-20，方案 A+：渠道级 + Key 多选并存，UI 沿用 ModelTestView 分组 tag 风格）
> 上承：`2026-08-19-channel-multi-key.md` 第 7 节「第二期（本期不做）」第一项
> 目标：把"渠道（base_url 组）"和"具体 Key（多选）"两个粒度同时支持到底层，避免聚合模型编辑下拉展示混乱、视觉候选手动逐 Key 列出

---

## 1. 决策

| 决策点 | 结论 |
|---|---|
| 设计 | **同一套抽象复用两处**：聚合模型 `AggregateTarget` 与视觉候选 `ViaOption` 都升级为 `{ model, channel_base_url?, channel_ids[]? }`（兼容旧 `channel_id` 单值） |
| 后端执行 | **统一工具** `expandCandidateKeys(target, channels, ctx) []ResolvedKey`，聚合 `selectAvailableTarget` 与视觉 `Describe` 都用它——把 target 展开为有序候选 Key 列表，逐个健康检查 + 模型支持检查 |
| 模型支持差异 | **天然处理**：视觉/聚合执行都先经 `model-gateway.resolveChannels`（已按 `channel.Models` 过滤，模型不在 Key 列表里的 Key 直接被跳过），目标内候选 Key 是"支持该模型"的子集 |
| 老数据 | 零迁移。`channel_id` 非空的旧目标保持 Key 级精确路由，行为完全不变 |
| **不动** | ModelTestView（用户显式声明"只作参考"） |

---

## 2. 数据模型扩展

### 2.1 `plugins/types/types.go`

```go
type AggregateTarget struct {
    Model          string   `json:"model"`
    ChannelID      string   `json:"channel_id,omitempty"`       // 兼容：单 Key
    ChannelIDs     []string `json:"channel_ids,omitempty"`      // Key 多选（空 = 不按 Key 多选）
    ChannelBaseURL string   `json:"channel_base_url,omitempty"` // 渠道级：按 base_url 组轮询 Key
}

type ViaOption struct {
    ViaModel       string   `json:"via_model"`
    ChannelID      string   `json:"channel_id,omitempty"`       // 兼容：单 Key
    ChannelIDs     []string `json:"channel_ids,omitempty"`      // 视觉候选 Key 多选
    ChannelBaseURL string   `json:"channel_base_url,omitempty"` // 视觉候选渠道级
}
```

`core/db/repository.go` 的 `AggregateTarget` struct 同步加字段：
```go
type AggregateTarget struct {
    Position       int      `json:"position"`
    Model          string   `json:"model"`
    ChannelID      string   `json:"channel_id"`
    ChannelIDs     []string `json:"channel_ids"`     // JSON 数组列 channel_ids_json
    ChannelBaseURL string   `json:"channel_base_url"`
}
```

### 2.2 数据库迁移

`core/db/migrate.go` 加 migration 8：

```sql
ALTER TABLE aggregate_targets RENAME TO aggregate_targets_old;
CREATE TABLE aggregate_targets (
  aggregate_id INTEGER NOT NULL REFERENCES aggregates(id) ON DELETE CASCADE,
  position INTEGER NOT NULL,
  model TEXT NOT NULL,
  channel_id TEXT REFERENCES channels(id),         -- 不再 NOT NULL
  channel_ids_json TEXT NOT NULL DEFAULT '[]',     -- Key 多选
  channel_base_url TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (aggregate_id, position)
);
INSERT INTO aggregate_targets (aggregate_id, position, model, channel_id, channel_ids_json, channel_base_url)
  SELECT aggregate_id, position, model, channel_id, '[]', '' FROM aggregate_targets_old;
DROP TABLE aggregate_targets_old;
```

> `capability_routes` 表的 `via_options_json` 已经是 JSON 列，**无新迁移**——只要 `admin_repository.RepReplaceCapabilityRoutes` 写入的 JSON 多两个键即可。

校验规则（`ReplaceAggregates`）：
- `model` 必填
- `channel_id` / `channel_base_url` / `channel_ids` 三者至少一个非空

---

## 3. 后端实现

### 3.1 共享工具：候选 Key 展开

文件：`plugins/model-gateway/expand.go`（新建）或挂在现有 `service.go`

```go
// ResolvedKey target 展开后的单个候选 Key。
type ResolvedKey struct {
    ChannelID  string
    BaseURL    string   // 归一化（去尾斜杠）
    Name       string   // Key name（用于日志）
}

// ExpandCandidateKeys 把聚合 target / 视觉候选展开为有序的候选 Key 列表。
//   优先级：channel_base_url > channel_ids > channel_id（兼容）
//   渠道级：从 channels 找出同 base_url 的 ManualEnabled Key，按原序返回
//   Key 多选 / 单 Key：按声明顺序，保留未启用的 Key 也返回（让上层做健康检查）
func ExpandCandidateKeys(targetKey string, targetIDs []string, targetBaseURL string,
    channels []db.Channel) []ResolvedKey {
    byID := make(map[string]db.Channel, len(channels))
    for _, ch := range channels {
        byID[ch.ID] = ch
    }
    if targetBaseURL != "" {
        var out []ResolvedKey
        targetBaseURL = normalizeBaseURL(targetBaseURL)
        for _, ch := range channels {
            if !ch.ManualEnabled { continue }
            if normalizeBaseURL(ch.BaseURL) == targetBaseURL {
                out = append(out, ResolvedKey{ChannelID: ch.ID, BaseURL: ch.BaseURL, Name: ch.Name})
            }
        }
        return out
    }
    var out []ResolvedKey
    seen := map[string]bool{}
    for _, id := range append(targetIDs, []string{targetKey}...) {
        if id == "" || seen[id] { continue }
        if ch, ok := byID[id]; ok {
            out = append(out, ResolvedKey{ChannelID: ch.ID, BaseURL: ch.BaseURL, Name: ch.Name})
            seen[id] = true
        }
    }
    return out
}

func normalizeBaseURL(s string) string { return strings.TrimRight(s, "/") }
```

### 3.2 聚合 `selectAvailableTarget` 重写

文件：`plugins/aggregate/health.go`

把"以 target 为粒度的健康检查"改为"以 target 展开后的候选 Key 为粒度"：

```go
func (s *Service) selectAvailableTarget(targets []types.AggregateTarget, failedKeys []string) (*types.AggregateTarget, *types.AggregateTarget, error) {
    // 返回 (选中 target, 选中 Key，对应 ResolveKey)；如无返回 nil, nil
    channels, err := s.loadChannels(context.Background()) // s.routing.ListChannels 或 st.Read(FileChannels)
    if err != nil { return nil, nil, err }
    for i := range targets {
        t := &targets[i]
        candidates := modelgateway.ExpandCandidateKeys(t.ChannelID, t.ChannelIDs, t.ChannelBaseURL, channels)
        for _, k := range candidates {
            mkey := fmt.Sprintf("%s@%s", t.Model, k.ChannelID)
            if contains(failedKeys, mkey) { continue }
            ok, _ := s.health.Check(context.Background(), k.ChannelID, t.Model)
            if ok && ok.EffectiveAvailable {
                return t, &types.AggregateTarget{Model: t.Model, ChannelID: k.ChannelID, ChannelBaseURL: t.ChannelBaseURL, ChannelIDs: t.ChannelIDs}, nil
            }
        }
    }
    return nil, nil, nil
}
```

> 注意：`HandleBeforeUpstream` / `HandleProxyBeforeUpstream` 用返回的"展开 target"写 metadata——**用具体 KeyID 写 `__current_channel`**，组内逐 Key failover 由 `model-gateway.proxyForward` 的 `for _, ch := range candidates` 天然完成（不再设置 `__channel_base_url`，避免三层过滤冗余）。

### 3.3 模型网关：候选过滤升级

文件：`plugins/model-gateway/service.go` `resolveChannels`

加 metadata 字段支持：

```go
func (s *Service) resolveChannels(ctx, model, metadata) ([]ResolvedChannel, error) {
    channels := s.routing.ListChannels(ctx)
    specified := ""
    specifiedIDs := []string{}
    specifiedBaseURL := ""
    if metadata != nil {
        specified, _ = metadata["__current_channel"].(string)
        if v, ok := metadata["__current_channel_ids"].([]string); ok { specifiedIDs = v }
        specifiedBaseURL, _ = metadata["__current_channel_base_url"].(string)
    }
    var out []ResolvedChannel
    for _, ch := range channels {
        if !ch.ManualEnabled { continue }
        switch {
        case specified != "" && ch.ID != specified:
            continue
        case len(specifiedIDs) > 0 && !slices.Contains(specifiedIDs, ch.ID):
            continue
        case specifiedBaseURL != "" && normalizeBaseURL(ch.BaseURL) != normalizeBaseURL(specifiedBaseURL):
            continue
        }
        // ... 健康检查 + 模型过滤 + 解密 + ResolvedChannel 同现状
    }
    return out, nil
}
```

文件：`plugins/model-gateway/proxy.go` proxyForward（约 174 行）

展开过滤块：

```go
// 渠道级 / Key 多选 / 单 Key 三档优先级（Key 单值保留向后兼容）
if baseURL, ok := pipe.Metadata["__current_channel_base_url"].(string); ok && baseURL != "" {
    filtered := candidates[:0]
    for _, ch := range candidates {
        if normalizeBaseURL(ch.BaseURL) == normalizeBaseURL(baseURL) {
            filtered = append(filtered, ch)
        }
    }
    if len(filtered) > 0 { candidates = filtered }
}
if idsAny, ok := pipe.Metadata["__current_channel_ids"]; ok {
    if ids, _ := idsAny.([]string); len(ids) > 0 {
        filtered := candidates[:0]
        for _, ch := range candidates {
            for _, id := range ids {
                if ch.ID == id { filtered = append(filtered, ch); break }
            }
        }
        if len(filtered) > 0 { candidates = filtered }
    }
}
if specified, ok := pipe.Metadata["__current_channel"].(string); ok && specified != "" {
    filtered := candidates[:0]
    for _, ch := range candidates { if ch.ID == specified { filtered = append(filtered, ch) } }
    if len(filtered) > 0 { candidates = filtered }
}
```

### 3.4 视觉候选：扩展 ViaOption + 内部展开

文件：`plugins/vision/proxy.go`（约 312 行 `for idx, opt := range options`）

视觉与聚合差异：视觉的 `Describe` 是直接调上游（不走 model-gateway proxyForward）。需视觉自身做"循环 Key"。

```go
// 旧：Describe(ctx, newGroup.images, viaModel, opt.ChannelID, streamWriter)
// 新：把 opt.ChannelIDs/ChannelBaseURL 展开为候选 Key 列表，逐 Key 调用 Describe，任一成功退出
candidates := modelgateway.ExpandCandidateKeys(opt.ChannelID, opt.ChannelIDs, opt.ChannelBaseURL, channels)
var newText string
var chID string
for _, k := range candidates {
    newText, chID, err = s.Describe(context.Background(), newGroup.images, viaModel, k.ChannelID, streamWriter)
    if err == nil { break }
}
// 全部候选都失败 → continue 到下一 via_option
```

> 视觉当前已经从 `route.ViaOptions` 拿到 Options，需要在 `s.findCapabilityRoute` 之后注入 channels（已有 `channels []types.Channel`，vision service 里可从 `ctx` 或注入得）。

视觉端 `Describe` 接口不变，**只在 vision 内部展开**，对外接口稳定（视觉插件的 RecordFailure 通过 `chID` 写入 model-health，自动按 Key 维度积累）。

### 3.5 admin-api：导入导出

文件：`plugins/admin-api/transfer.go`

```go
type exportAggregate struct {
    ...
    Targets []types.AggregateTarget `json:"targets"`
}
// 已有，无需改 struct。AggregateTarget 加字段后自动导出。
```

`ReplaceCapabilityRoutes` 写入 via_options_json 时带 `channel_base_url` / `channel_ids` 即可（JSON 列透明）。

---

## 4. 前端实现

### 4.1 类型

`frontend/src/lib/types.ts`：

```ts
export interface AggregateTarget {
  model: string
  channel_id?: string
  channel_ids?: string[]
  channel_base_url?: string
}

export interface ViaOption {
  via_model: string
  channel_id?: string
  channel_ids?: string[]
  channel_base_url?: string
}

export interface ModelChannelItem {
  model: string
  channel_id?: string        // 兼容（保留，老数据）
  channel_ids?: string[]     // Key 多选
  channel_base_url?: string  // 渠道级
}
```

### 4.2 `ModelChannelList.vue` 升级（核心组件）

**设计原则**：沿用 `ModelTestView.vue:524-610` 现有 Popover + tag 网格 + 按 base_url 分组的风格。ModelTestView 是单选（点击 tag 即选该 Key），ModelChannelList 改为**多选**（toggle tag 不关闭 popover）——同一套视觉语言、同一份 `groupChannelsByBaseURL` 工具，零新增组件。

**新结构**（每行目标 = 1 个 Popover 触发按钮 + 1 个 tag 网格弹层）：

```
┌─────────────────────────────────┐
│ 模型: [claude-haiku ▼]  │ 渠道: [volcengine（3 Key 轮询） ▼]  │ ↑ ↓ × │
└─────────────────────────────────┘
       ↓ 展开 Popover
┌─────────────────────────────────┐
│ 自动路由（按模型找渠道）        │
│                                 │
│ Loadout 自带 API                 │
│ [new1*] [d*]                    │
│                                 │
│ NewAPI · 1 个 Key               │
│ [newapi]                        │
│                                 │
│ 像素星空 · 3 个 Key              │
│ [volcengine*] [claude2*] [gpt*]│
│                                 │
│ 火山 · 2 个 Key                  │
│ [k1] [k2]                       │
│                                 │
│ (* = 已勾选；组内全勾时组标题高亮) │
└─────────────────────────────────┘
```

**关键行为**（与 4.6 触发器按钮文本联动）：

- 每行 item 用**归一化数据** `normalized: { auto: bool, groups: Array<{baseUrl, title, keys: Array<{id, name, checked}>>} }` 在内部存所有候选 Key 的勾选态
- 保存（emit `update:modelValue`）时**统一规范化为后端字段**：
  - `auto = true` → `channel_id: ''`，其他空
  - 任一组**全部 Key 已勾** → `channel_base_url = 该组 baseUrl`，其他空（**UI 高亮整组，值是渠道**）
  - 部分勾 → `channel_ids = 所有勾选 Key 的 id`，`channel_base_url = ''`
  - 都没勾 → 不 emit 跳过
- 加载（解析 channel_id / channel_ids / channel_base_url）时反向：
  - `channel_base_url` 非空 → 该组所有 Key 标 checked
  - `channel_ids[]` 非空 → 这些 Key 标 checked（跨组也支持，例如"newapi 的 Key1 + volcengine 的 Key2"）
  - `channel_id`（老兼容）单值 → 该 Key checked
  - 空 → `auto = true`（若 allowAutoChannel）

**Popover 沿用 ModelTestView 524-610 风格**：`<Popover><PopoverTrigger as-child><Button>...</Button></PopoverTrigger><PopoverContent align="start">...</PopoverContent></Popover>`，弹层内用 `<div v-for="group in groups">` 渲染组标题 + tag 按钮网格。

**模型候选并集**（同步重构 `modelCandidates`，避免"选了渠道但下拉没候选"）：
- `channel_base_url` 非空：组内所有 Key 的启用 models **并集**
- `channel_ids[]` 非空：所选 Key 的启用 models 并集
- `channel_id`（兼容）：单 Key 的 models
- 空 / `auto`：全部 Key 的 models 并集（现状）

**新增 prop**：
```ts
{
  showIndex?: boolean       // 已有
  addLabel?: string         // 已有
  requireChannelForModel: true   // 默认开：渠道/Key 必选才能选模型
  allowAutoChannel: true        // 默认开：保留「自动路由」入口
  // 新增：
  multiSelect?: boolean         // 默认 true（聚合/视觉候选都多选）
  enableChannelLevel?: boolean  // 默认 true：允许整组全勾时折叠为 channel_base_url
}
```

> **不引入"粒度切换"控件**。`channel_base_url` vs `channel_ids` 是后端存储形态，UI 不暴露——用户看到的只是"勾了哪几个 Key"。这与 4.6 触发器文本、4.5/4.7 提交转换保持单一逻辑。

### 4.3 `useChannels.ts`：导出分组工具

已在 8-19 plan 实现 `groupChannelsByBaseURL`，无需重写。**新增** base_url 归一化供粒度判定用：

```ts
export function normalizeBaseURL(url: string) { return url.replace(/\/+$/, '') }
```

### 4.4 模型候选并集

`ModelChannelList.vue` `modelCandidates(channelId)` 增加一个版本 `modelCandidatesForTarget(item)`：

- 渠道级（`item.channel_base_url`）：从 `channels` 找同 base_url 的所有 Key，启用 models 并集
- Key 多选（`item.channel_ids` 数组）：所选 Key 的启用 models 并集
- 单 Key（`item.channel_id`）：该 Key 的 models（现状）
- 空：全部渠道 models 并集（保持现状）

### 4.5 `AggregateEditor.vue` 适配

- 表单 `targets` 初始化为 `[{ model: '', channel_id: '', channel_ids: [], channel_base_url: '' }]`
- watch `props.aggregate` 形态映射（含老数据：`channel_id` 单值 → `channel_ids: [channel_id]`，ModelChannelList 内部 normalize 会反向处理）
- 提交时依赖 ModelChannelList emit 的字段形态直接透传给后端
- `requireChannelForModel: true`、`allowCustomModel: false`（Key 多选仍要求至少选一项，渠道级任一 Key 启用即 OK——靠 require-channel 保障）
- 不需要 `targetItemShape` prop：视觉候选同形态提交，只是字段名 `via_model` / `channel_id` 在 4.7 转换

### 4.6 `AggregateTable.vue` 显示修复

`channelName(channels, id)` 删掉，换为 `resolveTargetDisplay(target)`：

| 形态 | 显示 |
|---|---|
| `target.channel_base_url` 非空 | 找该 base_url 组第一个 Key 得 base_url，显示**渠道名**（组内首 Key 的 `channel_name` 或 `name`）+ `（N 个 Key 轮询）` |
| `target.channel_ids[]` 非空 | 列出所选 Key 的 `name`，同组用 `、`，跨组用 `；`（如 `volcengine 的 key1、key2；newapi 的 key3`） |
| `target.channel_id`（老兼容） | 原 Key `name`（保持现状） |

### 4.7 `CapabilityRouteEditor.vue` 视觉候选适配

视觉候选的 viaOptions 已用 ModelChannelList 渲染（v-model form.viaOptions），**只换组件内部字段形态**：

- 表单初始化 `viaOptions: [{ model: '', channel_id: '', channel_ids: [], channel_base_url: '' }]`
- watch `props.route` 形态映射（含老 viaOption.channel_id）
- submit 转换 `via_options`：ModelChannelList emit 出的字段直接映射到 `types.ViaOption` 的 `via_model` / `channel_id` / `channel_ids` / `channel_base_url`

---

## 5. 任务分解（执行顺序）

1. **计划文档**（本文件）
2. **DB schema**：`migrate.go` migration 8 + `repository.go` 读写新列 + `ReplaceAggregates` 校验
3. **共享工具**：`plugins/model-gateway/expand.go` `ExpandCandidateKeys` + 单测（含渠道级/多选/单值/空）
4. **聚合改造**：`aggregate/health.go selectAvailableTarget` 改用 `ExpandCandidateKeys`；`aggregate/service.go` / `aggregate/proxy.go` metadata 写法更新
5. **模型网关**：`model-gateway/service.go resolveChannels` + `model-gateway/proxy.go proxyForward` 候选过滤升级
6. **视觉候选**：`vision/proxy.go` 视觉循环用 `ExpandCandidateKeys`；vision 单测覆盖
7. **admin-api**：`transfer.go` 导入导出（Mostly 自动）
8. **前端类型**：`types.ts` AggregateTarget / ViaOption / ModelChannelItem 新增字段
9. **前端 ModelChannelList 升级**：粒度切换 + 渠道下拉 + Key 多选弹层 + 模型并集
10. **前端 AggregateEditor / AggregateTable 适配**
11. **前端 CapabilityRouteEditor 视觉候选适配**
12. **全量验证**：`go test ./...`、`vue-tsc`、`vite build`、`go build ./...`；更新 docs/API.md（如果变更 API）

---

## 6. 测试

| 层 | 用例 |
|---|---|
| model-gateway | `TestExpandCandidateKeys`：渠道级 / Key 多选 / 单值 / 空 / base_url 归一化 / 重复去重 |
| aggregate | `TestSelectAvailableTargetChannelLevel`：组内一个 Key 冷却、一个 Key 启用 → 跳过冷却选启用；组内全部不可用 → 该 target 跳过；组内全部可用 → 按顺序选首 Key |
| aggregate | `TestSelectAvailableTargetMultiKey`：channel_ids 顺序保持；failedKeys 命中某 Key → 跳过 |
| model-gateway | `TestResolveChannelsWithBaseURL`：metadata 含 `__current_channel_base_url` 时只保留同组 Key |
| model-gateway | `TestResolveChannelsWithChannelIDs`：metadata 含多个 id 时只保留所选 Key |
| vision | `TestVisionViaChannelLevel`：viaOption 渠道级，组内 key0 失败 → 自动切 key1 成功 |
| vision | `TestVisionViaMultiKey`：viaOption Key 多选，按顺序轮询 |
| 前端 | vue-tsc 通过；vite build 通过；UI 手动：聚合编辑保存-刷新、视觉编辑保存-刷新 |

---

## 7. 风险

- **migration 8 重建表**：外键 ON 状态下 `ALTER TABLE RENAME` + `CREATE TABLE` 在 SQLite 事务内合法；外键约束（`REFERENCES aggregates(id)` `aggregate_id`、`REFERENCES channels(id)` `channel_id`）原有数据均满足，迁移完成保留旧数据完整性
- **视觉插件 Describe 重试**：视觉当前对每个 ViaOption 调一次 Describe（单 Key）。改造后单 via_option 内会循环尝试多个 Key——视觉 step_no 占用需调整：单 via_option 内部多次 Describe 不暴露为多个 step，只算 1 步；多次 via_option 跨步才算 step_num++
- **`@` 显示**：截图里 `claude-haiku-4-5-20251001 @ newapi` 中的 `newapi` 是 channel_name 还是 name 仍存疑——老数据 name = "newapi"，channel_name 为空；前端显示要回退 `channel_name || name`，并标 `「组内 N 个 Key 轮询」`让用户看清粒度
- **`channelIDs` 是 Key 多选还是 BaseURL 列表**：明确——**Key 多选**（每个元素是 channel.id），不是 base_url 列表；与 channel_base_url 语义不重叠
