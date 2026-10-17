package requestcapture

import (
	"log/slog"
	"testing"

	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// 复现用户真实路由表（2026-10-17 生产库），验证 request_capture 判定是否受行顺序影响。
func TestShouldCaptureRealRouteTableOrder(t *testing.T) {
	real := []types.CapabilityRoute{
		{Capability: "request_capture", Route: "proxy", Models: []string{"step-5-preview"}},
		{Capability: "request_log", Route: "proxy", Models: []string{"step-5-preview"}, RequestLogKeepBase64: true},
		{Capability: "vision", Route: "native", Models: []string{"*"}, ChannelBaseURLs: []string{"https://copilot.tencent.com/v2"}},
		{Capability: "request_log", Route: "proxy", Models: []string{"*"}},
		{Capability: "message_inject", Route: "proxy", Models: []string{"git-*"}},
		{Capability: "force_stream", Route: "proxy", Models: []string{"*"}, ChannelBaseURLs: []string{"https://copilot.tencent.com/v2"}},
		{Capability: "sensitive_filter", Route: "proxy", Models: []string{"*"}},
		{Capability: "vision", Route: "proxy", Models: []string{"deepseek-*", "hy*", "glm-*"}},
		{Capability: "field_filter", Route: "proxy", Models: []string{"*"}},
	}
	// 场景 A：capture 在最上面
	for _, tc := range []struct {
		name  string
		routs []types.CapabilityRoute
	}{{"capture-on-top", real}, {"capture-at-bottom", append(append([]types.CapabilityRoute{}, real[1:]...), real[0])}} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := store.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := st.Write(types.FileCapabilityRoutes, tc.routs); err != nil {
				t.Fatal(err)
			}
			svc := NewService(st, slog.Default(), t.TempDir())
			pipe := &modelgateway.ProxyPipeline{
				RequestID: "x",
				Request:   &modelgateway.ProxyRequest{Method: "POST", Path: "chat/completions", Model: "step-5-preview", Body: []byte("{}")},
			}
			if !svc.shouldCapture(pipe) {
				t.Fatalf("%s: 应当抓包，实际判定不抓", tc.name)
			}
		})
	}
}

