package mcphub

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"loadout/core/store"
	"loadout/plugins/types"
	fakemcp "loadout/testkit/fake-mcp"
)

// TestRetryStateIsVisibleBeforeFirstAttempt 回归：重试一开始就必须能读到「重试 1」，
// 并且要停留足够久，让管理页 3 秒一次的轮询看得到。
//
// 之前第一次重试立刻执行：上游只要恢复得快（例如本地 stdio 进程重启），
// attempts 只存在几十毫秒，轮询直接跳过「重试 1」，用户只看到「失败 → 进行中」。
func TestRetryStateIsVisibleBeforeFirstAttempt(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	f := fakemcp.New("recover-srv", []fakemcp.Tool{
		{Name: "ping", Description: "ping", InputSchema: map[string]any{"type": "object"}},
	})
	t.Cleanup(f.Close)
	servers := []types.MCPServer{
		{ID: "srv-ok", Name: "recover-srv", Transport: types.TransportHTTP, URL: f.URL(), Enabled: true},
	}
	if err := st.Write(types.FileMCPServers, servers); err != nil {
		t.Fatalf("write servers: %v", err)
	}
	svc := NewService(st, slog.Default(), nil)
	t.Cleanup(func() { _ = svc.Close() })
	svc.health.SetDwell(1 * time.Second) // 缩短测试，但仍要明显可观察

	// 模拟一次探活失败：上游实际上是好的，第一次重试会瞬间成功。
	srv := servers[0]
	svc.startReconnect(srv, context.DeadlineExceeded)

	deadline := time.Now().Add(900 * time.Millisecond)
	for time.Now().Before(deadline) {
		stt, _ := svc.ServerStatus("srv-ok")
		if stt != StateReconnecting {
			t.Fatalf("重试开始后应保持 reconnecting 一段时间，实际 %s", stt)
		}
		if n := svc.ReconnectAttempts("srv-ok"); n != 1 {
			t.Fatalf("重试开始后应显示第 1 次，实际 %d", n)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// 重试成功后回到 running。
	waitFor(t, func() bool {
		stt, _ := svc.ServerStatus("srv-ok")
		return stt == StateRunning
	})
}
