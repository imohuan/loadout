# 「模型状态」页：恢复语义与 UI 改造方案

> 状态：**已实现**（2026-10-02）
> 范围：`frontend/src/views/ModelStatusView.vue`、`model-status/ModelStatusKeyList.vue`、`model-status/ModelStatusModelTable.vue`；后端 `plugins/model-health/service.go`、`plugins/admin-api/routing.go`、`frontend/src/composables/useModelStatus.ts`。

## 一、现状（三层结构）

| 层 | 是什么 | 数据来源 | 状态表 |
|---|---|---|---|
| 平台 | 同一个 `base_url` 的一组 Key | 前端按 base_url 分组 | 无独立表（Key 的聚合视图）|
| Key | 一条渠道记录（`Channel`） | `channels` | `channel_states`（Key 级熔断）|
| 模型 | Key 下的一个模型 | `channel_models` | `model_states`（模型级熔断）|

**两种「坏掉」互相独立**：

- **自动熔断**：系统按失败规则自动写（Key 级或模型级），到期或有条件地自愈。
- **手动开关**：用户自己拨的（`manual_enabled`），自动逻辑永不改动它。

## 二、用户指出的三个问题

1. **左侧 Key 选中无高亮**：`ModelStatusKeyList.vue` 选中态是 `border-border bg-background`，和面板底色相同，等于看不出选中。
2. **平台级没有恢复入口**：平台详情页只能恢复「当前 Key」，无法一次恢复整个平台下的所有 Key。
3. **按钮重复**：右栏 Key 头部有「恢复 Key」（自动熔断时出现），表格工具栏又有一个「恢复 Key」，两处重复。

## 三、命名重设计

问题根因：现在的名字混用了两种语义维度 —— **范围**（Key / 平台 / 全部）和 **强度**（只清熔断 / 连手动开关一起开）。

用一套统一命名：**「恢复」只清自动熔断，绝不碰手动开关**；破坏性操作单独命名并从主区收进「更多」菜单。

| 旧名 | 新名 | 范围 | 实际动作 |
|---|---|---|---|
| 恢复 Key | **恢复 Key** | 当前 Key | 清该 Key 的自动熔断（Key 级 + 它下面所有模型级）|
| （新增）| **恢复本平台** | 当前平台全部 Key | 对平台内每个 Key 执行「恢复 Key」|
| 全平台恢复渠道 | **恢复全部平台** | 所有平台 | 对所有平台执行「恢复本平台」|
| 恢复全部异常 | **强制开启本 Key** | 当前 Key | 清熔断 **并强制打开手动开关**（破坏性）|
| 全平台恢复全部异常 | **强制开启全部** | 所有平台 | 同上，全局范围（破坏性）|

> 「恢复」三个按钮语义完全一致、只是范围不同；「强制开启」放「更多 ▾」菜单并保留强化确认框（会覆盖你主动关闭的开关）。
> 不用「恢复渠道」这个名字：本仓库里 `Channel` 指的是「一条 Key」，用「渠道」称呼平台会和代码语义打架。

## 四、UI 改造

### 4.1 面包屑右侧：平台级操作区（新增）

```
< 平台总览 / workbuddy            [恢复本平台]  [更多 ▾]          [表格|标签]
```

- `恢复本平台`：一键清该平台下所有 Key 的自动熔断。
- `更多 ▾`：`强制开启本平台全部模型`（破坏性）。

### 4.2 右栏 Key 头部：只留一个恢复按钮

- 保留头部的「恢复 Key」（仅当该 Key 自动熔断时出现）。
- **删除表格工具栏里的重复「恢复 Key」**，只保留「恢复本平台」的入口在上方。

### 4.3 表格工具栏（清理后）

```
9 / 10 个模型可用   <原因>            [关闭全部] [开启全部]   [全选 反选 清空]
```

（`恢复全部异常` 移入平台级「更多」菜单）

### 4.4 左侧 Key 列表选中高亮

- 选中行：`bg-primary/10` + 左侧 2px 主色竖条（`border-l-2 border-primary`）+ Key 名加粗。
- 未选中行保持透明底 + 悬停浅底，对比明显。

### 4.5 全局操作（PageHeader）

```
[刷新] [健康检查]  [恢复全部平台]  [更多 ▾]
```

`更多 ▾` → `强制开启全部`。

## 五、后端改动

1. **修 `RecoverChannel` 的语义缺口**（此前定位的 bug）：「恢复 Key」除清 `channel_states` 外，同时清该 Key 下所有 `model_states` 的自动熔断；不碰 `manual_enabled`。
2. **新增平台级接口**：`POST /api/model-status/platforms/recover`，body `{ base_url }`；后端按 base_url 找出该组全部 Key，逐个执行「恢复 Key」。
3. 新增 `POST /api/model-status/platforms/recover-forced`（强制开启本平台）与全局对应接口（或在现有 `recover-all` 上扩展）。

## 六、验证

- 单测：`RecoverChannel` 必须同时清 Key 级与模型级；平台级恢复对组内每个 Key 生效。
- 前端：`pnpm test` + `vue-tsc` 构建通过；左栏高亮、按钮去重、平台级恢复逐个手测。

## 七、落地结果（2026-10-02）

后端：

- `RecoverChannel` 改为清「Key 级 + 模型级」两层自动熔断（新增 `recoverChannels` 共用实现）；
  新增 `RecoverPlatformByBaseURL` / `RecoverAllModelsByBaseURL`；`RecoverAllChannels`
  同步改为两层一起清。破坏性动作仍只由 `RecoverAllModels*` 承担（强制打开手动开关）。
- 新接口：`POST /api/model-status/platforms/recover`、`POST /api/model-status/platforms/recover-forced`。

前端：

- 左栏选中行：主色底 + 左侧竖条 + 加粗。
- 面包屑右侧加平台级操作区「恢复本平台 / 更多 ▾」。
- 删除表格工具栏里与 Key 头部重复的「恢复 Key」。
- 顶部改名「恢复全部平台」，破坏性「强制开启全部模型」收进「更多 ▾」。

测试：

- 后端新增 `recover_scope_test.go`、`recover_platform_test.go`、`recover_all_channels_test.go`、
  `plugins/aggregate/recover_returns_first_target_test.go`（4 组「挂掉 → 恢复 → 回到第一个目标」用例）。
- `go test ./...` 全绿（apps/server 一条 temp-dir 清理 flake 与本改动无关，单独重跑通过）；
  `go vet ./...` 干净；前端 `pnpm test` 50 项通过、`pnpm build`（含 vue-tsc）通过。
