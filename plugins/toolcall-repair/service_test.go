package toolcallrepair

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

func newPlugin(t *testing.T, routes []types.CapabilityRoute) (*Service, *store.Store) {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Write(types.FileCapabilityRoutes, routes); err != nil {
		t.Fatal(err)
	}
	return NewService(st, slog.Default()), st
}

func dirtyBody() []byte {
	return []byte(`{"model":"step-5-preview","messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"r1"},
		{"role":"tool","tool_call_id":"c2","content":"r2"}
	]}`)
}

func pipeWith(body []byte) *modelgateway.ProxyPipeline {
	return &modelgateway.ProxyPipeline{
		Request:  &modelgateway.ProxyRequest{Path: "chat/completions", Body: body, Model: "step-5-preview"},
		Metadata: map[string]any{},
	}
}

// 命中 proxy 路由：补全序列，且不得出现空工具名。
func TestHandle_ProxyRouteRepairs(t *testing.T) {
	svc, _ := newPlugin(t, []types.CapabilityRoute{{
		Models: []string{"step-5-preview"}, Capability: capabilityName, Route: types.RouteProxy,
	}})
	pipe := pipeWith(dirtyBody())
	out, err := svc.HandleProxyBeforeAttempt(pipe)
	if err != nil {
		t.Fatal(err)
	}
	got := out.(*modelgateway.ProxyPipeline)
	if bytes.Equal(got.Request.Body, dirtyBody()) {
		t.Fatal("命中路由时应改写 body")
	}
	if bytes.Contains(got.Request.Body, []byte(`"name":""`)) {
		t.Fatalf("不得写出空工具名: %s", got.Request.Body)
	}
}

// 未配置路由：零改动（这是本次拆分的核心语义）。
func TestHandle_NoRoutePassthrough(t *testing.T) {
	svc, _ := newPlugin(t, nil)
	body := dirtyBody()
	pipe := pipeWith(body)
	out, err := svc.HandleProxyBeforeAttempt(pipe)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.(*modelgateway.ProxyPipeline); !bytes.Equal(got.Request.Body, body) {
		t.Fatal("未配置能力路由时不应改写请求体")
	}
}

// native 路由：显式豁免，同样零改动。
func TestHandle_NativePassthrough(t *testing.T) {
	svc, _ := newPlugin(t, []types.CapabilityRoute{{
		Models: []string{"*"}, Capability: capabilityName, Route: types.RouteNative,
	}, {
		Models: []string{"step-5-preview"}, Capability: capabilityName, Route: types.RouteProxy,
	}})
	body := dirtyBody()
	pipe := pipeWith(body)
	out, _ := svc.HandleProxyBeforeAttempt(pipe)
	if got := out.(*modelgateway.ProxyPipeline); !bytes.Equal(got.Request.Body, body) {
		t.Fatal("native 命中应短路透传")
	}
}

