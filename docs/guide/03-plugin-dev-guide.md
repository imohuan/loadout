# 03 - 插件开发指南（如何新增一个插件）

> 本文是给"第一次给 Loadout 加插件"的开发者看的实战手册。读完你应能独立新增一个插件，
> 并知道插件内部能做什么。配套机制讲解见 [02-插件系统](./02-plugin-system.md)。

## 0. 心智模型（30 秒）

Loadout 里"一切皆插件"。core 不认识任何业务，业务全部挂在 `plugins/`。
新增一个插件 = **建目录 + 实现 `Plugin` 接口 + 在 `All()` 登记一行**，重新编译即可。
插件之间**不互相 import**，只通过 `Context` 的服务容器与事件总线协作。

## 1. 四步新增一个插件

以新增 `plugins/hello/` 为例。

### 步骤 1：建目录与 `plugin.go`

```
plugins/hello/plugin.go
```

### 步骤 2：实现 `Plugin` 接口

```go
// plugins/hello/plugin.go
package hello

import (
	"log/slog"
	"net/http"

	"loadout/core/plugin"
	modelgateway "loadout/plugins/model-gateway"
)

type helloPlugin struct{}

// New 是框架约定的构造函数（在 registry 里调用）。
func New() plugin.Plugin { return &helloPlugin{} }

// Manifest 声明依赖与提供。Inject 的服务必须有人 Provide；Provide 的服务才能被别人 Get。
func (p *helloPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:    "hello",
		Version: "0.1.0",
		Inject:  []string{"logger"},        // 本插件需要的服务（基础服务或别的插件提供）
		Provide: []string{"hello"},         // 本插件注册的服务名（ctx.Set 必须用同名）
	}
}

// Apply 是启动入口：取依赖、注册服务、挂路由、订阅事件、注册自检。返回 error 会中止整个装配。
func (p *helloPlugin) Apply(ctx plugin.Context) error {
	lg := ctx.Get("logger").(*slog.Logger)   // ① 取依赖服务（基础服务 logger 已预注册）
	lg.Info("hello 插件启动")

	svc := &helloService{}
	ctx.Set("hello", svc)                     // ② 注册本插件服务（卸载时框架自动逆序清理）

	// ③ 注册 HTTP 路由（Auth 决定挂哪种认证中间件）
	ctx.RegisterRoute(plugin.RouteSpec{
		Method:  "GET",
		Pattern: "/api/hello",
		Auth:    plugin.AuthSession,          // 管理后台需登录；公开用 plugin.AuthPublic
		Handler: http.HandlerFunc(svc.handleHello),
	})

	// ④ 订阅事件：在透明代理每次渠道尝试前改写请求（示例：注入一个 header）
	ctx.On(modelgateway.ProxyBeforeAttempt, svc.rewrite)

	// ⑤ 注册自检项（管理后台 /api/plugins 实时展示）
	ctx.RegisterCheck("hello-config", func() []plugin.Issue {
		return nil // nil = 无问题；有问题时返回 []plugin.Issue{{Level:"warn", Message:"..."}}
	})
	return nil
}

type helloService struct{}

func (s *helloService) handleHello(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// rewrite 是事件处理器：Waterfall 的返回值会作为下一个处理器的输入。
func (s *helloService) rewrite(payload any) (any, error) {
	pipe, ok := payload.(*modelgateway.ProxyPipeline)
	if !ok {
		return payload, nil
	}
	if pipe.Request != nil && pipe.Request.Header != nil {
		pipe.Request.Header.Set("X-Hello", "injected")
	}
	return pipe, nil
}
```

> `ProxyPipeline` 等事件载荷类型定义在 `plugins/model-gateway/types.go`。事件常量（`ProxyBeforeAttempt` 等）也在该包。

### 步骤 3：在 `plugins/registry.go` 登记

打开 `plugins/registry.go`，在 import 块加一行，在 `All()` 的返回切片里追加一行：

```go
import (
	// ... 其它
	hello "loadout/plugins/hello"   // 新增
)

func All() []plugin.Plugin {
	return []plugin.Plugin{
		// ... 其它插件
		hello.New(),                 // 新增：顺序无关，框架按依赖自动拓扑排序
	}
}
```

### 步骤 4：编译运行验证

```bash
go build -o loadout ./apps/server
./loadout
# 查看启动日志里 hello 插件的自检项；后台访问 /api/hello 验证路由
```

完成。无需改 core、无需改配置文件——框架自动处理装配顺序与认证中间件。

## 2. 插件内部能做什么（Context 能力速查）

| 能力 | 调用 | 典型用途 |
|---|---|---|
| 取服务 | `ctx.Get("name")` | 取 store / db / logger / 其它插件的 Service |
| 注册服务 | `ctx.Set("name", svc)` | 暴露本插件能力（name 必须在 `Manifest.Provide`） |
| 订阅事件 | `ctx.On(evt, h)` | 介入转发/生命周期（返回 Disposer 可取消） |
| 触发事件 | `ctx.Emit` / `ctx.Waterfall` | 通知他人 / 串行改写 payload |
| 可逆副作用 | `ctx.Effect(fn)` | 停止监听、关连接（卸载时逆序执行） |
| 日志 | `ctx.Logger()` | 带 `plugin=hello` 字段的 slog |
| 自检 | `ctx.RegisterCheck(name, fn)` | 暴露配置健康检查 |
| 路由 | `ctx.RegisterRoute(spec)` | 挂 HTTP 路由（自动按 Auth 挂认证） |

## 3. 能力插件的两大支柱

给模型"附加能力"（视觉、敏感词、字段过滤、日志等）时，复用两个公共机制，避免各插件各写一套。

### 支柱一：`SelectCapabilityRoutes`（统一选路）

来自 `plugins/types`：

