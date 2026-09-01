package multimodalmcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"loadout/plugins/contracts"
)

// fakeRouteLog 记录调用的 RouteLog mock，验证识别会写转发日志。
type fakeRouteLog struct {
	started  []contracts.RouteRequest
	attempts []contracts.RouteAttempt
	finished []contracts.RouteFinish
}

func (f *fakeRouteLog) Start(_ context.Context, req contracts.RouteRequest) error {
	f.started = append(f.started, req)
	return nil
}
func (f *fakeRouteLog) Attempt(_ context.Context, a contracts.RouteAttempt) (int64, error) {
	f.attempts = append(f.attempts, a)
	return int64(len(f.attempts)), nil
}
func (f *fakeRouteLog) Finish(_ context.Context, fin contracts.RouteFinish) error {
	f.finished = append(f.finished, fin)
	return nil
}
func (f *fakeRouteLog) List(context.Context, contracts.RouteLogFilter) (contracts.RouteLogPage, error) {
	return contracts.RouteLogPage{}, nil
}
func (f *fakeRouteLog) Detail(context.Context, string) (contracts.RouteRequestView, error) {
	return contracts.RouteRequestView{}, nil
}
func (f *fakeRouteLog) Clear(context.Context, time.Time) error { return nil }
func (f *fakeRouteLog) SelfHeal(context.Context, string, time.Duration) error {
	return nil
}

// TestUnderstandImageWritesRouteLog 验证：图片识别成功后写 route-log（Start/Attempt/Finish）。
func TestUnderstandImageWritesRouteLog(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Tools[0].Enabled = true
	cfg.Tools[0].Model = "qwen3-vl"
	s := newConfigService(t, cfg)
	fw := s.gw.(*fakeRecogForwarder)
	fw.respBody = []byte(`{"choices":[{"message":{"content":"一只猫"}}]}`)
	rl := &fakeRouteLog{}
	s.route = rl

	res, err := s.understandImage(context.Background(), map[string]any{"image": "https://example.com/a.png"})
	if err != nil {
		t.Fatalf("understandImage: %v", err)
	}
	if res == nil || !strings.Contains(res.Content[0].Text, "一只猫") {
		t.Fatalf("识别结果异常: %+v", res)
	}
	if len(rl.started) != 1 {
		t.Errorf("Start 调用次数 = %d, want 1", len(rl.started))
	}
	if len(rl.attempts) != 1 {
		t.Fatalf("Attempt 调用次数 = %d, want 1", len(rl.attempts))
	}
	att := rl.attempts[0]
	if att.Action != "图片识别" {
		t.Errorf("action = %q, want 图片识别", att.Action)
	}
	if att.Model != "qwen3-vl" {
		t.Errorf("model = %q, want qwen3-vl", att.Model)
	}
	if att.Result != "success" {
		t.Errorf("result = %q, want success", att.Result)
	}
	if att.Metadata["capability"] != "multimodal" {
		t.Errorf("metadata.capability = %v, want multimodal", att.Metadata["capability"])
	}
	if len(rl.finished) != 1 {
		t.Errorf("Finish 调用次数 = %d, want 1", len(rl.finished))
	}
	if rl.finished[0].Result != "success" {
		t.Errorf("finish result = %q, want success", rl.finished[0].Result)
	}
}

// TestUnderstandImageWritesRouteLogOnError 验证：识别失败时 attempt 记为 failed，且返回错误。
func TestUnderstandImageWritesRouteLogOnError(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Tools[0].Enabled = true
	cfg.Tools[0].Model = "qwen3-vl"
	s := newConfigService(t, cfg)
	fw := s.gw.(*fakeRecogForwarder)
	fw.err = context.DeadlineExceeded
	rl := &fakeRouteLog{}
	s.route = rl

	_, err := s.understandImage(context.Background(), map[string]any{"image": "https://example.com/a.png"})
	if err == nil {
		t.Fatal("识别失败时应返回错误")
	}
	if len(rl.attempts) != 1 {
		t.Fatalf("Attempt 调用次数 = %d, want 1", len(rl.attempts))
	}
	if rl.attempts[0].Result != "failed" {
		t.Errorf("attempt result = %q, want failed", rl.attempts[0].Result)
	}
	if len(rl.finished) != 1 {
		t.Fatalf("Finish 调用次数 = %d, want 1", len(rl.finished))
	}
	if rl.finished[0].Result != "failed" {
		t.Errorf("finish result = %q, want failed", rl.finished[0].Result)
	}
}
