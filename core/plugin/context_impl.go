package plugin

import (
	"fmt"
	"log/slog"
	"sync"
)

// handlerEntry 带唯一 ID 的事件处理器，用于精确取消订阅。
type handlerEntry struct {
	id uint64
	h  Handler
}

// contextImpl 是 Context 的默认实现，由 Load() 创建并注入每个插件。
type contextImpl struct {
	mu          sync.RWMutex
	services    map[string]any            // 服务名 → 实例
	provides    map[string]string         // 服务名 → 提供它的插件名（装配期 inject 校验用）
	events      map[string][]handlerEntry // 事件名 → 处理器列表（按订阅顺序）
	nextEventID uint64
	effects     []*effectEntry // 副作用清理函数（卸载时逆序执行）
	checks      map[string]func() []Issue
	checksOrder []string
	checkOwner  map[string]string // 检查项名 → 注册它的插件名（供插件自检页分组）
	routes      []RouteSpec

	logger      *slog.Logger // 基础日志器（无插件名）
	currentName string       // 当前正在 Apply 的插件名
	expected    map[string]bool
	applyErr    error
}

type effectEntry struct {
	once sync.Once
	fn   func()
}

func (e *effectEntry) dispose() { e.once.Do(e.fn) }

func newContext(logger *slog.Logger) *contextImpl {
	if logger == nil {
		logger = slog.Default()
	}
	return &contextImpl{
		services:   map[string]any{},
		provides:   map[string]string{},
		events:     map[string][]handlerEntry{},
		checks:     map[string]func() []Issue{},
		checkOwner: map[string]string{},
		logger:     logger,
	}
}

func (c *contextImpl) Get(name string) any {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.services[name]
}

func (c *contextImpl) Set(name string, svc any) Disposer {
	c.mu.Lock()
	owner := c.currentName
	if owner != "" && !c.expected[name] {
		c.applyErr = fmt.Errorf("plugin: 插件 %q 注册了未在 Manifest.Provide 中声明的服务 %q", owner, name)
		c.mu.Unlock()
		return func() {}
	}
	if previous, exists := c.provides[name]; exists {
		c.applyErr = fmt.Errorf("plugin: 服务 %q 已由 %s 注册，不能由 %s 覆盖", name, previous, owner)
		c.mu.Unlock()
		return func() {}
	}
	c.services[name] = svc
	if owner == "" {
		owner = "<base>"
	}
	c.provides[name] = owner
	c.mu.Unlock()

	return c.track(owner, func() {
			c.mu.Lock()
			if c.services[name] == svc {
				delete(c.services, name)
				delete(c.provides, name)
			}
			c.mu.Unlock()
	})
}

func (c *contextImpl) On(event string, h Handler) Disposer {
	c.mu.Lock()
	c.nextEventID++
	id := c.nextEventID
	c.events[event] = append(c.events[event], handlerEntry{id: id, h: h})
	c.mu.Unlock()

	return c.trackCurrent(func() {
			c.mu.Lock()
			entries := c.events[event]
			kept := entries[:0]
			for _, e := range entries {
				if e.id != id {
					kept = append(kept, e)
				}
			}
			if len(kept) == 0 {
				delete(c.events, event)
			} else {
				c.events[event] = kept
			}
			c.mu.Unlock()
	})
}

func (c *contextImpl) Emit(event string, payload any) {
	c.mu.RLock()
	handlers := make([]Handler, 0, len(c.events[event]))
	for _, e := range c.events[event] {
		handlers = append(handlers, e.h)
	}
	c.mu.RUnlock()

	for _, h := range handlers {
		if _, err := h(payload); err != nil {
			c.logger.Warn("事件处理出错", "event", event, "err", err)
		}
	}
}

func (c *contextImpl) Waterfall(event string, payload any) (any, error) {
	c.mu.RLock()
	handlers := make([]Handler, 0, len(c.events[event]))
	for _, e := range c.events[event] {
		handlers = append(handlers, e.h)
	}
	c.mu.RUnlock()

	var err error
	for _, h := range handlers {
		payload, err = h(payload)
		if err != nil {
			return payload, err
		}
	}
	return payload, nil
}

func (c *contextImpl) Effect(fn func()) Disposer {
	return c.trackCurrent(fn)
}

func (c *contextImpl) Logger() *slog.Logger {
	c.mu.RLock()
	name := c.currentName
	c.mu.RUnlock()
	if name == "" {
		return c.logger
	}
	return c.logger.With("plugin", name)
}

func (c *contextImpl) RegisterCheck(name string, fn func() []Issue) {
	c.mu.Lock()
	if owner, exists := c.checkOwner[name]; exists {
		c.applyErr = fmt.Errorf("plugin: 自检项 %q 已由 %s 注册", name, owner)
		c.mu.Unlock()
		return
	}
	c.checksOrder = append(c.checksOrder, name)
	c.checks[name] = fn
	c.checkOwner[name] = c.currentName // 记录归属插件
	c.mu.Unlock()
	c.trackCurrent(func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.checks, name)
		delete(c.checkOwner, name)
		for i, checkName := range c.checksOrder {
			if checkName == name {
				c.checksOrder = append(c.checksOrder[:i], c.checksOrder[i+1:]...)
				break
			}
		}
	})
}

func (c *contextImpl) RegisterRoute(spec RouteSpec) Disposer {
	c.mu.Lock()
	idx := len(c.routes)
	c.routes = append(c.routes, spec)
	c.mu.Unlock()

	return c.trackCurrent(func() {
			c.mu.Lock()
			if idx < len(c.routes) {
				c.routes[idx] = RouteSpec{} // 清空；装配完成后由上层忽略空项
			}
			c.mu.Unlock()
	})
}

func (c *contextImpl) beginApply(name string, provides []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.currentName = name
	c.expected = make(map[string]bool, len(provides))
	for _, service := range provides {
		c.expected[service] = true
	}
	c.applyErr = nil
}

func (c *contextImpl) endApply() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	err := c.applyErr
	c.currentName = ""
	c.expected = nil
	c.applyErr = nil
	return err
}

func (c *contextImpl) trackCurrent(fn func()) Disposer {
	c.mu.RLock()
	owner := c.currentName
	c.mu.RUnlock()
	return c.track(owner, fn)
}

func (c *contextImpl) track(owner string, fn func()) Disposer {
	entry := &effectEntry{fn: fn}
	if owner == "" || owner == "<base>" {
		return entry.dispose
	}
	c.mu.Lock()
	c.effects = append(c.effects, entry)
	c.mu.Unlock()
	return entry.dispose
}

// dispose 逆序执行所有副作用清理函数。
func (c *contextImpl) dispose() {
	c.mu.Lock()
	effects := append([]*effectEntry{}, c.effects...)
	c.effects = nil
	c.mu.Unlock()

	for i := len(effects) - 1; i >= 0; i-- {
		effects[i].dispose()
	}
}
