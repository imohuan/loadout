package mcphub

import (
	"context"
	"log/slog"
	"testing"

	"loadout/core/store"
	"loadout/plugins/types"
)

// TestServerStatusPrefersReconnecting 回归：重试进行中时，状态必须是「重试 N」，
// 不能被残留的连接错误盖成「失败」。用户看到的现象就是失败与进行中来回跳、
// 中间的重试状态被吞掉。
func TestServerStatusPrefersReconnecting(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	servers := []types.MCPServer{
		{ID: "srv-x", Name: "x", Transport: types.TransportHTTP, URL: "http://127.0.0.1:39999/mcp", Enabled: true},
	}
	if err := st.Write(types.FileMCPServers, servers); err != nil {
		t.Fatalf("write servers: %v", err)
	}
	svc := NewService(st, slog.Default(), nil)
	t.Cleanup(func() { _ = svc.Close() })

	// 制造一个「连接池里有失败记录」的上游。
	_ = svc.SetServerEnabled(context.Background(), "srv-x", true)
	if up := svc.getUpstreamByID("srv-x"); up == nil || up.LastError() == "" {
		t.Fatal("测试前提：连接池里应有失败记录")
	}

	// 重试进度一旦存在，就必须报 reconnecting（哪怕旧错误还在）。
	svc.health.SetAttempt("srv-x", 1, "boom")
	if st, _ := svc.ServerStatus("srv-x"); st != StateReconnecting {
		t.Fatalf("重试中状态应为 reconnecting，实际 %s", st)
	}
	if n := svc.ReconnectAttempts("srv-x"); n != 1 {
		t.Fatalf("重试次数应为 1，实际 %d", n)
	}
}
