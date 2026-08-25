# 渠道多 Key(多账号)实施计划

> 状态：一期已实施（2026-08-19，方案 B：不改表结构，UI 按 base_url 分组折叠）；二期事项见 §7
> 目标：一个渠道(base_url)支持多个 API Key(多账号)，每个 Key 模型目录/健康状态/费用禁用独立，UI 按 base_url 分组折叠展示。

---

## 1. 需求确认（已与用户对齐）

| 决策点 | 结论 |
|---|---|
| 存储方式 | **方案 B：一行 channel 记录 = 一个 Key（账号）**，不改任何表结构；"渠道(base_url)"是纯 UI 分组概念 |
| Key 模型独立 | 每条记录自己探测 `channel_models`，天然独立（channel_id 维度已存在） |
| Key 健康独立 | `model_states`/`channel_states` 按 channel_id 记，天然 key 级（无需加 key_id 列） |
| 费用不足 | 402 → 该记录（key）channel_states disabled，**不做渠道连坐**；`sync_billing` 连坐语义废弃，字段保留兼容 |
| 401/403(auth) | **禁整条 key 记录**（现状只禁模型，语义错位 → 修正） |
| 429 / 超时 / 5xx | 冷却（现状逻辑，key 级天然成立） |
| 失败重试 | 普通请求只换 key/换渠道，**不换模型**；仅聚合模型切模型（ProxyUpstreamFailed 事件，上限 10 次） |
| UI 形态 | **表格折叠展开**，参考 McpPanel.vue 折叠模式；父行 = base_url 组，展开行 = 组内 Key 列表 |
| 组移动 | 新增 `POST /api/channels/reorder`（全量 id 顺序重排），前端整组移动后提交全量顺序 |

---

## 2. 方案总览

数据模型零改动，一行 `channels` = 一个 Key（账号）。同 base_url 的记录在 UI 归为"渠道组"：

```
channels 表（原样）
├─ id=c1  name="主账号"    base_url=https://api.deepseek.com/v1  api_key_cipher=AES(key1)
├─ id=c2  name="备用账号"  base_url=https://api.deepseek.com/v1  api_key_cipher=AES(key2)
├─ id=c3  name="OpenAI"    base_url=https://api.openai.com/v1    api_key_cipher=AES(key3)
```

- **后端改动 2 处**：
  1. `model-health`：auth(401/403) 除禁模型外，同步置该记录 `channel_states` disabled（禁 key）。
  2. `admin-api`：新增 `POST /api/channels/reorder`（全量 id 顺序重排 position），支撑 UI 整组移动。
- **前端改动**：ChannelTable 按 base_url 分组折叠；ChannelsView 组操作（删除组/刷新组/移动组/添加 Key）；ChannelEditor 支持"添加 Key"模式（base_url 锁定）；useChannels 加 reorder 与分组工具。
- **天然成立、零改动**：key 级模型目录、key 级健康、402 禁 key、多 key 顺序 failover、仅聚合切模型。

---

## 3. 后端改动

### 3.1 model-health：auth 错误禁整个 key 记录

文件：`plugins/model-health/service.go`（`RecordFailure`，约 L142-170）

现状：`failureState` 中 auth/model_quota → model_states disabled（只禁该模型）。
改动：`class == "auth"` 时，额外把该 channel 的 `channel_states` 置 disabled（禁 key 记录）。

```go
// RecordFailure 内，写入 model_states 之后：
if class == "auth" {
    // key 无效（过期/被删）：禁整条 key 记录，不连坐渠道
    _, err := s.db.ExecContext(ctx, `INSERT INTO channel_states(channel_id, status, disabled_until, fail_count, last_error, last_failure_class, last_checked_at, updated_at)
        VALUES (?, 'disabled', NULL, 1, ?, ?, ?, ?)
        ON CONFLICT(channel_id) DO UPDATE SET status='disabled', disabled_until=NULL, fail_count=channel_states.fail_count+1, last_error=excluded.last_error, last_failure_class=excluded.last_failure_class, last_checked_at=excluded.last_checked_at, updated_at=excluded.updated_at`,
        failure.ChannelID, redact(failure.Error), class, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
    if err != nil {
        return class, fmt.Errorf("model-health: record channel failure: %w", err)
    }
}
```

注意：`channel_billing` 分支现有 `sync_billing` 条件——多 key 语义下 402 已按 key 级禁用（model_states），**不再需要 sync_billing 连坐**，该分支保留兼容但不扩展。

### 3.2 admin-api：reorder 端点

文件：`plugins/admin-api/routing.go`

新增：

```go
// handleChannelsReorderDB 全量重排渠道顺序：按提交的 id 顺序设置 position。
func (s *Service) handleChannelsReorderDB(w http.ResponseWriter, r *http.Request) {
    var input struct {
        IDs []string `json:"ids"`
    }
    if !decodeJSON(w, r, &input) {
        return
    }
    channels, err := s.listDBChannels(r.Context())
    if err != nil {
        s.writeServerError(w, err)
        return
    }
    byID := make(map[string]int, len(channels))
    for i := range channels {
        byID[channels[i].ID] = i
    }
    out := make([]db.Channel, 0, len(channels))
    seen := make(map[string]bool, len(input.IDs))
    for _, id := range input.IDs {
        idx, ok := byID[id]
        if !ok || seen[id] {
            continue // 未知/重复 id 忽略，不报错（前端可能带旧数据）
        }
        seen[id] = true
        out = append(out, channels[idx])
    }
    for i := range channels {
        if !seen[channels[i].ID] {
            out = append(out, channels[i]) // 未提交的记录保持原顺序追加
        }
    }
    if err := s.routing.ReplaceChannels(r.Context(), out); err != nil {
        s.writeServerError(w, err)
        return
    }
    writeJSON(w, http.StatusOK, channelAPIList(out))
}
```

