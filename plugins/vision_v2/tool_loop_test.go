package visionv2

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	"loadout/plugins/types"
)

// TestStreamNonVisionToolPassthroughE2E 端到端：视觉激活请求但模型调用 web_search（非本插件工具）：
// HandleProxyStreamChunk 不拦截任何 chunk（含 tool_calls 增量），流结束释放 state。
func TestStreamNonVisionToolPassthroughE2E(t *testing.T) {
	svc := NewService(nil, nil, slog.Default())
	pipe := &modelgateway.ProxyPipeline{
		RequestID: "req-nv-1",
		Request: &modelgateway.ProxyRequest{
			Method: "POST", Path: "chat/completions",
			Body:   []byte(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
			Model:  "gpt-4o",
			Stream: true,
		},
		Metadata: map[string]any{"__vision_v2_active": true},
	}
	lines := []string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"我先搜一下\"}}]}\n",
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"web_search\",\"arguments\":\"\"}}]}}]}\n",
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"query\\\":\\\"天气\\\"}\"}}]}}]}\n",
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n",
		"data: [DONE]\n\n",
	}
	for i, l := range lines {
		out, err := svc.HandleProxyStreamChunk(&modelgateway.StreamChunkPayload{Pipe: pipe, Data: []byte(l)})
		if err != nil {
			t.Fatalf("chunk %d 报错: %v", i, err)
		}
		sp, ok := out.(*modelgateway.StreamChunkPayload)
		if !ok || sp == nil || sp.Data == nil {
			t.Fatalf("chunk %d 被置 nil（非 vision 工具不应拦截）: %q", i, l)
		}
	}
	if _, ok := svc.states["req-nv-1"]; ok {
		t.Error("流结束后 state 应被释放")
	}
}

// TestStreamVisionTextNoStateLeak 视觉激活但无工具调用（普通文本流）：流结束释放 state。
func TestStreamVisionTextNoStateLeak(t *testing.T) {
	svc := NewService(nil, nil, slog.Default())
	pipe := &modelgateway.ProxyPipeline{
		RequestID: "req-plain-1",
		Request: &modelgateway.ProxyRequest{
			Method: "POST", Path: "chat/completions",
			Body:   []byte(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
			Model:  "gpt-4o",
			Stream: true,
		},
		Metadata: map[string]any{"__vision_v2_active": true},
	}
	lines := []string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"你好\"}}]}\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"世界\"}}]}\n",
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n",
		"data: [DONE]\n\n",
	}
	for _, l := range lines {
		if _, err := svc.HandleProxyStreamChunk(&modelgateway.StreamChunkPayload{Pipe: pipe, Data: []byte(l)}); err != nil {
			t.Fatalf("chunk 报错: %v", err)
		}
	}
	if _, ok := svc.states["req-plain-1"]; ok {
		t.Error("无工具调用的流结束后 state 应被释放")
	}
}

// TestStreamNonVisionRequestNoState 非视觉请求（路径非三格式）：不建 state、直接透传。
func TestStreamNonVisionRequestNoState(t *testing.T) {
	svc := NewService(nil, nil, slog.Default())
	pipe := &modelgateway.ProxyPipeline{
		RequestID: "req-other-1",
		Request: &modelgateway.ProxyRequest{
			Method: "POST", Path: "some/other/endpoint",
			Body:   []byte(`{}`),
			Model:  "gpt-4o",
			Stream: true,
		},
	}
	out, err := svc.HandleProxyStreamChunk(&modelgateway.StreamChunkPayload{Pipe: pipe, Data: []byte("hello")})
	if err != nil {
		t.Fatalf("chunk 报错: %v", err)
	}
	sp, ok := out.(*modelgateway.StreamChunkPayload)
	if !ok || sp == nil || sp.Data == nil || string(sp.Data) != "hello" {
		t.Fatalf("非视觉请求应透传原样, got %#v", out)
	}
	if _, ok := svc.states["req-other-1"]; ok {
		t.Error("非视觉请求不应建 state")
	}
}

