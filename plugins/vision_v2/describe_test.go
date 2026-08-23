package visionv2

import (
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

	"loadout/core/config"
	"loadout/core/db"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// setVisionConfig 临时固定全局视觉开关，测试结束恢复。
func setVisionConfig(t *testing.T) {
	t.Helper()
	origCache, origCompress := config.VisionCacheEnabled, config.VisionCompressEnabled
	config.VisionCacheEnabled = true
	config.VisionCompressEnabled = true
	t.Cleanup(func() {
		config.VisionCacheEnabled = origCache
		config.VisionCompressEnabled = origCompress
	})
}

// TestDescribeImageCacheAndDirection 验证缓存优先 + 方向入 key：
// 首次 miss 真调视觉模型；同方向第二次缓存命中；换方向第三次 miss 再调一次。
func TestDescribeImageCacheAndDirection(t *testing.T) {
	setVisionConfig(t)

	var calls int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"这是一张红色海报"}}]}`))
	}))
	defer ts.Close()

	svc := &Service{cacheDir: t.TempDir(), lg: slog.Default()}
	ctx := context.Background()
	ch := modelgateway.ResolvedChannel{ID: "c1", BaseURL: ts.URL, APIKey: "k"}

	id, err := svc.SaveImageDataURI(tinyPNGDataURI)
	if err != nil {
		t.Fatalf("SaveImageDataURI 报错: %v", err)
	}

	// 首次：缓存 miss，真调视觉模型。
	text, hit, err := svc.DescribeImage(ctx, id, "看颜色", nil, ch, "qwen-vl-max")
	if err != nil {
		t.Fatalf("首次 DescribeImage 报错: %v", err)
	}
	if hit {
		t.Fatal("首次应 miss（hit=false）")
	}
	if !strings.Contains(text, "红色") {
		t.Fatalf("首次描述 = %q, want 含「红色」", text)
	}

	// 同方向第二次：缓存命中。
	_, hit, err = svc.DescribeImage(ctx, id, "看颜色", nil, ch, "qwen-vl-max")
	if err != nil {
		t.Fatalf("第二次 DescribeImage 报错: %v", err)
	}
	if !hit {
		t.Fatal("同方向第二次应命中缓存（hit=true）")
	}

	// 换方向：缓存 key 含方向，miss → 再调一次。
	text2, hit, err := svc.DescribeImage(ctx, id, "看布局", nil, ch, "qwen-vl-max")
	if err != nil {
		t.Fatalf("换方向 DescribeImage 报错: %v", err)
	}
	if hit {
		t.Fatal("换方向应 miss（hit=false）")
	}
	if text2 != text {
		t.Fatalf("换方向描述 = %q, want 与首次相同 %q", text2, text)
	}

	// 视觉模型只被调 2 次：首次 + 换方向。
	if calls != 2 {
		t.Fatalf("视觉模型被调 %d 次, want 2（首次+换方向，第二次应命中缓存）", calls)
	}
}

// TestCallVisionStream 验证流式：SSE delta 实时透传 + 累积完整文本。
func TestCallVisionStream(t *testing.T) {
	setVisionConfig(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w,
			"data: {\"choices\":[{\"delta\":{\"content\":\"描\"}}]}\n\n",
			"data: {\"choices\":[{\"delta\":{\"content\":\"述\"}}]}\n\n",
			"data: [DONE]\n\n",
		)
	}))
	defer ts.Close()

	svc := &Service{cacheDir: t.TempDir(), lg: slog.Default()}
	ch := modelgateway.ResolvedChannel{ID: "c1", BaseURL: ts.URL, APIKey: "k"}

	var deltas []string
	writer := func(s string) error { deltas = append(deltas, s); return nil }

	text, err := svc.callVision(context.Background(), tinyPNGDataURI, "看文字", ch, "qwen-vl-max", writer)
	if err != nil {
		t.Fatalf("callVision 报错: %v", err)
	}
	if text != "描述" {
		t.Fatalf("最终文本 = %q, want 描述", text)
	}
	if len(deltas) != 2 || strings.Join(deltas, "") != "描述" {
		t.Fatalf("streamWriter 收到 %v, want 2 个 delta 拼成「描述」", deltas)
	}
}

// TestCallVisionError 验证上游非 2xx 时 callVision 报错。
func TestCallVisionError(t *testing.T) {
	setVisionConfig(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer ts.Close()

	svc := &Service{cacheDir: t.TempDir(), lg: slog.Default()}
	ch := modelgateway.ResolvedChannel{ID: "c1", BaseURL: ts.URL, APIKey: "k"}

	if _, err := svc.callVision(context.Background(), tinyPNGDataURI, "看颜色", ch, "qwen-vl-max", nil); err == nil {
		t.Fatal("上游 500 时 callVision 应报错")
	}
}

// TestCallVisionUsesViaModel 验证 callVision 请求体 model 字段透传 viaModel；
// viaModel 为空时兜底 config.DefaultVisionModel。
func TestCallVisionUsesViaModel(t *testing.T) {
	setVisionConfig(t)

	var gotModels []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(body, &req) == nil {
			gotModels = append(gotModels, req.Model)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"描述"}}]}`))
	}))
	defer ts.Close()

	svc := &Service{cacheDir: t.TempDir(), lg: slog.Default()}
	ch := modelgateway.ResolvedChannel{ID: "c1", BaseURL: ts.URL, APIKey: "k"}
	ctx := context.Background()

	// 传入 viaModel：model 字段应等于它。
	if _, err := svc.callVision(ctx, tinyPNGDataURI, "看颜色", ch, "qwen3-vl-flash-2026-01-22", nil); err != nil {
		t.Fatalf("callVision 报错: %v", err)
	}
	// 空 viaModel：兜底 DefaultVisionModel。
	if _, err := svc.callVision(ctx, tinyPNGDataURI, "看颜色", ch, "", nil); err != nil {
		t.Fatalf("空 viaModel callVision 报错: %v", err)
	}

	if len(gotModels) != 2 {
		t.Fatalf("视觉 server 收到 %d 个请求, want 2", len(gotModels))
	}
	if gotModels[0] != "qwen3-vl-flash-2026-01-22" {
		t.Errorf("model[0] = %q, want qwen3-vl-flash-2026-01-22（透传 viaModel）", gotModels[0])
	}
	if gotModels[1] != config.DefaultVisionModel {
		t.Errorf("model[1] = %q, want 兜底 %q", gotModels[1], config.DefaultVisionModel)
	}
}

