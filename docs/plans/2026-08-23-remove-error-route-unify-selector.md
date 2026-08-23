# 删除 error 路由方式 + 统一能力路由选择函数

> 用户决策：`error` 路由方式没意义（"不支持就不管他"），整体删除（后端常量 + 前端 UI）；同时把各插件的路由选择策略抽象成 types 包通用函数。

## 现状（已核实）

**RouteError 引用（后端 Go）**：
- `plugins/types/types.go:92` 常量定义
- `plugins/vision_v2/rewrite.go:122` — vision_v2 命中 error 报"模型不支持视觉能力"
- `plugins/vision/service.go:351` + `plugins/vision/proxy.go:299` — 旧 vision（已废弃，同一逻辑）
- `plugins/sensitive-filter/service.go:171` — sensitive-filter 命中 error 直接拒绝请求（内容安全拦截）

**选择策略差异（本次要统一的）**：
- sensitive / field / request-log：native/error 优先短路，proxy 全部收集
- vision_v2：第一个命中就返回，不区分 native/proxy（依赖 position 排序，是 bug 隐患）

**前端引用**：
- `CapabilityRouteEditor.vue:262` — sensitive_filter 的 routeOptions 含 `{value:'error', label:'命中拒绝'}`
- `CapabilityRouteEditor.vue:313` — submit 校验 `form.route === 'error'`
- `CapabilityRouteTable.vue:49/54` — routeLabel/routeVariant 含 error（'拒绝'/'destructive'）
- `frontend/src/lib/types.ts:233` — route 类型 `'native' | 'proxy' | 'error' | string`

## 设计

### 1. types 包：删常量 + 新增通用选择函数

`plugins/types/types.go`：
- 删 `RouteError = "error"` 常量（保留 `RouteNative`/`RouteProxy`）
- 新增：

```go
// SelectCapabilityRoutes 按能力路由表选择命中的路由：
//   - 模型 + 渠道匹配（MatchModels + MatchChannelScopeEx）；
//   - 非 proxy 路由（native，及历史数据 error）命中即短路返回该项（豁免/拒绝语义优先）；
//   - proxy 路由收集全部匹配项（叠加规则，如字段过滤多条规则合并）。
//
// 语义统一：所有能力插件（vision/sensitive/field/request-log）共用，避免
// 「vision_v2 第一个命中就返回 vs 其它 native 优先短路」的行为分叉。
// 历史 route="error" 数据不迁移：非 proxy 短路语义下自动退化为 native（透传），
// 即「不支持就不管他」。
func SelectCapabilityRoutes(routes []CapabilityRoute, capability, model string, scope ChannelRequestScope) []*CapabilityRoute
```

### 2. 四个插件统一调用

| 插件 | 现函数 | 改法 |
|---|---|---|
| vision_v2 | `DecideRouteScope`（第一个命中返回）| 调 `SelectCapabilityRoutes`，取第 0 个；`route.Route == RouteError` 分支删除 |
| sensitive-filter | `DecideRoutesScope`（native 短路 + proxy 收集）| 调 `SelectCapabilityRoutes` |
| field-filter | `decideRoutes`（同）| 调 `SelectCapabilityRoutes` |
| request-log | `DecideRoutesScope`（同）| 调 `SelectCapabilityRoutes` |
| vision（旧，废弃）| `DecideRouteScope` | 同步删 RouteError 分支（保持编译通过）|

### 3. sensitive-filter 删除"命中拒绝"

- `service.go:171` `if routes[0].Route == types.RouteError { 拒绝 }` 删除
- 影响：命中敏感词不再直接拒绝请求，只能 proxy 替换或 native 透传
- ⚠️ 注意：这是内容安全行为的变更（原"命中违规词直接 400"）。用户已确认"不支持就不管他"——但敏感词 error 是内容拦截而非"能力不支持"，plan 里明示，待用户确认

### 4. 前端

- `CapabilityRouteEditor.vue`：sensitive_filter routeOptions 删 error 项（三态→两态）；submit 删 error 校验
- `CapabilityRouteTable.vue`：routeLabel/routeVariant 删 error 项
- `lib/types.ts`：route 类型改 `'native' | 'proxy' | string`

## 测试

- types：新增 `TestSelectCapabilityRoutes`（native 短路优先于 proxy、proxy 全收集、历史 error 退化为 native、模型/渠道不匹配返回空）
- vision_v2：现有测试适配（error 相关断言删除/改判透传）
- sensitive-filter：error 拒绝测试删除或改断言
- field-filter / request-log：回归
- 前端：vue-tsc + vite build

## 风险

- sensitive-filter error（内容拒绝）删除是行为变更——确认后再动
- DB 存量 route="error" 数据不迁移，运行时自动当 native 处理（透传），需确认可接受
- 旧 vision 仍编译（废弃未删）——同步改保持 build 通过

## 优先级

P0：统一选择函数（vision_v2 不依赖 position 排序的隐患）
P1：删 RouteError 常量 + 各插件 error 分支
P2：前端 UI