// TestToolLoopStreamChat 端到端：主上游第 1 次返回含 tool_calls 的流 → hook 收集调用 →
// 工具循环（视觉识别 + 续流）→ 第 2 次返回正常文本流 → 客户端看到完整输出。
func TestToolLoopStreamChat(t *testing.T) {
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

	// 主上游：第 1 次（原始流）返回 tool_calls，第 2 次（续流）返回正常文本流。
	var mainCalls atomic.Int32
	main := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := mainCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"我看看\"}}]}\n")
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"look_at_image\",\"arguments\":%q}}]}}]}\n", argsJSON)
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"最终回答\"}}]}\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer main.Close()

	// 视觉模型 server：SSE 输出「图片描述」；断言请求体 model 透传 via_options 的 viaModel。
	var visionCalls atomic.Int32
	var gotModel atomic.Value
	vision := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		visionCalls.Add(1)
		if body, err := io.ReadAll(r.Body); err == nil {
			var req struct {
				Model string `json:"model"`
			}
			if json.Unmarshal(body, &req) == nil {
				gotModel.Store(req.Model)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"图片描述\"}}]}\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer vision.Close()

	// 渠道表：bailian 渠道（视觉识别）、main 渠道（主链路续流）。
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
	rl := &mockVisionRouteLog{}
	svc.SetRouteLog(rl)
	if err := repo.ReplaceChannels(context.Background(), []db.Channel{
		{ID: "bailian", Name: "百炼", BaseURL: vision.URL, ManualEnabled: true},
		{ID: "main", Name: "主模型", BaseURL: main.URL, ManualEnabled: true},
	}); err != nil {
		t.Fatalf("ReplaceChannels: %v", err)
	}

	// 构造 pipe：主链路渠道走 main，视觉识别按路由 via_options 的 channel_ids 数组走 bailian。
	rr := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	pipe := &modelgateway.ProxyPipeline{
		RequestID: "req-1",
		Request: &modelgateway.ProxyRequest{
			Method: "POST", Path: "chat/completions",
			Body:   []byte(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"这图里有啥"}]}`),
			Model:  "gpt-4o",
			Stream: true,
		},
		ResponseWriter: rr,
		HTTPRequest:    httpReq,
		Metadata: map[string]any{
			"__route_step":       1, // 模拟主链路已写 step1（主请求首次尝试）
			"__main_step":        1, // 主链路段：视觉识别/续流拼子段（1.1、1.2）
			"__current_channel":  "main",
			"__vision_v2_active": true,
			"__vision_v2_route": &types.CapabilityRoute{ViaOptions: []types.ViaOption{
				{ChannelIDs: []string{"bailian"}, ViaModel: "qwen3-vl-flash-2026-01-22"},
			}},
		},
	}

	// 原始流：真实请求主上游，逐行喂给 HandleProxyStreamChunk（模拟网关转发）。
	origResp, err := http.Post(main.URL+"/chat/completions", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("主上游原始流请求失败: %v", err)
	}
	reader := bufio.NewReader(origResp.Body)
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			out, _ := svc.HandleProxyStreamChunk(&modelgateway.StreamChunkPayload{Pipe: pipe, Data: []byte(line)})
			if sp, ok := out.(*modelgateway.StreamChunkPayload); ok && sp != nil && sp.Data != nil {
				rr.Write(sp.Data)
			}
		}
		if err != nil {
			break
		}
	}
	origResp.Body.Close()

	body := rr.Body.String()
	if !strings.Contains(body, "最终回答") {
		t.Errorf("最终 body 缺少「最终回答」: %q", body)
	}
	if !strings.Contains(body, "图片理解") {
		t.Errorf("最终 body 缺少「图片理解」前缀: %q", body)
	}
	if strings.Contains(body, "tool_calls") {
		t.Errorf("最终 body 不应含 tool_calls: %q", body)
	}
	if strings.Contains(body, "<vision_img_") {
		t.Errorf("最终 body 不应含占位符: %q", body)
	}
	if visionCalls.Load() == 0 {
		t.Error("视觉模型 server 应被调用")
	}
	if m, ok := gotModel.Load().(string); !ok || m != "qwen3-vl-flash-2026-01-22" {
		t.Errorf("视觉请求 model = %v, want qwen3-vl-flash-2026-01-22（透传 via_options）", m)
	}
	if mainCalls.Load() != 2 {
		t.Errorf("主上游应被调 2 次（原始流+续流），实际 %d", mainCalls.Load())
	}
	// route-log 断言：step 序列 = 1 主请求 → 1.1 视觉识别 → 1.2 续流（点分，兄弟关系）。
	// mock 按 append 记录（running 占位 + success 覆盖各一条），取 step 的末条即最终态。
	var visionStep, contStep *contracts.RouteAttempt
	for i := range rl.attempts {
		a := &rl.attempts[i]
		switch a.StepNo {
		case "1.1":
			visionStep = a
		case "1.2":
			contStep = a
		}
	}
	if visionStep == nil {
		t.Error("缺少 step=1.1 的视觉识别 attempt")
	} else {
		if visionStep.Action != "视觉识别" || visionStep.Result != "success" {
			t.Errorf("step1.1 = %+v, want Action=视觉识别 Result=success", *visionStep)
		}
		if v, _ := visionStep.Metadata["called_via_tool"].(bool); !v {
			t.Errorf("step1.1 called_via_tool = %v, want true", visionStep.Metadata["called_via_tool"])
		}
		if v, _ := visionStep.Metadata["tool"].(string); v != "look_at_image" {
			t.Errorf("step1.1 tool = %v, want look_at_image", visionStep.Metadata["tool"])
		}
		if v, _ := visionStep.Metadata["image_id"].(string); v != imgID {
			t.Errorf("step1.1 image_id = %v, want %q", visionStep.Metadata["image_id"], imgID)
		}
		if v, _ := visionStep.Metadata["prompt"].(string); v != "看颜色" {
			t.Errorf("step1.1 prompt = %v, want 看颜色", visionStep.Metadata["prompt"])
		}
		if v, _ := visionStep.Metadata["cache_hit"].(bool); v {
			t.Error("step1.1 cache_hit = true, want false（本测试未预写缓存）")
		}
		if v, _ := visionStep.Metadata["via_channel"].(string); v != "bailian" {
			t.Errorf("step1.1 via_channel = %v, want bailian", visionStep.Metadata["via_channel"])
		}
		if visionStep.Model != "qwen3-vl-flash-2026-01-22" {
			t.Errorf("step1.1 Model = %q, want qwen3-vl-flash-2026-01-22", visionStep.Model)
		}
	}
	if contStep == nil {
		t.Error("缺少 step=1.2 的续流 attempt")
	} else {
		if contStep.Action != "首次尝试" || contStep.Result != "success" {
			t.Errorf("step1.2 = %+v, want Action=首次尝试 Result=success", *contStep)
		}
		if contStep.ChannelID != "main" {
			t.Errorf("step1.2 ChannelID = %q, want main（续流复用主链路渠道）", contStep.ChannelID)
		}
		if !contStep.Stream {
			t.Error("step1.2 Stream = false, want true（续流为 SSE 流式）")
		}
	}
	// 主链路段不被视觉推进（__route_step 仍 1），子段计数器到 2（识别 1.1、续流 1.2）。
	if v, _ := pipe.Metadata["__route_step"].(int); v != 1 {
		t.Errorf("__route_step = %v, want 1（主链路段不被视觉子步骤推进）", pipe.Metadata["__route_step"])
	}
	if v, _ := pipe.Metadata["__vision_sub_step"].(int); v != 2 {
		t.Errorf("__vision_sub_step = %v, want 2（识别+续流两个子步骤）", pipe.Metadata["__vision_sub_step"])
	}
}

// TestVisionAttemptStepSequence 验证视觉识别 attempt 的 step 按主链路 __route_step+1 分配：
// 起始 __route_step=1 → 识别写 step=2（running 占位 + success 覆盖）→ 续流写 step=3（首次尝试），
// 最终 __route_step=3。直接调 executeToolLoop（绕过 HandleProxyStreamChunk 主链路）。
func TestVisionAttemptStepSequence(t *testing.T) {
	setVisionConfig(t)

	svc := NewService(nil, nil, slog.Default())
	svc.cacheDir = t.TempDir()
	imgID, err := svc.SaveImageDataURI(tinyPNGDataURI)
	if err != nil {
		t.Fatalf("SaveImageDataURI: %v", err)
	}

	vision := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"图片描述\"}}]}\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer vision.Close()

	var mainCalls atomic.Int32
	main := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mainCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"最终回答\"}}]}\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer main.Close()

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
	rl := &mockVisionRouteLog{}
	svc.SetRouteLog(rl)
	if err := repo.ReplaceChannels(context.Background(), []db.Channel{
		{ID: "bailian", Name: "百炼", BaseURL: vision.URL, ManualEnabled: true},
		{ID: "main", Name: "主模型", BaseURL: main.URL, ManualEnabled: true},
	}); err != nil {
		t.Fatalf("ReplaceChannels: %v", err)
	}

	rr := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	pipe := &modelgateway.ProxyPipeline{
		RequestID: "req-step-1",
		Request: &modelgateway.ProxyRequest{
			Method: "POST", Path: "chat/completions",
			Body:   []byte(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"这图里有啥"}]}`),
			Model:  "gpt-4o",
			Stream: true,
		},
		ResponseWriter: rr,
		HTTPRequest:    httpReq,
		Metadata: map[string]any{
			"__route_step":       1, // 主请求已写 step1，视觉识别从子段开始
			"__main_step":        1, // 主链路段：视觉识别=1.1、续流=1.2
			"__current_channel":  "main",
			"__vision_v2_active": true,
			"__vision_v2_route": &types.CapabilityRoute{ViaOptions: []types.ViaOption{
				{ChannelIDs: []string{"bailian"}, ViaModel: "qwen3-vl-flash-2026-01-22"},
			}},
		},
	}
	st0 := &toolLoopState{
		format: formatChat,
		calls:  []ToolCall{{ID: "call_1", Name: "look_at_image", ImageID: imgID, Prompt: "看颜色"}},
		round:  0,
	}

	handled, err := svc.executeToolLoop(pipe, st0)
	if err != nil {
		t.Fatalf("executeToolLoop 报错: %v", err)
	}
	if !handled {
		t.Error("executeToolLoop 应返回 handled=true（本轮有 look_at_image）")
	}
	if len(rl.attempts) != 3 {
		t.Fatalf("attempts = %d 条, want 3（step1.1 running + step1.1 success + step1.2 续流）", len(rl.attempts))
	}
	// step1.1：running 占位在前，success 覆盖在后。
	run := rl.attempts[0]
	if run.StepNo != "1.1" || run.Action != "视觉识别" || run.Result != "running" {
		t.Errorf("attempts[0] = %+v, want StepNo=1.1 Action=视觉识别 Result=running（running 占位）", run)
	}
	visionStep := rl.attempts[1]
	if visionStep.StepNo != "1.1" || visionStep.Action != "视觉识别" || visionStep.Result != "success" {
		t.Errorf("attempts[1] = %+v, want StepNo=1.1 Action=视觉识别 Result=success", visionStep)
	}
	if v, _ := visionStep.Metadata["called_via_tool"].(bool); !v {
		t.Errorf("视觉识别 called_via_tool = %v, want true", visionStep.Metadata["called_via_tool"])
	}
	if v, _ := visionStep.Metadata["tool"].(string); v != "look_at_image" {
		t.Errorf("视觉识别 tool = %v, want look_at_image", visionStep.Metadata["tool"])
	}
	if v, _ := visionStep.Metadata["cache_hit"].(bool); v {
		t.Error("视觉识别 cache_hit = true, want false")
	}
	// step1.2：续流（主请求的子步骤，兄弟关系）。
	contStep := rl.attempts[2]
	if contStep.StepNo != "1.2" || contStep.Action != "首次尝试" || contStep.Result != "success" {
		t.Errorf("attempts[2] = %+v, want StepNo=1.2 Action=首次尝试 Result=success", contStep)
	}
	if contStep.ChannelID != "main" {
		t.Errorf("续流 ChannelID = %q, want main", contStep.ChannelID)
	}
	// 主链路段不被视觉推进：__route_step 仍 1；子段计数器到 2。
	if v, _ := pipe.Metadata["__route_step"].(int); v != 1 {
		t.Errorf("__route_step = %v, want 1（主链路段不被视觉子步骤推进）", pipe.Metadata["__route_step"])
	}
	if v, _ := pipe.Metadata["__vision_sub_step"].(int); v != 2 {
		t.Errorf("__vision_sub_step = %v, want 2（识别+续流两个子步骤）", pipe.Metadata["__vision_sub_step"])
	}
	if mainCalls.Load() != 1 {
		t.Errorf("主上游应被调 1 次（仅续流），实际 %d", mainCalls.Load())
	}
}

