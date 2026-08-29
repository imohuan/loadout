package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakePlugin 测试用插件：Apply 时把自身名追加到共享的 applyOrder 里。
type fakePlugin struct {
	manifest Manifest
	apply    func(ctx Context) error
}

func (f fakePlugin) Manifest() Manifest { return f.manifest }
func (f fakePlugin) Apply(ctx Context) error {
	if f.apply != nil {
		return f.apply(ctx)
	}
	return nil
}

func TestLoadAppliesInDependencyOrder(t *testing.T) {
	var order []string
	mk := func(name string, inject, provide []string) fakePlugin {
		return fakePlugin{
			manifest: Manifest{Name: name, Version: "0.1.0", Inject: inject, Provide: provide},
			apply: func(ctx Context) error {
				order = append(order, name)
				for _, s := range provide {
					ctx.Set(s, name)
				}
				return nil
			},
		}
	}
	// b 依赖 a（inject: svc-a），c 依赖 b。
	plugins := []Plugin{
		mk("c", []string{"svc-b"}, []string{"svc-c"}),
		mk("a", nil, []string{"svc-a"}),
		mk("b", []string{"svc-a"}, []string{"svc-b"}),
	}
	asm, err := Load(plugins, Options{})
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	defer asm.Unload()

	want := []string{"a", "b", "c"}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Fatalf("装配顺序 = %v, want %v", order, want)
	}
	// 依赖服务可被取用。
	if got := asm.Get("svc-a"); got != "a" {
		t.Fatalf("Get(svc-a) = %v, want a", got)
	}
}

func TestLoadInjectMissingService(t *testing.T) {
	p := fakePlugin{manifest: Manifest{Name: "x", Inject: []string{"nobody"}}}
	if _, err := Load([]Plugin{p}, Options{}); err == nil {
		t.Fatal("依赖无人提供的服务应报错，但未报错")
	} else if !strings.Contains(err.Error(), "无人提供") {
		t.Fatalf("错误信息不符: %v", err)
	}
}

func TestLoadOrderDependencyForcesAfter(t *testing.T) {
	var order []string
	mk := func(name string, inject, provide []string) fakePlugin {
		return fakePlugin{
			manifest: Manifest{Name: name, Version: "0.1.0", Inject: inject, Provide: provide},
			apply: func(ctx Context) error {
				order = append(order, name)
				for _, s := range provide {
					ctx.Set(s, name)
				}
				return nil
			},
		}
	}
	plugins := []Plugin{
		mk("sensitive-filter", []string{"svc-field-filter", "svc-message-inject"}, []string{"svc-sensitive-filter"}),
		mk("field-filter", nil, []string{"svc-field-filter"}),
		mk("message-inject", nil, []string{"svc-message-inject"}),
	}
	asm, err := Load(plugins, Options{})
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	defer asm.Unload()
	if len(order) != 3 {
		t.Fatalf("装配插件数 = %d, want 3 (%v)", len(order), order)
	}
	if order[2] != "sensitive-filter" {
		t.Fatalf("sensitive-filter 未排在最后, 装配顺序 = %v", order)
	}
}

func TestLoadDetectsCycle(t *testing.T) {
	mk := func(name string, inject []string) fakePlugin {
		return fakePlugin{manifest: Manifest{Name: name, Inject: inject, Provide: []string{"svc-" + name}}}
	}
	plugins := []Plugin{
		mk("a", []string{"svc-b"}),
		mk("b", []string{"svc-a"}),
	}
	if _, err := Load(plugins, Options{}); err == nil {
		t.Fatal("依赖环应报错，但未报错")
	} else if !strings.Contains(err.Error(), "依赖环") {
		t.Fatalf("错误信息不符: %v", err)
	}
}

func TestLoadDuplicateName(t *testing.T) {
	a := fakePlugin{manifest: Manifest{Name: "dup"}}
	b := fakePlugin{manifest: Manifest{Name: "dup"}}
	if _, err := Load([]Plugin{a, b}, Options{}); err == nil {
		t.Fatal("重名插件应报错")
	}
}

func TestLoadBaseServicesAvailable(t *testing.T) {
	var gotLogger any
	var gotStore any
	p := fakePlugin{
		manifest: Manifest{Name: "p", Inject: []string{"logger", "store"}},
		apply: func(ctx Context) error {
			gotLogger = ctx.Get("logger")
			gotStore = ctx.Get("store")
			return nil
		},
	}
	_, err := Load([]Plugin{p}, Options{Services: map[string]any{
		"logger": "L", "store": "S",
	}})
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if gotLogger != "L" || gotStore != "S" {
		t.Fatalf("基础服务未就绪: logger=%v store=%v", gotLogger, gotStore)
	}
}

