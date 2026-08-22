package visionv2

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"loadout/core/config"
	"loadout/core/db"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/contracts"
	routelog "loadout/plugins/route-log"
	"loadout/plugins/types"
)

// TestParseToolCallsChat 解析 chat 非流式响应的 tool_calls。
func TestParseToolCallsChat(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"look_at_image","arguments":"{\"image_id\":\"aaa111\",\"prompt\":\"看颜色\"}"}}]},"finish_reason":"tool_calls"}]}`)
	calls := parseToolCallsNonStream(body, formatChat)
	if len(calls) != 1 {
		t.Fatalf("calls = %d 条, want 1", len(calls))
	}
	if calls[0].ID != "call_1" || calls[0].Name != "look_at_image" ||
		calls[0].ImageID != "aaa111" || calls[0].Prompt != "看颜色" {
		t.Errorf("calls[0] = %+v", calls[0])
	}
	// 无工具调用（普通 stop 响应）→ 空。
	stop := []byte(`{"choices":[{"message":{"role":"assistant","content":"你好"},"finish_reason":"stop"}]}`)
	if calls := parseToolCallsNonStream(stop, formatChat); len(calls) != 0 {
		t.Errorf("stop 响应应解析出 0 条, got %d", len(calls))
	}
	// 非法 JSON → 空。
	if calls := parseToolCallsNonStream([]byte("not-json"), formatChat); len(calls) != 0 {
		t.Errorf("非法 JSON 应解析出 0 条, got %d", len(calls))
	}
}

// TestParseToolCallsClaude 解析 claude 非流式响应的 tool_use。
func TestParseToolCallsClaude(t *testing.T) {
	body := []byte(`{"content":[{"type":"text","text":"让我看看"},{"type":"tool_use","id":"toolu_1","name":"look_at_image","input":{"image_id":"aaa111","prompt":"看颜色"}}],"stop_reason":"tool_use","role":"assistant"}`)
	calls := parseToolCallsNonStream(body, formatClaude)
	if len(calls) != 1 {
		t.Fatalf("calls = %d 条, want 1", len(calls))
	}
	if calls[0].ID != "toolu_1" || calls[0].Name != "look_at_image" ||
		calls[0].ImageID != "aaa111" || calls[0].Prompt != "看颜色" {
		t.Errorf("calls[0] = %+v", calls[0])
	}
	// 非 tool_use 停止（end_turn）→ 空。
	stop := []byte(`{"content":[{"type":"text","text":"你好"}],"stop_reason":"end_turn","role":"assistant"}`)
	if calls := parseToolCallsNonStream(stop, formatClaude); len(calls) != 0 {
		t.Errorf("end_turn 响应应解析出 0 条, got %d", len(calls))
	}
	// 其他工具名 → 过滤掉。
	other := []byte(`{"content":[{"type":"tool_use","id":"t1","name":"get_weather","input":{}}],"stop_reason":"tool_use","role":"assistant"}`)
	if calls := parseToolCallsNonStream(other, formatClaude); len(calls) != 0 {
		t.Errorf("非 look_at_image 工具应过滤, got %d", len(calls))
	}
}

// TestParseToolCallsResponses 解析 responses 非流式响应的 function_call。
func TestParseToolCallsResponses(t *testing.T) {
	body := []byte(`{"output":[{"type":"function_call","id":"fc_1","call_id":"fc_1","name":"look_at_image","arguments":"{\"image_id\":\"aaa111\",\"prompt\":\"看颜色\"}"}]}`)
	calls := parseToolCallsNonStream(body, formatResponses)
	if len(calls) != 1 {
		t.Fatalf("calls = %d 条, want 1", len(calls))
	}
	if calls[0].ID != "fc_1" || calls[0].Name != "look_at_image" ||
		calls[0].ImageID != "aaa111" || calls[0].Prompt != "看颜色" {
		t.Errorf("calls[0] = %+v", calls[0])
	}
	// 无 function_call → 空。
	plain := []byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"你好"}]}]}`)
	if calls := parseToolCallsNonStream(plain, formatResponses); len(calls) != 0 {
		t.Errorf("无 function_call 应解析出 0 条, got %d", len(calls))
	}
}

