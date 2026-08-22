package plugin

import (
	"fmt"
	"io"
	"log/slog"
	"sort"
	"sync"
)

// Options 装配参数。
type Options struct {
	Logger   *slog.Logger   // 基础日志器（nil 则用 slog.Default）
	Services map[string]any // 预注册的基础服务（logger、store、http-client 等）
}

// Assembly 一次装配的产物：按依赖序启动后的插件集合 + 路由 + 自检。
type Assembly struct {
	ctx        *contextImpl
	order      []string    // 应用顺序（插件名）
	Routes     []RouteSpec // 所有插件注册的路由（过滤掉已注销的空项）
	unloadOnce sync.Once
}

// Load 按 inject/provide 拓扑排序后逐个 Apply 插件。
// 装配失败时返回错误；成功后可调用 Assembly.Unload() 逆序卸载副作用。
func Load(plugins []Plugin, opts Options) (*Assembly, error) {
	ctx := newContext(opts.Logger)

	// 预注册基础服务。
	for name, svc := range opts.Services {
		ctx.Set(name, svc)
	}

	// 构建插件索引与 provide 索引。
	byName := map[string]Plugin{}
	manifestOf := map[string]Manifest{}
	for _, p := range plugins {
		m := p.Manifest()
		if m.Name == "" {
			return nil, fmt.Errorf("plugin: 存在未声明 name 的插件")
		}
		if _, dup := byName[m.Name]; dup {
			return nil, fmt.Errorf("plugin: 插件名重复: %q", m.Name)
		}
		byName[m.Name] = p
		manifestOf[m.Name] = m
	}

	provideTo := map[string]string{} // 服务名 → 插件名
	for name, m := range manifestOf {
		for _, svc := range m.Provide {
			if _, base := ctx.provides[svc]; base {
				return nil, fmt.Errorf("plugin: 服务 %q 是基础服务，插件 %q 不能覆盖", svc, name)
			}
			if prev, ok := provideTo[svc]; ok && prev != name {
				return nil, fmt.Errorf("plugin: 服务 %q 被多个插件提供（%s、%s）", svc, prev, name)
			}
			provideTo[svc] = name
		}
	}

	// 计算每个插件的插件级依赖（inject 里的服务若由某插件提供，则依赖该插件）。
	deps := map[string]map[string]bool{} // 插件 → 依赖插件集合
	for name := range manifestOf {
		deps[name] = map[string]bool{}
	}
	for name, m := range manifestOf {
		for _, svc := range m.Inject {
			if _, base := ctx.provides[svc]; base {
				continue // 基础服务已就绪
			}
			dep, ok := provideTo[svc]
			if !ok {
				return nil, fmt.Errorf("plugin: 插件 %q 依赖的服务 %q 无人提供", name, svc)
			}
			if dep == name {
				return nil, fmt.Errorf("plugin: 插件 %q 依赖自身提供的服务 %q", name, svc)
			}
			deps[name][dep] = true
		}
	}

	order, err := topoSort(manifestOf, deps)
	if err != nil {
		return nil, err
	}

	asm := &Assembly{ctx: ctx, order: order}
	for _, name := range order {
		p := byName[name]
		ctx.beginApply(name, manifestOf[name].Provide)

		if err := p.Apply(ctx); err != nil {
			_ = ctx.endApply()
			ctx.dispose()
			return nil, fmt.Errorf("plugin: 应用插件 %q 失败: %w", name, err)
		}

		if err := ctx.endApply(); err != nil {
			ctx.dispose()
			return nil, err
		}

		ctx.mu.RLock()
		for _, svc := range manifestOf[name].Provide {
			if ctx.provides[svc] != name {
				ctx.mu.RUnlock()
				ctx.dispose()
				return nil, fmt.Errorf("plugin: 插件 %q 未注册 Manifest.Provide 声明的服务 %q", name, svc)
			}
		}
		ctx.mu.RUnlock()
	}

	ctx.mu.Lock()
	asm.Routes = nonEmptyRoutes(ctx.routes)
	ctx.mu.Unlock()
	return asm, nil
}