// TestBuildToolMessagesChat 验证 chat 格式的消息构造：assistant.tool_calls + role=tool 结果。
func TestBuildToolMessagesChat(t *testing.T) {
	calls := []ToolCall{
		{ID: "call_1", Name: "look_at_image", ImageID: "aaa111", Prompt: "看颜色"},
		{ID: "call_2", Name: "look_at_image", ImageID: "bbb222", Prompt: "看文字"},
	}
	am := buildAssistantToolMessage(calls, formatChat)
	if am["role"] != "assistant" {
		t.Errorf("assistant role = %v, want assistant", am["role"])
	}
	tcs, ok := am["tool_calls"].([]any)
	if !ok || len(tcs) != 2 {
		t.Fatalf("tool_calls = %#v, want 2 条", am["tool_calls"])
	}
	first := tcs[0].(map[string]any)
	if first["id"] != "call_1" || first["type"] != "function" {
		t.Errorf("tool_call 头部 = %#v", first)
	}
	fn := first["function"].(map[string]any)
	if fn["name"] != "look_at_image" {
		t.Errorf("function name = %v", fn["name"])
	}
	var args struct {
		ImageID string `json:"image_id"`
		Prompt  string `json:"prompt"`
	}
	if err := json.Unmarshal([]byte(fn["arguments"].(string)), &args); err != nil {
		t.Fatalf("arguments 非 JSON: %v", err)
	}
	if args.ImageID != "aaa111" || args.Prompt != "看颜色" {
		t.Errorf("arguments = %+v", args)
	}

	rm := buildToolResultMessage(calls[0], "图片描述", formatChat).(map[string]any)
	if rm["role"] != "tool" || rm["tool_call_id"] != "call_1" || rm["content"] != "图片描述" {
		t.Errorf("工具结果消息 = %#v", rm)
	}
}

