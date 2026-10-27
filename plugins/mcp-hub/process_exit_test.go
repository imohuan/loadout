package mcphub

import (
	"log/slog"
	"testing"
	"time"

	"loadout/core/store"
	"loadout/plugins/types"
)

// TestStdioProcessExitStartsReconnectImmediately 回归：用户手动杀掉 stdio 进程后，
// 状态必须马上变成「重试 1」，而不是停在「失败」等下一轮探活（最多 15 秒）。
//
// 现象：进程刚被杀时 Alive() 已经是 false，ServerStatus 直接返回 failed，
// 前端 3 秒轮询先看到「失败」；重试要等后台探活下一轮才开始。
func TestStdioProcessExitStartsReconnectImmediately(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	// 用一个马上退出的命令模拟 stdio 进程，连接必然失败并可被观测。
	servers := []types.MCPServer{
		{ID: "srv-stdio", Name: "stdio-x", Transport: types.TransportStdio, Command: "cmd", Args: []string{"/c", "exit", "0"}, Enabled: true},
	}
	if err := st.Write(types.FileMCPServers, servers); err != nil {
		t.Fatalf("write servers: %v", err)
	}
	svc := NewService(st, slog.Default(), nil)
	t.Cleanup(func() { _ = svc.Close() })
	svc.health.SetDwell(300 * time.Millisecond)

	// 进程退出（或被用户杀掉）时，立刻登记重试并进入 reconnecting。
	svc.handleUpstreamProcessExit("srv-stdio")

	if stt, _ := svc.ServerStatus("srv-stdio"); stt != StateReconnecting {
		t.Fatalf("进程退出后应立即进入重试，实际 %s", stt)
	}
	if n := svc.ReconnectAttempts("srv-stdio"); n != 1 {
		t.Fatalf("应立即显示第 1 次重试，实际 %d", n)
	}
}