// TestDescribeWithFailoverReturnsChannelID 验证 failover 返回成功渠道 id：
// 路由 2 个候选（第 1 个 500、第 2 个成功）→ 返回文本来自第 2 个、successChannelID = 第 2 个渠道 id。
func TestDescribeWithFailoverReturnsChannelID(t *testing.T) {
	setVisionConfig(t)

	var srv1Calls atomic.Int32
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv1Calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, "boom")
	}))
	defer srv1.Close()
	var srv2Calls atomic.Int32
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv2Calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"第二渠道描述"}}]}`)
	}))
	defer srv2.Close()

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
	svc.SetGateway(newMockForwarder(map[string]string{"ch1": srv1.URL, "ch2": srv2.URL}))
	if err := repo.ReplaceChannels(context.Background(), []db.Channel{
		{ID: "ch1", Name: "c1", BaseURL: srv1.URL, ManualEnabled: true},
		{ID: "ch2", Name: "c2", BaseURL: srv2.URL, ManualEnabled: true},
	}); err != nil {
		t.Fatalf("ReplaceChannels: %v", err)
	}

	imgID, err := svc.SaveImageDataURI(tinyPNGDataURI)
	if err != nil {
		t.Fatalf("SaveImageDataURI: %v", err)
	}
	route := &types.CapabilityRoute{ViaOptions: []types.ViaOption{
		{ChannelIDs: []string{"ch1"}, ViaModel: "doubao-seed-2-0-mini-260428"},
		{ChannelIDs: []string{"ch2"}, ViaModel: "qwen3-vl-flash-2026-01-22"},
	}}

	text, successChannelID, _, err := svc.describeWithFailover(context.Background(), imgID, "看颜色", nil, route, "parent-test")
	if err != nil {
		t.Fatalf("describeWithFailover 报错: %v", err)
	}
	if !strings.Contains(text, "第二渠道") {
		t.Errorf("返回文本 = %q, want 来自第 2 个候选", text)
	}
	if successChannelID != "ch2" {
		t.Errorf("successChannelID = %q, want ch2（第 2 个成功候选渠道 id）", successChannelID)
	}
	if srv1Calls.Load() != 1 {
		t.Errorf("srv1 被调 %d 次, want 1（首个候选失败一次）", srv1Calls.Load())
	}
	if srv2Calls.Load() != 1 {
		t.Errorf("srv2 被调 %d 次, want 1（第 2 个候选成功）", srv2Calls.Load())
	}
}

// TestDescribeWithFailoverReturnsReqLogID 验证视觉识别子请求走网关后，request_log_id
// 从网关 pipe 的 __request_log_attempt_id 回填（request-log 在 ProxyBeforeAttempt 写入，
// mockForwarder 模拟该行为）——前端"完整日志"按钮依赖此字段。
func TestDescribeWithFailoverReturnsReqLogID(t *testing.T) {
	setVisionConfig(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"红色海报"}}]}`))
	}))
	defer srv.Close()

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
	svc.SetGateway(newMockForwarder(map[string]string{"ch1": srv.URL}))
	if err := repo.ReplaceChannels(context.Background(), []db.Channel{
		{ID: "ch1", Name: "c1", BaseURL: srv.URL, ManualEnabled: true},
	}); err != nil {
		t.Fatalf("ReplaceChannels: %v", err)
	}

	imgID, err := svc.SaveImageDataURI(tinyPNGDataURI)
	if err != nil {
		t.Fatalf("SaveImageDataURI: %v", err)
	}
	route := &types.CapabilityRoute{ViaOptions: []types.ViaOption{
		{ChannelIDs: []string{"ch1"}, ViaModel: "doubao-seed-2-0-mini-260428"},
	}}

	_, _, reqLogID, err := svc.describeWithFailover(context.Background(), imgID, "看颜色", nil, route, "parent-test")
	if err != nil {
		t.Fatalf("describeWithFailover 报错: %v", err)
	}
	if reqLogID != "mock-reqlog-ch1" {
		t.Errorf("reqLogID = %q, want mock-reqlog-ch1（request-log 关联 UUID 回填）", reqLogID)
	}
}

// TestDescribeWithFailoverCacheHitNoChannel 验证缓存命中：返回 (text, "", nil)，
// successChannelID 为空（缓存命中不关联渠道，调用方据此写 cache_hit=true），不调任何视觉 server。
func TestDescribeWithFailoverCacheHitNoChannel(t *testing.T) {
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
	if err := svc.writeCache(visionCacheKey(imgID, "看颜色"), "缓存描述"); err != nil {
		t.Fatalf("writeCache: %v", err)
	}
	route := &types.CapabilityRoute{ViaOptions: []types.ViaOption{
		{ChannelIDs: []string{"ch1"}, ViaModel: "qwen3-vl-flash-2026-01-22"},
	}}

	text, successChannelID, _, err := svc.describeWithFailover(context.Background(), imgID, "看颜色", nil, route, "parent-test")
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