// TestBuildToolMessagesClaude 验证 claude 格式：content 含 tool_use 块 + role=user 的 tool_result。
func TestBuildToolMessagesClaude(t *testing.T) {
	calls := []ToolCall{{ID: "toolu_1", Name: "look_at_image", ImageID: "aaa111", Prompt: "看颜色"}}
	am := buildAssistantToolMessage(calls, formatClaude)
	blocks, ok := am["content"].([]any)
	if !ok || len(blocks) != 1 {
		t.Fatalf("content = %#v", am["content"])
	}
	first := blocks[0].(map[string]any)
	if first["type"] != "tool_use" || first["id"] != "toolu_1" || first["name"] != "look_at_image" {
		t.Errorf("tool_use 块 = %#v", first)
	}
	in := first["input"].(map[string]any)
	if in["image_id"] != "aaa111" || in["prompt"] != "看颜色" {
		t.Errorf("input = %#v", in)
	}

	rm := buildToolResultMessage(calls[0], "图片描述", formatClaude).(map[string]any)
	if rm["role"] != "user" {
		t.Errorf("role = %v, want user", rm["role"])
	}
	rc := rm["content"].([]any)
	tr := rc[0].(map[string]any)
	if tr["type"] != "tool_result" || tr["tool_use_id"] != "toolu_1" || tr["content"] != "图片描述" {
		t.Errorf("tool_result 块 = %#v", tr)
	}
}

