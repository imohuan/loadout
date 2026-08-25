# 02 - 插件系统（框架机制）

> Loadout 是 "Cordis 思想的 Go 实现"：一切皆插件，core 不 import 任何业务。
> 本文讲清框架提供的**机制**（接口、装配、事件、自检、路由）；
> 怎么用它写一个插件，见 [03-插件开发指南](./03-plugin-dev-guide.md)。

源码位置：`core/plugin/`（manifest.go / plugin.go / context.go / loader.go / context_impl.go）。

## 1. 两个核心类型

### 1.1 Manifest（插件清单）

```go
// core/plugin/manifest.go
type Manifest struct {
	Name    string   `yaml:"name"`    // 插件唯一名，如 "vision_v2"
	Version string   `yaml:"version"` // 语义化版本
	Inject  []string `yaml:"inject"`  // 依赖的服务名，就绪后才启动
	Provide []string `yaml:"provide"` // 本插件对外提供的服务名
}
```

- `Inject`：声明本插件需要的服务（由基础服务或其它插件的 `Provide` 提供）。
- `Provide`：声明本插件注册的服务名。`ctx.Set(name, svc)` 时**必须与之一致**，否则装配报错。
- 注：当前采用**编译期装配**（方案 A），Manifest 以 Go 代码返回为主；`LoadManifest`/`ValidateManifest` 主要用于生成器与自检。

### 1.2 Plugin 接口（插件入口）

```go
// core/plugin/plugin.go
type Plugin interface {
	Manifest() Manifest
	Apply(ctx Context) error // 启动入口；返回 error 时整个装配中止
}
```

每个插件 = 一个目录（如 `plugins/vision_v2/`），导出 `func New() plugin.Plugin`。

## 2. Context：插件与框架交互的唯一入口

```go
// core/plugin/context.go
type Context interface {
	Get(name string) any                                          // 取服务；不存在返回 nil
	Set(name string, svc any) Disposer                            // 注册服务；卸载时自动逆序清理
	On(event string, h Handler) Disposer                          // 订阅事件；返回取消订阅的 Disposer
	Emit(event string, payload any)                               // 触发：忽略返回值，错误仅记日志
	Waterfall(event string, payload any) (any, error)             // 触发：返回值作为下一处理器输入，遇错即停
	Effect(fn func()) Disposer                                    // 注册可逆副作用；卸载时逆序执行
	Logger() *slog.Logger                                         // 带当前插件名的日志器
	RegisterCheck(name string, fn func() []Issue)                 // 注册自检项
	RegisterRoute(spec RouteSpec) Disposer                        // 注册 HTTP 路由；返回注销器
}
```

`Disposer` 即 `func()`：返回的清理函数会在插件卸载时**逆序**执行（后注册先清理）。

### 2.1 服务容器（Get / Set）

- 基础服务由框架在 `plugin.Load` 时预注册：`store`、`logger`、`http-client`、`db`。
- 插件 `ctx.Set("xxx", svc)` 注册的服务，其它插件通过 `ctx.Get("xxx")` 取用——**按接口而非 import**。
- 约束（来自 `context_impl.go`）：`Set` 的服务名必须在插件 `Manifest.Provide` 里声明，否则装配失败；同名服务不允许两个插件提供。

### 2.2 事件总线（On / Emit / Waterfall）

```go
type Handler func(payload any) (any, error)
```

- `On(event, h)` 订阅；返回 `Disposer` 可取消。
- `Emit`：**并行/顺序调用所有处理器，忽略返回值**，错误仅记日志（用于"通知类"事件）。
- `Waterfall`：**串行**调用，上一个处理器的返回值作为下一个的输入；某处理器返回 `error` 即停止并把 error 上抛（用于"改写类"事件，如改写请求体）。

> Emit 与 Waterfall 的选择是插件协作的关键：要"改东西"用 Waterfall，要"知会一下"用 Emit。

### 2.3 可逆副作用（Effect）

`ctx.Effect(fn)` 注册一个清理函数，插件卸载时逆序执行。典型用途：停止后台监听、关闭连接。
等价于 `On`/`RegisterRoute`/`RegisterCheck` 的底层清理机制——它们都通过 `Effect` 登记。

### 2.4 日志（Logger）

`ctx.Logger()` 返回带 `plugin` 字段（当前插件名）的 `*slog.Logger`，日志会自动带上 `plugins/xxx/...` 的源码位置。

### 2.5 自检（RegisterCheck）

```go
type Issue struct { Level string; Message string } // Level: info / warn / error
ctx.RegisterCheck("config-ok", func() []plugin.Issue { return nil })
```