// 渠道级约束：只对指定 base_url 的渠道生效。
func TestHandle_ChannelBaseURLScope(t *testing.T) {
	svc, st := newPlugin(t, []types.CapabilityRoute{{
		Models: []string{"step-5-preview"}, Capability: capabilityName, Route: types.RouteProxy,
		ChannelBaseURLs: []string{"https://copilot.tencent.com/v2"},
	}})
	if err := st.Write(types.FileChannels, []types.Channel{
		{ID: "wb", ChannelName: "workbuddy", BaseURL: "https://copilot.tencent.com/v2", Enabled: true},
		{ID: "volc", ChannelName: "volcengine", BaseURL: "https://ark.cn-beijing.volces.com/api/v3", Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}

	match := pipeWith(dirtyBody())
	// before-attempt 阶段：真实渠道已经写入，按当前渠道判断。
	match.Metadata["__current_channel"] = "wb"
	match.Metadata["__current_channel_base_url"] = "https://copilot.tencent.com/v2"
	out, _ := svc.HandleProxyBeforeAttempt(match)
	if bytes.Equal(out.(*modelgateway.ProxyPipeline).Request.Body, dirtyBody()) {
		t.Fatal("workbuddy 渠道命中，应改写")
	}

	miss := pipeWith(dirtyBody())
	miss.Metadata["__current_channel"] = "volc"
	miss.Metadata["__current_channel_base_url"] = "https://ark.cn-beijing.volces.com/api/v3"
	out, _ = svc.HandleProxyBeforeAttempt(miss)
	if got := out.(*modelgateway.ProxyPipeline).Request.Body; !bytes.Equal(got, dirtyBody()) {
		t.Fatalf("volcengine 渠道未配置，不应改写，实际: %s", got)
	}
}

// failover：上一渠道补全过，切到未配置的渠道时必须还原原始 body。
func TestHandle_FailoverRestoresBody(t *testing.T) {
	svc, st := newPlugin(t, []types.CapabilityRoute{{
		Models: []string{"step-5-preview"}, Capability: capabilityName, Route: types.RouteProxy,
		ChannelIDs: []string{"wb"},
	}})
	if err := st.Write(types.FileChannels, []types.Channel{
		{ID: "wb", ChannelName: "workbuddy", BaseURL: "https://copilot.tencent.com/v2", Enabled: true},
		{ID: "volc", ChannelName: "volcengine", BaseURL: "https://ark.cn-beijing.volces.com/api/v3", Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	original := dirtyBody()
	pipe := pipeWith(original)
	pipe.Metadata["__current_channel"] = "wb"
	pipe.Metadata["__current_channel_base_url"] = "https://copilot.tencent.com/v2"
	if _, err := svc.HandleProxyBeforeAttempt(pipe); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(pipe.Request.Body, original) {
		t.Fatal("命中的渠道应改写")
	}

	// 切到另一个未配置的渠道：必须还原，不能把上一渠道的改写带过去。
	pipe.Metadata["__current_channel"] = "volc"
	pipe.Metadata["__current_channel_base_url"] = "https://ark.cn-beijing.volces.com/api/v3"
	if _, err := svc.HandleProxyBeforeAttempt(pipe); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pipe.Request.Body, original) {
		t.Fatalf("切到未配置渠道应还原原始 body，实际: %s", pipe.Request.Body)
	}
}

// 只处理 chat/completions；子请求豁免；未命中即返回原对象。
func TestHandle_SkipsUnrelatedRequests(t *testing.T) {
	svc, _ := newPlugin(t, []types.CapabilityRoute{{
		Models: []string{"*"}, Capability: capabilityName, Route: types.RouteProxy,
	}})
	cases := map[string]*modelgateway.ProxyPipeline{
		"other path": {Request: &modelgateway.ProxyRequest{Path: "responses", Body: dirtyBody(), Model: "m"}, Metadata: map[string]any{}},
		"sub request": {Request: &modelgateway.ProxyRequest{Path: "chat/completions", Body: dirtyBody(), Model: "m"}, Metadata: map[string]any{"__sub_request": true}},
		"nil body": {Request: &modelgateway.ProxyRequest{Path: "chat/completions", Model: "m"}, Metadata: map[string]any{}},
	}
	for name, pipe := range cases {
		before := append([]byte(nil), pipe.Request.Body...)
		if _, err := svc.HandleProxyBeforeAttempt(pipe); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !bytes.Equal(pipe.Request.Body, before) {
			t.Fatalf("%s: 不应改写", name)
		}
	}
}

// 同一 pipe 重复执行结果幂等：快照保证不会在已改写 body 上反复补全。
func TestHandle_Idempotent(t *testing.T) {
	svc, _ := newPlugin(t, []types.CapabilityRoute{{
		Models: []string{"*"}, Capability: capabilityName, Route: types.RouteProxy,
	}})
	pipe := pipeWith(dirtyBody())
	_, _ = svc.HandleProxyBeforeAttempt(pipe)
	first := append([]byte(nil), pipe.Request.Body...)
	_, _ = svc.HandleProxyBeforeAttempt(pipe)
	if !bytes.Equal(pipe.Request.Body, first) {
		t.Fatalf("重复执行应幂等:\nfirst: %s\nsecond: %s", first, pipe.Request.Body)
	}
}

// 真实事故回放：79ed5ee492c3d825 的原始请求含孤立 tool 结果；补全后不得出现空名。
func TestHandle_RealReplayNeverEmitsEmptyName(t *testing.T) {
	path := os.Getenv("LOADOUT_REPLAY_RAW")
	if path == "" {
		t.Skip("未设置 LOADOUT_REPLAY_RAW，跳过真实请求回放")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skip(err.Error())
	}
	var body []byte
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "const REQUEST_BODY = ") {
			var s string
			if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(line, "const REQUEST_BODY = "), ";")), &s); err != nil {
				t.Fatal(err)
			}
			body = []byte(s)
		}
	}
	if len(body) == 0 {
		t.Skip("复现脚本里没有 REQUEST_BODY")
	}
	svc, _ := newPlugin(t, []types.CapabilityRoute{{
		Models: []string{"step-5-preview"}, Capability: capabilityName, Route: types.RouteProxy,
	}})
	pipe := pipeWith(body)
	out, _ := svc.HandleProxyBeforeAttempt(pipe)
	fixed := out.(*modelgateway.ProxyPipeline).Request.Body
	if bytes.Contains(fixed, []byte(`"name":""`)) || bytes.Contains(fixed, []byte(`"name": ""`)) {
		t.Fatal("补全后仍含空工具名，上游会 400")
	}
	var parsed struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(fixed, &parsed); err != nil {
		t.Fatalf("补全结果不是合法 JSON: %v", err)
	}
}

// 端到端：网关 + 插件 hook，mock 上游收到的 body 已清理；未配置路由时原样透传。
func TestE2E_GatewayHook(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		var gotBody []byte
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotBody, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
		}))

		st, err := store.New(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Write(types.FileChannels, []types.Channel{
			{ID: "ch", Name: "k", ChannelName: "step", BaseURL: upstream.URL, Enabled: true},
		}); err != nil {
			t.Fatal(err)
		}
		if enabled {
			if err := st.Write(types.FileCapabilityRoutes, []types.CapabilityRoute{{
				Models: []string{"step-5-preview"}, Capability: capabilityName, Route: types.RouteProxy,
			}}); err != nil {
				t.Fatal(err)
			}
		} else if err := st.Write(types.FileCapabilityRoutes, []types.CapabilityRoute{}); err != nil {
			t.Fatal(err)
		}

		ctx := newMockCtx()
		svc := NewService(st, slog.Default())
		ctx.On(modelgateway.ProxyBeforeAttempt, svc.HandleProxyBeforeAttempt)
		gw := modelgateway.NewService(st, slog.Default(), ctx)

		body := dirtyBody()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		rr := httptest.NewRecorder()
		gw.HandleProxy(rr, req)
		upstream.Close()

		if rr.Code != http.StatusOK {
			t.Fatalf("enabled=%v 状态码=%d: %s", enabled, rr.Code, rr.Body.String())
		}
		if enabled {
			if bytes.Contains(gotBody, []byte(`"name":""`)) {
				t.Fatalf("enabled: 上游仍收到空工具名: %s", gotBody)
			}
			if bytes.Equal(gotBody, body) {
				t.Fatal("enabled: 应改写请求体")
			}
		} else if !bytes.Equal(gotBody, body) {
			t.Fatalf("disabled: 应原样透传，收到 %s", gotBody)
		}
	}
}
