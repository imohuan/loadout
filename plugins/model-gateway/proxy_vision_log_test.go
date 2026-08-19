package modelgateway

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"loadout/core/db"
	"loadout/core/store"
	"loadout/plugins/contracts"
	routelog "loadout/plugins/route-log"
)

// TestHandleProxyFlushVisionAttempt 端到端验证：before-upstream hook（vision 插件）
// 把视觉识别结果暂存到 pipe.Metadata 后，model-gateway 在 route-log Start 之后
// flush 一条 step_no=-1、action=视觉识别 的 attempt，与主链路共用同一 request_id，
// 且主链路 step_no 仍从 1 开始、action 仍为「首次尝试」。
func TestHandleProxyFlushVisionAttempt(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	ctx := newMockCtx()
	svc := NewService(st, slog.Default(), ctx)

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("db.OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	rl := routelog.NewService(database, slog.Default())
	repo, err := db.NewRepository(database)
	if err != nil {
		t.Fatalf("db.NewRepository: %v", err)
	}
	svc.SetRoutingServices(database, &mockHealth{}, rl)

	// 模拟 vision 插件：before-upstream hook 内暂存视觉识别结果。
	ctx.On(ProxyBeforeUpstream, func(payload any) (any, error) {
		pipe := payload.(*ProxyPipeline)
		pipe.Metadata[contracts.MetadataKeyVisionAttempt] = contracts.VisionAttemptLog{
			ViaModel:   "qwen-vl-max",
			ChannelID:  "echo",
			Result:     "success",
			StartedAt:  time.Now(),
			Duration:   contracts.DurationMS(120 * time.Millisecond),
			ImageCount: 2,
		}
		return pipe, nil
	})

	upstream, _ := newEchoServer(t, `{"choices":[{"message":{"content":"hi"}}]}`, 200, nil)
	defer upstream.Close()
	if err := repo.ReplaceChannels(context.Background(), []db.Channel{
		{ID: "echo", Name: "回显渠道", BaseURL: upstream.URL, ManualEnabled: true},
	}); err != nil {
		t.Fatalf("写渠道失败: %v", err)
	}

	body := `{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`
	rr := doProxy(t, svc, "POST", "chat/completions", "", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("转发应成功，实际 %d: %s", rr.Code, rr.Body.String())
	}

	logs, err := rl.List(context.Background(), contracts.RouteLogFilter{})
	if err != nil {
		t.Fatalf("查询 route log 失败: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("应有 1 条请求日志，实际 %d", len(logs))
	}
	log := logs[0]
	if log.RequestID == "" || log.RequestID != rr.Header().Get("X-Request-Id") {
		t.Fatalf("route-log request_id 与响应头不一致: %q vs %q", log.RequestID, rr.Header().Get("X-Request-Id"))
	}
	// attempts 只在 Detail 里加载，前端展开行时才查。
	detail, err := rl.Detail(context.Background(), log.RequestID)
	if err != nil {
		t.Fatalf("查询 route log 详情失败: %v", err)
	}
	var visionSeen, mainSeen bool
	for _, a := range detail.Attempts {
		switch a.StepNo {
		case -1:
			visionSeen = true
			if a.Action != "视觉识别" || a.Model != "qwen-vl-max" || a.ChannelID != "echo" || a.Result != "success" {
				t.Fatalf("视觉 attempt 内容不符: %+v", a)
			}
		case 1:
			mainSeen = true
			if a.Action != "首次尝试" {
				t.Fatalf("主链路首次尝试 action 应为「首次尝试」，实际 %q", a.Action)
			}
		}
	}
	if !visionSeen {
		t.Fatal("缺少 step_no=-1 的视觉识别 attempt")
	}
	if !mainSeen {
		t.Fatal("缺少主链路 attempt（step_no=1）")
	}
}

// TestHandleProxyFlushVisionAttemptFailed 视觉识别全部失败：hook 返回错误走
// proxyRejectedLog，暂存的 failed 视觉结果也要落库，且请求最终标记 failed。
func TestHandleProxyFlushVisionAttemptFailed(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	ctx := newMockCtx()
	svc := NewService(st, slog.Default(), ctx)

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("db.OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	rl := routelog.NewService(database, slog.Default())
	svc.SetRoutingServices(database, &mockHealth{}, rl)

	ctx.On(ProxyBeforeUpstream, func(payload any) (any, error) {
		pipe := payload.(*ProxyPipeline)
		pipe.Metadata[contracts.MetadataKeyVisionAttempt] = contracts.VisionAttemptLog{
			ViaModel:     "qwen-vl-max",
			Result:       "failed",
			StartedAt:    time.Now(),
			Duration:     contracts.DurationMS(50 * time.Millisecond),
			ErrorMessage: "vision: 所有渠道均失败",
			ImageCount:   1,
		}
		return pipe, fmt.Errorf("vision: 视觉能力调用失败")
	})

	body := `{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`
	rr := doProxy(t, svc, "POST", "chat/completions", "", body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("视觉失败应拒绝请求，实际 %d", rr.Code)
	}

	logs, err := rl.List(context.Background(), contracts.RouteLogFilter{})
	if err != nil {
		t.Fatalf("查询 route log 失败: %v", err)
	}
	if len(logs) != 1 || logs[0].Result != "failed" {
		t.Fatalf("应有 1 条 failed 请求日志，实际 %+v", logs)
	}
	detail, err := rl.Detail(context.Background(), logs[0].RequestID)
	if err != nil {
		t.Fatalf("查询详情失败: %v", err)
	}
	var visionSeen bool
	for _, a := range detail.Attempts {
		if a.StepNo == -1 {
			visionSeen = true
			if a.Action != "视觉识别" || a.Model != "qwen-vl-max" || a.Result != "failed" {
				t.Fatalf("视觉失败 attempt 内容不符: %+v", a)
			}
			if a.ErrorMessage == "" {
				t.Fatal("视觉失败 attempt 应带错误信息")
			}
		}
	}
	if !visionSeen {
		t.Fatal("缺少视觉识别失败 attempt")
	}
}