// TestAfterMixedToolNotIntercepted 非流式响应 tool_calls 混合 look_at_image + web_search：
// parseToolCallsNonStream 返回 nil（整轮透传）；纯 look_at_image 正常返回。
func TestAfterMixedToolNotIntercepted(t *testing.T) {
	mixed := []byte(`{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[
		{"id":"call_1","type":"function","function":{"name":"look_at_image","arguments":"{\"image_id\":\"aaa111\",\"prompt\":\"看颜色\"}"}},
		{"id":"call_2","type":"function","function":{"name":"web_search","arguments":"{\"query\":\"天气\"}"}}
	]},"finish_reason":"tool_calls"}]}`)
	if calls := parseToolCallsNonStream(mixed, formatChat); calls != nil {
		t.Fatalf("混合工具调用应返回 nil（整轮透传），got %v", calls)
	}
	vision := []byte(`{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[
		{"id":"call_1","type":"function","function":{"name":"look_at_image","arguments":"{\"image_id\":\"aaa111\",\"prompt\":\"看颜色\"}"}}
	]},"finish_reason":"tool_calls"}]}`)
	if calls := parseToolCallsNonStream(vision, formatChat); len(calls) != 1 {
		t.Errorf("纯 look_at_image 应返回 1 条, got %d", len(calls))
	}
	// claude 混合：tool_use 里有非 look_at_image → nil。
	claudeMixed := []byte(`{"content":[{"type":"tool_use","id":"t1","name":"look_at_image","input":{"image_id":"aaa111","prompt":"p"}},{"type":"tool_use","id":"t2","name":"web_search","input":{}}],"stop_reason":"tool_use","role":"assistant"}`)
	if calls := parseToolCallsNonStream(claudeMixed, formatClaude); calls != nil {
		t.Fatalf("claude 混合工具应返回 nil, got %v", calls)
	}
	// responses 混合：output 里有非 look_at_image → nil。
	respMixed := []byte(`{"output":[{"type":"function_call","id":"fc_1","call_id":"fc_1","name":"look_at_image","arguments":"{\"image_id\":\"aaa111\",\"prompt\":\"p\"}"},{"type":"function_call","id":"fc_2","call_id":"fc_2","name":"web_search","arguments":"{}"}]}`)
	if calls := parseToolCallsNonStream(respMixed, formatResponses); calls != nil {
		t.Fatalf("responses 混合工具应返回 nil, got %v", calls)
	}
}

