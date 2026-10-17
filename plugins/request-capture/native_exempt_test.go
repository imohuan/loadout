package requestcapture

import (
	"log/slog"
	"testing"

	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// 回归（用户 2026-10-17 反馈）：其它能力（vision）的 native 通配路由排在前面时，
// 不得把 request_capture 的抓包一并豁免。只有本能力自己的 native 路由才算豁免。
func TestShouldCaptureNotBlockedByForeignNativeRoute(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	routes := []types.CapabilityRoute{
		{Capability: "vision", Route: "native", Models: []string{"*"}},
		{Capability: "request_capture", Route: "proxy", Models: []string{"step-5-preview"}},
	}
	if err := st.Write(types.FileCapabilityRoutes, routes); err != nil {
		t.Fatal(err)
	}
	svc := NewService(st, slog.Default(), t.TempDir())
	pipe := &modelgateway.ProxyPipeline{
		RequestID: "r1",
		Request:   &modelgateway.ProxyRequest{Method: "POST", Path: "chat/completions", Model: "step-5-preview", Body: []byte("{}")},
	}
	if !svc.shouldCapture(pipe) {
		t.Fatal("foreign native route must not disable capture")
	}
}

// 本能力自己的 native 路由仍然应当豁免抓包。
func TestShouldCaptureOwnNativeStillExempts(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	routes := []types.CapabilityRoute{
		{Capability: "request_capture", Route: "native", Models: []string{"step-*"}},
		{Capability: "request_capture", Route: "proxy", Models: []string{"*"}},
	}
	if err := st.Write(types.FileCapabilityRoutes, routes); err != nil {
		t.Fatal(err)
	}
	svc := NewService(st, slog.Default(), t.TempDir())
	pipe := &modelgateway.ProxyPipeline{
		RequestID: "r2",
		Request:   &modelgateway.ProxyRequest{Method: "POST", Path: "chat/completions", Model: "step-5-preview", Body: []byte("{}")},
	}
	if svc.shouldCapture(pipe) {
		t.Fatal("own native route should exempt capture")
	}
}