自检项在启动时打印，并通过 `/api/plugins` 实时重跑，供管理后台展示。返回 `nil` 或空切片表示无问题。

### 2.6 路由注册（RegisterRoute）

```go
type RouteSpec struct {
	Method  string   // GET/POST/...（空 = 全部方法）
	Pattern string   // 如 "/api/hello"、"v1/{path...}"
	Auth    AuthKind // 认证类别，框架据此挂中间件
	Handler http.Handler
}
```

`Auth` 四档（见 `core/plugin/plugin.go`）：

| 常量 | 含义 | 中间件 |
|---|---|---|
| `AuthSession` | 管理后台会话 | JWT Cookie 校验 |
| `AuthSkKey` | 模型 API | `Authorization: Bearer sk-xxx` |
| `AuthMCPHeader` | MCP 端点 | 默认 header `X-Loadout-Key` |
| `AuthPublic` | 无需认证 | 无 |

## 3. 装配：拓扑排序 + 逐个 Apply

入口：`core/plugin/loader.go` 的 `Load(plugins, opts)`：

1. 预注册基础服务（`opts.Services`）。
2. 构建插件索引；校验插件名唯一、`Provide` 不被多个插件重复、不允许覆盖基础服务。
3. 计算依赖：若插件 A 的 `Inject` 命中插件 B 的 `Provide`，则 A 依赖 B。
4. **拓扑排序**（稳定：冲突时按插件名字典序），保证依赖在前。检测到环则返回错误。
5. 按序逐个 `Apply`；任一 `Apply` 返回 error 立即中止并逆序 `dispose` 已装配的副作用。
6. 装配完成后可调用 `Assembly.Unload()` 逆序卸载；`Assembly.ChecksByPlugin()` 取自检结果。

> 结论：插件**不需要手动控制启动顺序**——只要把依赖关系写进 `Manifest.Inject/Provide`，框架自动排序。

## 4. 插件登记（新增插件的唯一"全局动作"）

所有插件的实例化集中在 `plugins/registry.go` 的 `All()`：

```go
func All() []plugin.Plugin {
	return []plugin.Plugin{
		gatewaykeys.New(),
		adminauth.New(),
		// ... 其它插件 ...
		volcfreequota.New(),
	}
}
```

新增插件的最后一步就是在这里 `import` 本包并追加 `xxx.New(),`（详见 [03-插件开发指南](./03-plugin-dev-guide.md)）。

## 5. 事件 hook 速查（实际由 model-gateway 触发）

能力插件大多通过订阅这些事件介入转发。常量与载荷定义在 `plugins/model-gateway/types.go`：

| 事件常量 | 字符串 | 时机 | 用途 |
|---|---|---|---|
| `EventBeforeUpstream` | `chat:before-upstream` | chat 转发前（一次） | 改写 Messages/VisionText |
| `EventUpstreamFailed` | `chat:upstream-failed` | chat 上游失败 | 切换目标（旧链路） |
| `ProxyBeforeUpstream` | `proxy:before-upstream` | 透明代理转发前（入口一次） | 改写 Body/Path/Query/Header |
| `ProxyBeforeAttempt` | `proxy:before-attempt` | **每次渠道尝试前** | 安检/改写（渠道上下文已就绪） |
| `ProxyAfterUpstream` | `proxy:after-upstream` | 非流式响应返回后 | 改写状态码/Header/Body |
| `ProxyStreamChunk` | `proxy:stream-chunk` | 流式逐 chunk | 改写/删除 chunk（返回 nil 删除） |
| `ProxyUpstreamFailed` | `proxy:upstream-failed` | 上游失败（聚合路径） | failover |
| `ProxyUpstreamSucceeded` | `proxy:upstream-succeeded` | 上游成功 | 恢复可用 |
| `ProxyAttemptFailed` | `proxy:attempt-failed` | 每次尝试写失败行 | 日志收尾 |

> **关键陷阱**：需要"渠道上下文"（当前渠道/BaseURL）的改写，**必须挂 `ProxyBeforeAttempt`**，而非 `ProxyBeforeUpstream`（`ProxyBeforeUpstream` 在入口阶段渠道上下文尚为空）。vision_v2 正是因此从 `ProxyBeforeUpstream` 改到 `ProxyBeforeAttempt`——见 [03](./03-plugin-dev-guide.md) 的常见坑。

## 下一步

- 动手写插件 → [03-插件开发指南](./03-plugin-dev-guide.md)
- 看这些机制的实战 → [04-模型网关](./04-model-gateway.md)