// TestBuildToolMessagesResponses 验证 responses 格式：input 追加 function_call + function_call_output。
func TestBuildToolMessagesResponses(t *testing.T) {
	calls := []ToolCall{{ID: "fc_1", Name: "look_at_image", ImageID: "aaa111", Prompt: "看颜色"}}
	am := buildAssistantToolMessage(calls, formatResponses)
	if am["type"] != "function_call" || am["id"] != "fc_1" || am["call_id"] != "fc_1" || am["name"] != "look_at_image" {
		t.Errorf("function_call item = %#v", am)
	}
	var args struct {
		ImageID string `json:"image_id"`
		Prompt  string `json:"prompt"`
	}
	if err := json.Unmarshal([]byte(am["arguments"].(string)), &args); err != nil {
		t.Fatalf("arguments 非 JSON: %v", err)
	}
	if args.ImageID != "aaa111" || args.Prompt != "看颜色" {
		t.Errorf("arguments = %+v", args)
	}

	rm := buildToolResultMessage(calls[0], "图片描述", formatResponses).(map[string]any)
	if rm["type"] != "function_call_output" || rm["call_id"] != "fc_1" || rm["output"] != "图片描述" {
		t.Errorf("function_call_output = %#v", rm)
	}
}

// mockVisionRouteLog 记录 route-log 调用，供视觉 failover 的 attempt 断言。
type mockVisionRouteLog struct {
	attempts []contracts.RouteAttempt
}