// Checks 依次执行所有插件注册的自检项，返回 检查项名 → 问题列表。
// 输出顺序为注册顺序，保证结果稳定。
func (a *Assembly) Checks() map[string][]Issue {
	a.ctx.mu.RLock()
	names := append([]string{}, a.ctx.checksOrder...)
	fns := make([]func() []Issue, len(names))
	for i, n := range names {
		fns[i] = a.ctx.checks[n]
	}
	a.ctx.mu.RUnlock()

	out := make(map[string][]Issue, len(names))
	for i, n := range names {
		issues := fns[i]()
		if issues == nil {
			issues = []Issue{}
		}
		out[n] = issues
	}
	return out
}

// ChecksByPlugin 按插件分组执行自检，返回每个插件的检查项与结果。
// 输出顺序：插件按装配顺序、检查项按注册顺序，保证稳定；
// 未注册自检项的插件也会返回（checks 为空数组），保证覆盖全部插件。
func (a *Assembly) ChecksByPlugin() []PluginCheck {
	a.ctx.mu.RLock()
	names := append([]string{}, a.ctx.checksOrder...)
	fns := make([]func() []Issue, len(names))
	owners := make([]string, len(names))
	for i, n := range names {
		fns[i] = a.ctx.checks[n]
		owners[i] = a.ctx.checkOwner[n]
	}
	a.ctx.mu.RUnlock()

	// 按 owner 分组执行自检，保持检查项注册顺序。
	byPlugin := make(map[string][]CheckResult, len(a.order))
	for i, n := range names {
		issues := fns[i]()
		if issues == nil {
			issues = []Issue{}
		}
		byPlugin[owners[i]] = append(byPlugin[owners[i]], CheckResult{Name: n, Issues: issues})
	}

	// 按装配顺序输出全部插件；未注册自检项的插件返回空 checks。
	out := make([]PluginCheck, 0, len(a.order))
	for _, p := range a.order {
		checks := byPlugin[p]
		if checks == nil {
			checks = []CheckResult{}
		}
		out = append(out, PluginCheck{Plugin: p, Checks: checks})
	}
	return out
}

// Unload 逆序卸载所有插件注册的副作用。
func (a *Assembly) Unload() {
	a.unloadOnce.Do(func() {
		a.ctx.dispose()
		a.ctx.mu.Lock()
		for name, owner := range a.ctx.provides {
			if owner == "<base>" {
				if closer, ok := a.ctx.services[name].(io.Closer); ok {
					_ = closer.Close()
				}
			}
		}
		a.ctx.mu.Unlock()
		a.Routes = nil
	})
}

// Get 在装配完成后按服务名取用服务（供上层在装配后查询）。
func (a *Assembly) Get(name string) any {
	return a.ctx.Get(name)
}

// ctx 暴露给同包测试与上层复用（包内访问）。
func (a *Assembly) context() *contextImpl { return a.ctx }

// nonEmptyRoutes 过滤掉已注销（被清空）的路由。
func nonEmptyRoutes(in []RouteSpec) []RouteSpec {
	out := make([]RouteSpec, 0, len(in))
	for _, r := range in {
		if r.Pattern != "" {
			out = append(out, r)
		}
	}
	return out
}

// topoSort 返回满足「依赖在前」的插件名序列。检测环。
func topoSort(manifestOf map[string]Manifest, deps map[string]map[string]bool) ([]string, error) {
	indeg := map[string]int{}
	for name := range manifestOf {
		indeg[name] = len(deps[name])
	}
	// 稳定顺序：按插件名字典序处理，保证装配结果可复现。
	names := make([]string, 0, len(manifestOf))
	for name := range manifestOf {
		names = append(names, name)
	}
	sort.Strings(names)

	queue := []string{}
	for _, name := range names {
		if indeg[name] == 0 {
			queue = append(queue, name)
		}
	}

	var order []string
	for len(queue) > 0 {
		// 取队列头部并保持字典序（用 sort 保证稳定）。
		cur := queue[0]
		queue = queue[1:]
		order = append(order, cur)

		// 找出依赖 cur 的插件，减入度。
		for _, other := range names {
			if deps[other][cur] {
				indeg[other]--
				if indeg[other] == 0 {
					queue = append(queue, other)
					// 插入后保持字典序。
					sort.Strings(queue)
				}
			}
		}
	}

	if len(order) != len(manifestOf) {
		return nil, fmt.Errorf("plugin: 检测到插件依赖环: %v", remaining(names, order))
	}
	return order, nil
}

func remaining(all, done []string) []string {
	d := map[string]bool{}
	for _, x := range done {
		d[x] = true
	}
	var out []string
	for _, x := range all {
		if !d[x] {
			out = append(out, x)
		}
	}
	return out
}
