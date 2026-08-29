# P4 轻方案：保证 sensitive-filter 最后执行

## 目标
三个改请求体的插件（field-filter、message-inject、sensitive-filter）都订阅 `proxy:before-attempt`
waterfall 事件，改同一份 `pipe.Request.Body`。唯一真实顺序冲突：message-inject 注入的内容若含敏感词，
必须由 sensitive-filter 在最后做整体替换才被过滤。因此**保证 sensitive-filter 最后执行**即可，无需给
插件加优先级、不动 UI、不动数据库。

## 现状
- 装配顺序由 `core/plugin/loader.go:topoSort` 决定：无依赖插件按**插件名字典序**排。
- 三个插件名字字典序 f<m<s，当前恰好 sensitive-filter 最后——但这是巧合，非契约。
- 框架依赖机制（loader.go:63-82）：`Inject` 声明的服务若由某插件提供，则本插件依赖该插件，拓扑排序强制排后；
  服务无人提供则启动报错。

## 方案（依赖强制，防回归）
修改 `plugins/sensitive-filter/plugin.go`：
1. `Manifest()` 的 `Inject` 追加 `"field-filter"`、`"message-inject"`（两者均 `Provide` 同名服务，合法）。
   → `topoSort` 强制 sensitive-filter 在 field-filter、message-inject 之后装配，从而其 `ctx.On`
   `proxy:before-attempt` 的订阅在最后，waterfall 里最后执行。
   → 若 field-filter/message-inject 被移除，启动报错（loader.go:75），防顺序契约被无意破坏。
2. 加注释说明：依赖仅用于排序约束（不取用服务对象），保证敏感词整体替换最后执行。

## 改动文件
- `plugins/sensitive-filter/plugin.go`（唯一）

## 验证
- `go build ./...`、`go vet ./plugins/sensitive-filter/...`
- `go test ./plugins/sensitive-filter/... ./plugins/field-filter/... ./plugins/message-inject/...`
- 补一个装配顺序断言测试：Load 全部插件后 sensitive-filter 在 field-filter/message-inject 之后
  （放 core/plugin/loader 或 registry 相关测试，确认可行则加，否则手测顺序）。

## 不做的事
- 不动 UI（CapabilityRouteTable.vue）
- 不动数据库（capability_routes 表）
- 不给插件加优先级属性
