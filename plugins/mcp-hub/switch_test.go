package mcphub

import (
	"context"
	"log/slog"
	"testing"

	"loadout/core/store"
	"loadout/plugins/types"
)

// TestSetServerEnabledFalseClosesUpstream 回归：手动关闭必须真的断开连接/杀掉进程。
// 之前「关掉左侧进程、上游服务没变化」就是关闭分支没有 Close 造成的。
func TestSetServerEnabledFalseClosesUpstream(t *testing.T) {
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

	// 先建一个连接（失败也会留下 upstream 记录），再关闭。
	_ = svc.SetServerEnabled(context.Background(), "srv-x", true)
	if svc.getUpstreamByID("srv-x") == nil {
		t.Fatal("拉起后连接池应有该上游")
	}
	if err := svc.SetServerEnabled(context.Background(), "srv-x", false); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
	if up := svc.getUpstreamByID("srv-x"); up != nil {
		t.Fatal("关闭后连接池不应再有该上游（否则进程/连接还在跑）")
	}
}
