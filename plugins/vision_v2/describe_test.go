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
	"testing"

	"loadout/core/config"
	modelgateway "loadout/plugins/model-gateway"
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