func TestContextEffectReverseOrder(t *testing.T) {
	var log []string
	asm, err := Load([]Plugin{fakePlugin{
		manifest: Manifest{Name: "p"},
		apply: func(ctx Context) error {
			ctx.Effect(func() { log = append(log, "1") })
			ctx.Effect(func() { log = append(log, "2") })
			return nil
		},
	}}, Options{})
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	asm.Unload()
	if fmt.Sprint(log) != "[2 1]" {
		t.Fatalf("副作用应逆序执行, got %v", log)
	}
}

func TestContextEvents(t *testing.T) {
	var emitCalls []string
	asm, err := Load([]Plugin{fakePlugin{
		manifest: Manifest{Name: "p"},
		apply: func(ctx Context) error {
			ctx.On("e", func(p any) (any, error) {
				emitCalls = append(emitCalls, "h1:"+p.(string))
				return nil, nil
			})
			ctx.On("e", func(p any) (any, error) {
				emitCalls = append(emitCalls, "h2:"+p.(string))
				return nil, nil
			})
			return nil
		},
	}}, Options{})
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	asm.context().Emit("e", "x")
	if len(emitCalls) != 2 {
		t.Fatalf("Emit 应调用两个处理器, got %v", emitCalls)
	}
}

func TestContextWaterfall(t *testing.T) {
	asm, err := Load([]Plugin{fakePlugin{
		manifest: Manifest{Name: "p"},
		apply: func(ctx Context) error {
			ctx.On("w", func(p any) (any, error) { return p.(int) + 1, nil })
			ctx.On("w", func(p any) (any, error) { return p.(int) * 10, nil })
			return nil
		},
	}}, Options{})
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	got, err := asm.context().Waterfall("w", 1)
	if err != nil {
		t.Fatalf("Waterfall 出错: %v", err)
	}
	if got != 20 {
		t.Fatalf("Waterfall(1) = %v, want 20", got)
	}
}

func TestContextWaterfallStopsOnError(t *testing.T) {
	var calls int
	asm, _ := Load([]Plugin{fakePlugin{
		manifest: Manifest{Name: "p"},
		apply: func(ctx Context) error {
			ctx.On("w", func(p any) (any, error) { calls++; return nil, fmt.Errorf("stop") })
			ctx.On("w", func(p any) (any, error) { calls++; return nil, nil })
			return nil
		},
	}}, Options{})
	if _, err := asm.context().Waterfall("w", nil); err == nil {
		t.Fatal("应传播错误")
	}
	if calls != 1 {
		t.Fatalf("遇错应停止, calls=%d", calls)
	}
}

func TestContextOnDisposer(t *testing.T) {
	var calls int
	asm, _ := Load([]Plugin{fakePlugin{
		manifest: Manifest{Name: "p"},
		apply: func(ctx Context) error {
			off := ctx.On("e", func(p any) (any, error) { calls++; return nil, nil })
			off()
			return nil
		},
	}}, Options{})
	asm.context().Emit("e", nil)
	if calls != 0 {
		t.Fatalf("取消订阅后不应再收到事件, calls=%d", calls)
	}
}

func TestContextRegisterRouteAndCheck(t *testing.T) {
	asm, err := Load([]Plugin{fakePlugin{
		manifest: Manifest{Name: "p"},
		apply: func(ctx Context) error {
			ctx.RegisterRoute(RouteSpec{Method: "POST", Pattern: "/v1/chat/completions", Auth: AuthSkKey})
			ctx.RegisterCheck("渠道检查", func() []Issue {
				return []Issue{{Level: "error", Message: "无渠道"}}
			})
			return nil
		},
	}}, Options{})
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if len(asm.Routes) != 1 || asm.Routes[0].Auth != AuthSkKey {
		t.Fatalf("路由未正确注册: %+v", asm.Routes)
	}
	checks := asm.Checks()
	if len(checks["渠道检查"]) != 1 || checks["渠道检查"][0].Level != "error" {
		t.Fatalf("自检结果不符: %+v", checks)
	}
}

func TestAssemblyUnloadCleansPluginRegistrations(t *testing.T) {
	var calls int
	asm, err := Load([]Plugin{fakePlugin{
		manifest: Manifest{Name: "owned", Provide: []string{"owned-service"}},
		apply: func(ctx Context) error {
			ctx.Set("owned-service", "value")
			ctx.On("event", func(any) (any, error) { calls++; return nil, nil })
			ctx.RegisterRoute(RouteSpec{Method: "GET", Pattern: "/owned"})
			ctx.RegisterCheck("owned-check", func() []Issue { return nil })
			return nil
		},
	}}, Options{})
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	asm.Unload()
	asm.Unload()
	if asm.Get("owned-service") != nil {
		t.Fatal("卸载后插件服务仍可见")
	}
	asm.context().Emit("event", nil)
	if calls != 0 {
		t.Fatalf("卸载后事件监听仍生效: %d", calls)
	}
	if len(asm.Checks()) != 0 || len(asm.Routes) != 0 {
		t.Fatalf("卸载后仍有自检或路由: checks=%v routes=%v", asm.Checks(), asm.Routes)
	}
}