func (m *mockVisionRouteLog) Start(ctx context.Context, r contracts.RouteRequest) error { return nil }
func (m *mockVisionRouteLog) Attempt(ctx context.Context, a contracts.RouteAttempt) (int64, error) {
	m.attempts = append(m.attempts, a)
	return int64(len(m.attempts)), nil
}
func (m *mockVisionRouteLog) Finish(ctx context.Context, f contracts.RouteFinish) error { return nil }
func (m *mockVisionRouteLog) List(ctx context.Context, f contracts.RouteLogFilter) ([]contracts.RouteRequestView, error) {
	return nil, nil
}
func (m *mockVisionRouteLog) Detail(ctx context.Context, id string) (contracts.RouteRequestView, error) {
	return contracts.RouteRequestView{}, nil
}
func (m *mockVisionRouteLog) Clear(ctx context.Context, t time.Time) error { return nil }
func (m *mockVisionRouteLog) SelfHeal(ctx context.Context, id string, threshold time.Duration) error {
	return nil
}

// TestDescribeWithFailoverOrder 验证 failover 顺序：路由 3 个候选，第 1 个渠道 500、第 2 个成功，
// 只调用到第 2 个、返回第 2 个结果及第 2 个渠道 id（route-log 已改由调用方写，本方法不再内部写 attempt）。
func TestDescribeWithFailoverOrder(t *testing.T) {
	setVisionConfig(t)

	var srv1Calls atomic.Int32
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv1Calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, "boom")
	}))
	defer srv1.Close()
	var srv2Calls atomic.Int32
	var gotModel atomic.Value
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv2Calls.Add(1)
		if body, err := io.ReadAll(r.Body); err == nil {
			var req struct {
				Model string `json:"model"`
			}
			if json.Unmarshal(body, &req) == nil {
				gotModel.Store(req.Model)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"第二轮描述"}}]}`)
	}))
	defer srv2.Close()
	var srv3Calls atomic.Int32
	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv3Calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"第三轮描述"}}]}`)
	}))
	defer srv3.Close()

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
	svc := NewService(st, repo, slog.Default())
	svc.cacheDir = t.TempDir()
	if err := repo.ReplaceChannels(context.Background(), []db.Channel{
		{ID: "ch1", Name: "c1", BaseURL: srv1.URL, ManualEnabled: true},
		{ID: "ch2", Name: "c2", BaseURL: srv2.URL, ManualEnabled: true},
		{ID: "ch3", Name: "c3", BaseURL: srv3.URL, ManualEnabled: true},
	}); err != nil {
		t.Fatalf("ReplaceChannels: %v", err)
	}
	log := &mockVisionRouteLog{}
	svc.routeLog = log

	imgID, err := svc.SaveImageDataURI(tinyPNGDataURI)
	if err != nil {
		t.Fatalf("SaveImageDataURI: %v", err)
	}
	route := &types.CapabilityRoute{ViaOptions: []types.ViaOption{
		{ChannelIDs: []string{"ch1"}, ViaModel: "doubao-seed-2-0-mini-260428"},
		{ChannelIDs: []string{"ch2"}, ViaModel: "qwen3-vl-flash-2026-01-22"},
		{ChannelIDs: []string{"ch3"}, ViaModel: "qwen3.7-flash-2026-07-15"},
	}}

	text, successChannelID, err := svc.describeWithFailover(context.Background(), imgID, "看颜色", nil, route)
	if err != nil {
		t.Fatalf("describeWithFailover 报错: %v", err)
	}
	if text != "第二轮描述" {
		t.Errorf("返回文本 = %q, want 第二轮描述（第 1 个候选失败后取第 2 个）", text)
	}
	if srv1Calls.Load() != 1 {
		t.Errorf("srv1 被调 %d 次, want 1（首个候选失败一次）", srv1Calls.Load())
	}
	if srv2Calls.Load() != 1 {
		t.Errorf("srv2 被调 %d 次, want 1（第 2 个候选成功）", srv2Calls.Load())
	}
	if srv3Calls.Load() != 0 {
		t.Errorf("srv3 不应被调用（第 2 个候选已成功），实际 %d", srv3Calls.Load())
	}
	if m, ok := gotModel.Load().(string); !ok || m != "qwen3-vl-flash-2026-01-22" {
		t.Errorf("成功请求 model = %v, want qwen3-vl-flash-2026-01-22（第 2 个候选的 viaModel）", m)
	}
	if successChannelID != "ch2" {
		t.Errorf("successChannelID = %q, want ch2（第 2 个成功候选渠道 id）", successChannelID)
	}
}

