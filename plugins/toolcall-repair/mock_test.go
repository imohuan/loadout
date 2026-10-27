package toolcallrepair

import (
	"context"
	"log/slog"

	"loadout/core/plugin"
)

// mockCtx 最小 plugin.Context：支持 On/Waterfall，其余方法空实现。
type mockCtx struct {
	handlers map[string][]plugin.Handler
}

func newMockCtx() *mockCtx { return &mockCtx{handlers: map[string][]plugin.Handler{}} }

func (m *mockCtx) Get(name string) any                      { return nil }
func (m *mockCtx) Set(name string, svc any) plugin.Disposer { return func() {} }

func (m *mockCtx) On(event string, h plugin.Handler) plugin.Disposer {
	m.handlers[event] = append(m.handlers[event], h)
	return func() {}
}

func (m *mockCtx) Emit(event string, payload any) {
	for _, h := range m.handlers[event] {
		_, _ = h(payload)
	}
}

func (m *mockCtx) Waterfall(event string, payload any) (any, error) {
	cur := payload
	for _, h := range m.handlers[event] {
		next, err := h(cur)
		if err != nil {
			return nil, err
		}
		cur = next
	}
	return cur, nil
}

func (m *mockCtx) Effect(fn func()) plugin.Disposer                    { return func() {} }
func (m *mockCtx) Logger() *slog.Logger                                { return slog.Default() }
func (m *mockCtx) RegisterCheck(name string, fn func() []plugin.Issue) {}
func (m *mockCtx) RegisterRoute(spec plugin.RouteSpec) plugin.Disposer { return func() {} }

func (m *mockCtx) SetRouteLogPresenceHook(install func(fn func(ctx context.Context, ids []string) (map[string]bool, error))) {
}

func (m *mockCtx) InstallRouteLogPresence(fn func(ctx context.Context, ids []string) (map[string]bool, error)) bool {
	return false
}