// TestAfterUpstreamToolLoopChat 端到端：主上游第 1 次返回 tool_calls 非流式响应 →
// HandleProxyAfterUpstream 执行工具循环（视觉识别 + 非流式续流）→ 第 2 次返回最终文本。
func TestAfterUpstreamToolLoopChat(t *testing.T) {
	origCache, origCompress := config.VisionCacheEnabled, config.VisionCompressEnabled
	config.VisionCacheEnabled = true
	config.VisionCompressEnabled = true
	t.Cleanup(func() {
		config.VisionCacheEnabled = origCache
		config.VisionCompressEnabled = origCompress
	})

	svc := NewService(nil, nil, slog.Default())
	svc.cacheDir = t.TempDir()
	imgID, err := svc.SaveImageDataURI(tinyPNGDataURI)
	if err != nil {
		t.Fatalf("SaveImageDataURI: %v", err)
	}
	argsJSON := fmt.Sprintf(`{"image_id":%q,"prompt":%q}`, imgID, "看颜色")
	firstBody := fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"look_at_image","arguments":%q}}]},"finish_reason":"tool_calls"}]}`, argsJSON)
	finalBody := `{"choices":[{"message":{"role":"assistant","content":"最终回答"},"finish_reason":"stop"}]}`

	// 主上游：仅在续流（doUpstream）时被调用，返回最终文本。
	// 首轮 tool_calls 响应由 ap.Response.Body 直接构造，不经过本 server。
	var mainCalls atomic.Int32
	main := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mainCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, finalBody)
	}))
	defer main.Close()

	// 视觉模型 server：非流式返回描述。
	var visionCalls atomic.Int32
	vision := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		visionCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"图片描述"}}]}`)
	}))
	defer vision.Close()

	// 渠道表：main 渠道指向上游 server、vision 渠道指向视觉 server。
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("db.OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	repo, err := db.NewRepository(database)
	if err != nil {
		t.Fatalf("db.NewRepository: %v", err)
	}
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	svc.repo = repo
	svc.st = st
	// 注入真实 route-log（与主链路同库），断言视觉识别/续流的 attempt 序列。
	rl := routelog.NewService(database, slog.Default())
	svc.SetRouteLog(rl)
	if err := rl.Start(context.Background(), contracts.RouteRequest{
		RequestID: "req-after-1", RequestedModel: "gpt-4o", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("rl.Start: %v", err)
	}
	if err := repo.ReplaceChannels(context.Background(), []db.Channel{
		{ID: "vision", Name: "视觉", BaseURL: vision.URL, ManualEnabled: true},
		{ID: "main", Name: "主模型", BaseURL: main.URL, ManualEnabled: true},
	}); err != nil {
		t.Fatalf("ReplaceChannels: %v", err)
	}

	// 构造 pipe + 首次响应载荷。
	pipe := &modelgateway.ProxyPipeline{
		RequestID: "req-after-1",
		Request: &modelgateway.ProxyRequest{
			Method: "POST", Path: "chat/completions",
			Body:  []byte(`{"model":"gpt-4o","stream":false,"messages":[{"role":"user","content":"这图里有啥"}]}`),
			Model: "gpt-4o",
		},
		HTTPRequest: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		Metadata: map[string]any{
			"__route_step":      1, // 模拟主链路已写 step1（主请求首次尝试）
			"__current_channel": "main",
			"__vision_v2_route": &types.CapabilityRoute{ViaOptions: []types.ViaOption{
				{ChannelIDs: []string{"vision"}, ViaModel: "qwen3-vl-flash-2026-01-22"},
			}},
		},
	}
	ap := &modelgateway.AfterUpstreamPayload{
		Pipe:     pipe,
		Response: &modelgateway.ProxyResponse{StatusCode: http.StatusOK, Body: []byte(firstBody)},
	}

	out, err := svc.HandleProxyAfterUpstream(ap)
	if err != nil {
		t.Fatalf("HandleProxyAfterUpstream 报错: %v", err)
	}
	got, ok := out.(*modelgateway.AfterUpstreamPayload)
	if !ok || got.Response == nil {
		t.Fatalf("返回值异常: %#v", out)
	}
	body := string(got.Response.Body)
	if !strings.Contains(body, "最终回答") {
		t.Errorf("最终 body 缺少「最终回答」: %q", body)
	}
	if strings.Contains(body, "tool_calls") {
		t.Errorf("最终 body 不应含 tool_calls: %q", body)
	}
	if got.Response.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want 200", got.Response.StatusCode)
	}
	if visionCalls.Load() == 0 {
		t.Error("视觉模型 server 应被调用")
	}
	if mainCalls.Load() != 1 {
		t.Errorf("主上游应被调 1 次（仅续流），实际 %d", mainCalls.Load())
	}
	// route-log 断言：主请求=1 → 视觉识别=2 → 续流=3。
	// running 占位被 success UPSERT 覆盖（request_id+step_no），Detail 里 step2 只剩最终态。
	detail, err := rl.Detail(context.Background(), pipe.RequestID)
	if err != nil {
		t.Fatalf("rl.Detail: %v", err)
	}
	if len(detail.Attempts) != 2 {
		t.Fatalf("attempts = %d 条, want 2（step2 视觉识别 + step3 续流）: %+v", len(detail.Attempts), detail.Attempts)
	}
	var visionStep, contStep *contracts.RouteAttempt
	for i := range detail.Attempts {
		a := &detail.Attempts[i]
		switch a.StepNo {
		case "2":
			visionStep = a
		case "3":
			contStep = a
		}
	}
	if visionStep == nil {
		t.Error("缺少 step=2 的视觉识别 attempt")
	} else {
		if visionStep.Action != "视觉识别" || visionStep.Result != "success" {
			t.Errorf("step2 = %+v, want Action=视觉识别 Result=success", *visionStep)
		}
		if v, _ := visionStep.Metadata["called_via_tool"].(bool); !v {
			t.Errorf("step2 called_via_tool = %v, want true", visionStep.Metadata["called_via_tool"])
		}
		if v, _ := visionStep.Metadata["tool"].(string); v != "look_at_image" {
			t.Errorf("step2 tool = %v, want look_at_image", visionStep.Metadata["tool"])
		}
		if v, _ := visionStep.Metadata["cache_hit"].(bool); v {
			t.Error("step2 cache_hit = true, want false（本测试未预写缓存）")
		}
	}
	if contStep == nil {
		t.Error("缺少 step=3 的续流 attempt")
	} else {
		if contStep.Action != "首次尝试" || contStep.Result != "success" {
			t.Errorf("step3 = %+v, want Action=首次尝试 Result=success", *contStep)
		}
		if contStep.ChannelID != "main" {
			t.Errorf("step3 ChannelID = %q, want main（续流复用主链路渠道）", contStep.ChannelID)
		}
		if contStep.Stream {
			t.Error("step3 Stream = true, want false（非流式续流）")
		}
	}
	// __route_step 应停在 3（识别推进到 2、续流推进到 3）。
	if v, _ := pipe.Metadata["__route_step"].(int); v != 3 {
		t.Errorf("__route_step = %v, want 3", pipe.Metadata["__route_step"])
	}
}