// TestDescribeWithFailoverCacheHit 验证缓存命中：不调用任何视觉 server、不进 failover。
func TestDescribeWithFailoverCacheHit(t *testing.T) {
	setVisionConfig(t)

	var visionCalls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		visionCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"不应返回"}}]}`)
	}))
	defer ts.Close()

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
	svc := NewService(st, repo, slog.Default())
	svc.cacheDir = t.TempDir()
	if err := repo.ReplaceChannels(context.Background(), []db.Channel{
		{ID: "ch1", Name: "c1", BaseURL: ts.URL, ManualEnabled: true},
	}); err != nil {
		t.Fatalf("ReplaceChannels: %v", err)
	}

	imgID, err := svc.SaveImageDataURI(tinyPNGDataURI)
	if err != nil {
		t.Fatalf("SaveImageDataURI: %v", err)
	}
	// 预写缓存。
	key := visionCacheKey(imgID, "看颜色")
	if err := svc.writeCache(key, "缓存描述"); err != nil {
		t.Fatalf("writeCache: %v", err)
	}

	route := &types.CapabilityRoute{ViaOptions: []types.ViaOption{
		{ChannelIDs: []string{"ch1"}, ViaModel: "qwen3-vl-flash-2026-01-22"},
	}}
	text, successChannelID, err := svc.describeWithFailover(context.Background(), imgID, "看颜色", nil, route)
	if err != nil {
		t.Fatalf("describeWithFailover 报错: %v", err)
	}
	if text != "缓存描述" {
		t.Errorf("返回文本 = %q, want 缓存描述", text)
	}
	if successChannelID != "" {
		t.Errorf("successChannelID = %q, want 空（缓存命中不关联渠道）", successChannelID)
	}
	if visionCalls.Load() != 0 {
		t.Errorf("缓存命中不应调用视觉 server, 实际 %d 次", visionCalls.Load())
	}
}