func TestEffectDisposerRunsOnce(t *testing.T) {
	calls := 0
	asm, err := Load([]Plugin{fakePlugin{
		manifest: Manifest{Name: "effects"},
		apply: func(ctx Context) error {
			dispose := ctx.Effect(func() { calls++ })
			dispose()
			return nil
		},
	}}, Options{})
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	asm.Unload()
	if calls != 1 {
		t.Fatalf("手动清理后卸载不应重复执行: %d", calls)
	}
}

func TestLoadRequiresDeclaredServices(t *testing.T) {
	_, err := Load([]Plugin{fakePlugin{
		manifest: Manifest{Name: "missing", Provide: []string{"service"}},
		apply:    func(Context) error { return nil },
	}}, Options{})
	if err == nil || !strings.Contains(err.Error(), "未注册") {
		t.Fatalf("未真实提供服务应失败: %v", err)
	}
}

func TestLoadRejectsBaseServiceOverride(t *testing.T) {
	_, err := Load([]Plugin{fakePlugin{
		manifest: Manifest{Name: "override", Provide: []string{"store"}},
		apply:    func(ctx Context) error { ctx.Set("store", "replacement"); return nil },
	}}, Options{Services: map[string]any{"store": "base"}})
	if err == nil || !strings.Contains(err.Error(), "基础服务") {
		t.Fatalf("覆盖基础服务应失败: %v", err)
	}
}

func TestLoadManifestFromYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plugin.yaml")
	content := "name: vision\nversion: 0.1.0\ninject:\n  - logger\n  - store\nprovide:\n  - vision-service\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	m, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest 失败: %v", err)
	}
	if m.Name != "vision" || len(m.Inject) != 2 || m.Provide[0] != "vision-service" {
		t.Fatalf("清单解析错误: %+v", m)
	}
	if err := ValidateManifest(m); err != nil {
		t.Fatalf("合法清单校验失败: %v", err)
	}
	if err := ValidateManifest(Manifest{}); err == nil {
		t.Fatal("空清单应校验失败")
	}
}

// TestChecksByPlugin 验证自检结果按插件分组且记录归属插件。
func TestChecksByPlugin(t *testing.T) {
	mk := func(name string) fakePlugin {
		return fakePlugin{
			manifest: Manifest{Name: name, Version: "0.1.0"},
			apply: func(ctx Context) error {
				ctx.RegisterCheck(name+"-check", func() []Issue {
					return []Issue{{Level: "error", Message: name + " 有问题"}}
				})
				return nil
			},
		}
	}
	asm, err := Load([]Plugin{mk("a"), mk("b")}, Options{})
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	defer asm.Unload()

	got := asm.ChecksByPlugin()
	if len(got) != 2 {
		t.Fatalf("插件数 = %d，期望 2：%+v", len(got), got)
	}
	if got[0].Plugin != "a" || got[1].Plugin != "b" {
		t.Fatalf("插件分组错误: %+v", got)
	}
	for _, pc := range got {
		if len(pc.Checks) != 1 || pc.Checks[0].Name != pc.Plugin+"-check" {
			t.Fatalf("检查项归属错误: %+v", pc)
		}
		if len(pc.Checks[0].Issues) != 1 || pc.Checks[0].Issues[0].Level != "error" {
			t.Fatalf("自检结果错误: %+v", pc.Checks)
		}
	}
}

// TestChecksByPluginIncludesAllPlugins 验证未注册自检项的插件也会出现在结果中（checks 为空数组）。
func TestChecksByPluginIncludesAllPlugins(t *testing.T) {
	asm, err := Load([]Plugin{
		fakePlugin{
			manifest: Manifest{Name: "with-check", Version: "0.1.0"},
			apply: func(ctx Context) error {
				ctx.RegisterCheck("配置检查", func() []Issue {
					return []Issue{{Level: "warn", Message: "注意"}}
				})
				return nil
			},
		},
		fakePlugin{
			manifest: Manifest{Name: "no-check", Version: "0.1.0"},
			apply:    func(ctx Context) error { return nil },
		},
	}, Options{})
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	defer asm.Unload()

	got := asm.ChecksByPlugin()
	if len(got) != 2 {
		t.Fatalf("插件数 = %d，期望 2：%+v", len(got), got)
	}
	byName := map[string]PluginCheck{}
	for _, pc := range got {
		byName[pc.Plugin] = pc
	}
	wc, ok := byName["with-check"]
	if !ok || len(wc.Checks) != 1 || wc.Checks[0].Name != "配置检查" {
		t.Fatalf("with-check 分组错误: %+v", wc)
	}
	nc, ok := byName["no-check"]
	if !ok {
		t.Fatalf("未注册自检项的插件 no-check 缺失: %+v", got)
	}
	if nc.Checks == nil || len(nc.Checks) != 0 {
		t.Fatalf("no-check 应返回空 checks，实际: %+v", nc.Checks)
	}
}