// TestAfterUpstreamErrorNoFailover 视觉模型 500 → HandleProxyAfterUpstream 返回 err==nil、
// Response.StatusCode=502、Body 含 vision_capability_error（不触发渠道 failover）。
func TestAfterUpstreamErrorNoFailover(t *testing.T) {
	origCache, origCompress := config.VisionCacheEnabled, config.VisionCompressEnabled
	config.VisionCacheEnabled = true
	config.VisionCompressEnabled = true
	t.Cleanup(func() {
		config.VisionCacheEnabled = origCache
		config.VisionCompressEnabled = origCompress
	})

	svc := NewService(nil, nil, slog.Default())
	svc.cacheDir = t.TempDir()
	imgID, err := svc.SaveImageDataURI(tinyPNGDataURI)
	if err != nil {
		t.Fatalf("SaveImageDataURI: %v", err)
	}
	argsJSON := fmt.Sprintf(`{"image_id":%q,"prompt":%q}`, imgID, "看颜色")
	firstBody := fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"look_at_image","arguments":%q}}]},"finish_reason":"tool_calls"}]}`, argsJSON)

	// 视觉模型 server：500。
	vision := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, "boom")
	}))
	defer vision.Close()

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("db.OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	repo, err := db.NewRepository(database)
	if err != nil {
		t.Fatalf("db.NewRepository: %v", err)
	}
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	svc.repo = repo
	svc.st = st
	if err := repo.ReplaceChannels(context.Background(), []db.Channel{
		{ID: "vision", Name: "视觉", BaseURL: vision.URL, ManualEnabled: true},
		{ID: "main", Name: "主模型", BaseURL: "http://127.0.0.1:1", ManualEnabled: true},
	}); err != nil {
		t.Fatalf("ReplaceChannels: %v", err)
	}

	pipe := &modelgateway.ProxyPipeline{
		RequestID: "req-after-2",
		Request: &modelgateway.ProxyRequest{
			Method: "POST", Path: "chat/completions",
			Body:  []byte(`{"model":"gpt-4o","stream":false,"messages":[{"role":"user","content":"这图里有啥"}]}`),
			Model: "gpt-4o",
		},
		HTTPRequest: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		Metadata: map[string]any{
			"__route_step":      1, // 模拟主链路已写 step1（主请求首次尝试）
			"__current_channel": "main",
			"__vision_v2_route": &types.CapabilityRoute{ViaOptions: []types.ViaOption{
				{ChannelIDs: []string{"vision"}, ViaModel: "qwen3-vl-flash-2026-01-22"},
			}},
		},
	}
	ap := &modelgateway.AfterUpstreamPayload{
		Pipe:     pipe,
		Response: &modelgateway.ProxyResponse{StatusCode: http.StatusOK, Body: []byte(firstBody)},
	}

	out, err := svc.HandleProxyAfterUpstream(ap)
	if err != nil {
		t.Fatalf("工具失败不应 return error（避免触发 failover）, got %v", err)
	}
	got, ok := out.(*modelgateway.AfterUpstreamPayload)
	if !ok || got.Response == nil {
		t.Fatalf("返回值异常: %#v", out)
	}
	if got.Response.StatusCode != http.StatusBadGateway {
		t.Errorf("StatusCode = %d, want 502", got.Response.StatusCode)
	}
	var errBody struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(got.Response.Body, &errBody); err != nil {
		t.Fatalf("错误 Body 非 JSON: %v, body=%q", err, string(got.Response.Body))
	}
	if errBody.Error.Type != "vision_capability_error" {
		t.Errorf("error.type = %q, want vision_capability_error", errBody.Error.Type)
	}
	if !strings.Contains(errBody.Error.Message, "视觉工具执行失败") {
		t.Errorf("error.message = %q, want 含「视觉工具执行失败」", errBody.Error.Message)
	}
}