```go
func SelectCapabilityRoutes(routes []CapabilityRoute, capability, model string, scope ChannelRequestScope) []*CapabilityRoute
```

- 作用：根据"能力名 + 目标模型 + 请求渠道范围"从能力路由表里选出命中规则。
- 语义（重点）：
  - **非 proxy（native）命中即短路返回**——豁免/降级优先于代理，不依赖表内顺序。
  - **proxy 全部收集**——多条代理规则叠加（如字段过滤多条规则合并）。
  - 返回 `nil` = 无匹配，调用方按透传处理。
- 模型匹配支持 `*` 全匹配、`prefix*` 前缀匹配、精确匹配（`MatchModels`）。
- 渠道范围来自 `ChannelRequestScope{IDs, BaseURLs}`，通常由 `ChannelScopeFromMetadata(pipe.Metadata, resolveBaseURLs)` 从请求 metadata 解析。

```go
import "loadout/plugins/types"

scope := types.ChannelScopeFromMetadata(pipe.Metadata, myResolveBaseURLs)
hits := types.SelectCapabilityRoutes(allRoutes, "my_capability", pipe.Request.Model, scope)
for _, h := range hits {
	if h.Route == types.RouteProxy {
		// 应用 h.ViaOptions / h.Replacements / h.FieldRules ...
	}
}
```

### 支柱二：`ForwardSubRequest`（网关内调上游）

来自 `plugins/model-gateway` 的 Service 方法：

```go
func (s *Service) ForwardSubRequest(ctx context.Context, pipe *ProxyPipeline, streamWriter func(line []byte) error) (*ProxyPipeline, []byte, error)
```

- 作用：插件自己要调上游模型时（如视觉识别、续流），**走网关主链路而非自建 `http.Client`**。
- 好处：自动获得 request-log / 额度 / failover，且不会产生顶级日志行（子请求标记 `__sub_request`）。
- 核心原则：**所有上游请求只能从 model-gateway 一个口子出去**，禁止插件"走后门"直接 `http.Client.Do`。

```go
gw := ctx.Get("model-gateway").(*modelgateway.Service)
pipe := &modelgateway.ProxyPipeline{
	Request: &modelgateway.ProxyRequest{
		Method: "POST",
		Path:   "chat/completions",
		Header: http.Header{"Content-Type": []string{"application/json"}},
		Body:   bodyBytes,
		Model:  visionModel,
	},
}
final, respBody, err := gw.ForwardSubRequest(ctx, pipe, nil)
```

## 4. 事件 hook 怎么用

model-gateway 在转发各阶段触发事件，插件订阅即可介入。参见 [02-插件系统](./02-plugin-system.md) 的事件速查表。

- 改写请求体/路径/header → 订阅 `ProxyBeforeUpstream`（入口一次）或 `ProxyBeforeAttempt`（每次渠道尝试）。
- 改写非流式响应 → 订阅 `ProxyAfterUpstream`。
- 逐 chunk 改流式 → 订阅 `ProxyStreamChunk`（返回 `nil` 即删除该 chunk）。
- 触发用 `Waterfall`（串行改写）；纯通知用 `Emit`。

### 示例：在转发前改写某个字段

```go
ctx.On(modelgateway.ProxyBeforeAttempt, func(payload any) (any, error) {
	pipe := payload.(*modelgateway.ProxyPipeline)
	if pipe.Request != nil && pipe.Request.Body != nil {
		// 解析 JSON、改字段、回写 pipe.Request.Body ...
	}
	return pipe, nil
})
```

## 5. 常见坑（来自真实案例）

1. **`Set` 的服务名必须对得上 `Manifest.Provide`**：否则装配直接报错（框架强制校验）。
2. **需要渠道上下文就挂 `ProxyBeforeAttempt`，不要挂 `ProxyBeforeUpstream`**：
   `ProxyBeforeUpstream` 在入口阶段触发，此时 `__current_channel` / `__current_channel_base_url` 还是空，
   依赖渠道约束的路由（如按渠道的 native 透传）会永远匹配不上，被兜底代理抢走。
   vision_v2 因此从 `ProxyBeforeUpstream` 改到 `ProxyBeforeAttempt`（见其 `plugin.go` 注释）。
3. **native 短路会屏蔽同 model+channel 的 proxy 规则**：若"豁免"和"替换"要并存，豁免行必须精确限定 model/渠道，避免宽通配 `*` 误伤。
4. **插件禁止自建 `http.Client` 直连上游**：一律走 `ForwardSubRequest`，否则丢失日志/额度/failover，并产生游离日志行。
5. **依赖必须有人 `Provide`**：`Inject` 的服务若没有任何插件/基础服务提供，装配报错"依赖的服务无人提供"。
6. **不要import `core` 之外的业务包**：插件之间、插件对 core 都应依赖接口，而非具体实现（保持 core 零业务）。
7. **流式响应不做字段级处理**：增量 delta 无法删除具体字段，字段过滤类插件只在非流式（`ProxyAfterUpstream`）生效。

## 6. 自检（RegisterCheck）

自检项在启动时打印到日志，并通过 `/api/plugins` 实时重跑，供管理后台"插件页"展示。
返回 `nil` 或空切片表示健康；返回 `[]plugin.Issue{{Level:"error", Message:"..."}}` 会在页面标红。

```go
ctx.RegisterCheck("db-ready", func() []plugin.Issue {
	if ctx.Get("db") == nil {
		return []plugin.Issue{{Level: "error", Message: "SQLite 未就绪"}}
	}
	return nil
})
```

## 下一步

- 看能力插件实战 → [04-模型网关](./04-model-gateway.md)（含视觉附加）
- 看聚合/健康检查如何订阅事件 → [05-聚合与健康检查](./05-aggregate-health.md)
