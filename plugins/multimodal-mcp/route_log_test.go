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
	// lastStartCtxErr / lastAttemptCtxErr / lastFinishCtxErr 记录调用时刻 ctx 的错误，
	// 用于断言 runRecognition 用了独立 ctx（不应受主 ctx 取消/超时影响）。
	lastStartCtxErr   error
	lastAttemptCtxErr error
	lastFinishCtxErr  error
}

func (f *fakeRouteLog) Start(ctx context.Context, req contracts.RouteRequest) error {
	f.lastStartCtxErr = ctx.Err()
	f.started = append(f.started, req)
	return nil
}
func (f *fakeRouteLog) Attempt(ctx context.Context, a contracts.RouteAttempt) (int64, error) {
	f.lastAttemptCtxErr = ctx.Err()
	f.attempts = append(f.attempts, a)
	return int64(len(f.attempts)), nil
}
func (f *fakeRouteLog) Finish(ctx context.Context, fin contracts.RouteFinish) error {
	f.lastFinishCtxErr = ctx.Err()
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

// TestRouteLogIndependentOfParentCtx 验证：route-log 写入用独立 ctx，主 ctx 在识别阶段
// 超时/取消也不应阻断 Attempt/Finish 落地（避免孤儿 request）。
// 模拟场景：识别调用因超时失败 → 主 ctx 在 recognize() 返回时已 DeadlineExceeded，
// 若写日志仍用主 ctx，db 调用会立刻 DeadlineExceeded 失败；用独立 ctx 才能正常写完。
func TestRouteLogIndependentOfParentCtx(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Tools[0].Enabled = true
	cfg.Tools[0].Model = "qwen3-vl"
	s := newConfigService(t, cfg)
	fw := s.gw.(*fakeRecogForwarder)
	fw.err = context.DeadlineExceeded // 识别走子请求时返回超时
	rl := &fakeRouteLog{}
	s.route = rl

	// 模拟主 ctx 已死（识别超时后状态）。
	parentCtx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _ = s.understandImage(parentCtx, map[string]any{"image": "https://example.com/a.png"})

	// 主 ctx 已取消 → 如果写日志仍用主 ctx，ctx.Err() != nil；
	// 用独立 ctx 后写日志时的 ctx 应当仍有效（ctx.Err() == nil）。
	if rl.lastStartCtxErr != nil {
		t.Errorf("Start 写入 ctx 已死（%v）——主 ctx 被错误地传给 route-log", rl.lastStartCtxErr)
	}
	if rl.lastAttemptCtxErr != nil {
		t.Errorf("Attempt 写入 ctx 已死（%v）——主 ctx 透传，识别超时后日志写不进去", rl.lastAttemptCtxErr)
	}
	if rl.lastFinishCtxErr != nil {
		t.Errorf("Finish 写入 ctx 已死（%v）——主 ctx 透传，识别超时后日志写不进去", rl.lastFinishCtxErr)
	}
	// 关键：Attempt 和 Finish 都被调用了一次（日志没落空、没留孤儿）。
	if len(rl.attempts) != 1 || len(rl.finished) != 1 {
		t.Errorf("attempt=%d finish=%d, want 1/1", len(rl.attempts), len(rl.finished))
	}
}
