package servercore

import (
	"testing"
	"time"
)

// TestTriggerShutdown 验证 TriggerShutdown 触发全局退出 context，且多次调用幂等（不 panic）。
func TestTriggerShutdown(t *testing.T) {
	TriggerShutdown()

	// 触发后 Done 应已关闭（立即返回）。
	select {
	case <-shutdownCtx.Done():
	default:
		t.Fatal("TriggerShutdown 后 shutdownCtx 未取消")
	}

	// 幂等：重复调用不应 panic。
	TriggerShutdown()
	TriggerShutdown()

	// 再次确认仍处于已取消状态。
	select {
	case <-shutdownCtx.Done():
	default:
		t.Fatal("重复 TriggerShutdown 后 context 状态异常")
	}
}

// TestTriggerShutdownTimeout 兜底：即使触发失败也不应永久阻塞（防止测试悬挂）。
func TestTriggerShutdownTimeout(t *testing.T) {
	done := make(chan struct{})
	go func() {
		TriggerShutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("TriggerShutdown 阻塞超过 2s")
	}
}