路由注册（service.go `Routes()` 中，`/channels` 组内）：
```go
mux.HandleFunc("POST /api/channels/reorder", s.handleChannelsReorderDB)
```

---

## 4. 前端改动

### 4.1 useChannels.ts：reorder + 分组工具

```ts
const reorder = (ids: string[]) => request<void>('/api/channels/reorder', 'POST', { ids })
```

分组纯函数（组 = 同 base_url，组内按 position 即数组序）：
```ts
export interface ChannelGroup {
  baseUrl: string
  keys: Channel[]
}
export function groupChannelsByBaseURL(channels: Channel[]): ChannelGroup[] {
  const groups = new Map<string, Channel[]>()
  for (const ch of channels) {
    const list = groups.get(ch.base_url) || []
    list.push(ch)
    groups.set(ch.base_url, list)
  }
  return [...groups.entries()].map(([baseUrl, keys]) => ({ baseUrl, keys }))
}
```

### 4.2 ChannelTable.vue：分组折叠表格（重写）

参考 McpPanel 上游服务器 tab 的折叠模式：

- 状态：`expandedGroups = ref<string[]>([])` + `toggleGroup(baseUrl)` / `isGroupExpanded(baseUrl)`
- **父行**（每 base_url 一组）：`[展开箭头] | base_url(font-mono,主标识) | Key 数 badge | 模型数汇总 | 费用同步(任一组内开启即"开启") | 操作(刷新全部/上移/下移/编辑组/删除组)`
- **展开行**：`<TableRow v-if="isGroupExpanded(group.baseUrl)" class="bg-muted/30">` → `<TableCell :colspan>` 内嵌面板：
  - 面板头：`"Key 列表"` + `N 个 Key` badge + 右侧 `+ 添加 Key` 按钮
  - `divide-y rounded-md border bg-background` 内每行一个 key：左侧 `name` + 模型数 + 费用同步 badge；右侧 `[测试] [刷新模型] [编辑] [启用/禁用] [删除]`
- 事件上抛：`refreshGroup(baseUrl)` / `moveGroup(baseUrl, direction)` / `removeGroup(baseUrl)` / `addKey(baseUrl)` / `editKey(channel)` / `refreshKey(channel)` / `removeKey(channel)` / `toggleKey(channel)` / `testKey(channel)`

### 4.3 ChannelsView.vue：组操作适配

- `save(input)`：保留现有合并模型逻辑（组内新增 key = 新渠道创建）
- `removeGroup(baseUrl)`：确认后批量删除组内全部记录（循环 remove）
- `refreshGroup(baseUrl)`：循环 refreshModels
- `moveGroup(baseUrl, direction)`：重排全量 channels 数组（组为整体）→ `reorder(ids)`
- `addKey(baseUrl)`：打开 ChannelEditor，`baseUrl` 锁定为组 base_url，只填 name + api_key
- `editKey(channel)`：现有编辑

### 4.4 ChannelEditor.vue：添加 Key 模式

新增 prop `lockBaseUrl?: string`：非空时 base_url 输入框禁用（隐藏并显示组 base_url），保存时带固定 base_url。

---

## 5. 测试计划

| 层 | 用例 |
|---|---|
| model-health | `TestRecordFailureAuthDisablesChannel`：RecordFailure(401) 后 Check(channel, model) 返回 EffectiveAvailable=false 且 reason 含 "disabled"；429/402 不触发渠道禁用 |
| admin-api | `TestChannelsReorder`：乱序提交 ids → ListChannels 顺序与提交一致；未提交记录追加在尾部；重复/未知 id 忽略不报错 |
| 前端 | vue-tsc 通过；vite build 通过 |

---

## 6. 任务分解（执行顺序）

1. **计划文档**（本文件）
2. **model-health auth 禁 key**：TDD（先写失败测试 → 实现 → 跑测试）→ commit
3. **admin-api reorder 端点**：TDD → commit
4. **useChannels reorder + 分组工具**（纯函数 + API）
5. **ChannelTable 分组折叠重写**
6. **ChannelsView / ChannelEditor 组操作适配**
7. **全量验证**：`go test ./...`、`vue-tsc`、`go build ./...`；更新 `docs/API.md` 记录 reorder 端点

---

## 7. 第二期（本期不做）

- 能力路由/聚合模型/概览页的渠道选择器按 base_url 分组展示（本期选择器会列出同 base_url 多条记录，功能可用但展示需优化）
- 组内 key 手动排序（本期按 position 自然序）
- "渠道级配置"若未来需要（限速/余额查询/共享开关），再迁移到 channel_keys 父子表（方案 A），迁移逻辑 = 现有分组逻辑
