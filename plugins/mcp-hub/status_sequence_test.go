package mcphub

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"loadout/core/store"
	"loadout/plugins/types"
)

// TestRetryPhaseNeverReportsFailed 回归「进行中 → 失败 → 进行中」：
// 只要重试阶段还没结束（attempts > 0），状态就必须是 reconnecting，
// 不能因为连接池里残留旧错误而短暂显示 failed。
func TestRetryPhaseNeverReportsFailed(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	servers := []types.MCPServer{
		{ID: "srv-seq", Name: "seq", Transport: types.TransportHTTP, URL: "http://127.0.0.1:39997/mcp", Enabled: true},
	}
	if err := st.Write(types.FileMCPServers, servers); err != nil {
		t.Fatalf("write servers: %v", err)
	}
	svc := NewService(st, slog.Default(), nil)
	t.Cleanup(func() { _ = svc.Close() })
	svc.health.SetMax(3)
	svc.health.SetDwell(120 * time.Millisecond)

	// 先拉起一次，制造连接池里的失败记录（这正是曾被误判成 failed 的源头）。
	_ = svc.SetServerEnabled(context.Background(), "srv-seq", true)

	srv := servers[0]
	svc.startReconnect(srv, context.DeadlineExceeded)

	checked := 0
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		n := svc.ReconnectAttempts("srv-seq")
		if n == 0 {
			break // 重试阶段结束（成功或耗尽）
		}
		stt, _ := svc.ServerStatus("srv-seq")
		if stt == StateFailed {
			t.Fatalf("重试进行中（第 %d 次）却报 failed，用户会以为重试已结束", n)
		}
		if stt != StateReconnecting {
			t.Fatalf("重试进行中应报 reconnecting，实际 %s", stt)
		}
		checked++
		time.Sleep(20 * time.Millisecond)
	}
	if checked == 0 {
		t.Fatal("没有采样到任何重试中的状态，测试前提被破坏")
	}
}
