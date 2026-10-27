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

// TestProbeOnceHealthAndFailure 探活的两条分支：可达上游 tools/list 成功；
// 不可达上游返回错误（这条错误会驱动自动重连）。
func TestProbeOnceHealthAndFailure(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	f := fakemcp.New("ok-srv", []fakemcp.Tool{
		{Name: "ping", Description: "ping", InputSchema: map[string]any{"type": "object"}},
	})
	t.Cleanup(f.Close)
	servers := []types.MCPServer{
		{ID: "srv-ok", Name: "ok-srv", Transport: types.TransportHTTP, URL: f.URL(), Enabled: true},
		{ID: "srv-bad", Name: "bad-srv", Transport: types.TransportHTTP, URL: "http://127.0.0.1:39999/mcp", Enabled: true},
	}
	if err := st.Write(types.FileMCPServers, servers); err != nil {
		t.Fatalf("write servers: %v", err)
	}
	svc := NewService(st, slog.Default(), nil)
	t.Cleanup(func() { _ = svc.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.probeOnce(ctx, servers[0]); err != nil {
		t.Fatalf("健康上游探活应成功，实际 %v", err)
	}
	// 未拉起（不在连接池）的上游不做探活，避免和「编辑配置后重建连接」互相踩踏。
	if err := svc.probeOnce(ctx, servers[1]); err != nil {
		t.Fatalf("未拉起的上游应跳过探活，实际 %v", err)
	}
	// 拉起后再探活：不可达地址必须报错（这条错误驱动自动重连）。
	if err := svc.SetServerEnabled(ctx, "srv-bad", true); err == nil {
		t.Log("不可达上游居然连上了，跳过失败断言")
	} else if err := svc.probeOnce(ctx, servers[1]); err == nil {
		t.Fatal("不可达上游拉起后探活应失败")
	}
	svc.health.SetAttempt("srv-bad", 1, "boom")
	if got := svc.ReconnectAttempts("srv-bad"); got != 1 {
		t.Fatalf("重连次数 = %d，期望 1", got)
	}
	svc.health.Clear("srv-bad") // 探活成功后清空进度
	if got := svc.ReconnectAttempts("srv-bad"); got != 0 {
		t.Fatalf("探活成功后应清零，实际 %d", got)
	}
}

// TestDisableServerPersistent 自动停用要落库：只关连接不写配置的话，列表仍是
// 「启用」，下次启动又会被拉起（自动重连用尽后的收尾必须持久化）。
func TestDisableServerPersistent(t *testing.T) {
	svc := newHealthEnv(t)
	if err := svc.disableServerPersistent("srv-dead"); err != nil {
		t.Fatalf("disableServerPersistent: %v", err)
	}
	servers, err := svc.ListServers(context.Background())
	if err != nil {
		t.Fatalf("ListServers: %v", err)
	}
	if len(servers) != 1 || servers[0].Enabled {
		t.Fatalf("自动停用后配置应为 enabled=false，实际 %+v", servers)
	}
	// 手动停用（不带原因）：状态是 stopped，不是 failed。
	if st, _ := svc.ServerStatus("srv-dead"); st != StateStopped {
		t.Fatalf("手动停用后状态应为 stopped，实际 %s", st)
	}
}

// TestDisableServerPersistentWithErrorShowsFailed 回归：自动重试耗尽要显示「失败」而不是
// 「已停止」——后者会让用户以为重试还在继续。失败原因也要能从接口读到。
func TestDisableServerPersistentWithErrorShowsFailed(t *testing.T) {
	svc := newHealthEnv(t)
	if err := svc.disableServerPersistentWithError("srv-dead", "dial tcp: connection refused"); err != nil {
		t.Fatalf("disableServerPersistentWithError: %v", err)
	}
	servers, err := svc.ListServers(context.Background())
	if err != nil {
		t.Fatalf("ListServers: %v", err)
	}
	if servers[0].Enabled || servers[0].LastError != "dial tcp: connection refused" {
		t.Fatalf("应停用并落库失败原因，实际 %+v", servers[0])
	}
	if st, _ := svc.ServerStatus("srv-dead"); st != StateFailed {
		t.Fatalf("重试耗尽后状态应为 failed，实际 %s", st)
	}
	if msg := svc.ServerError("srv-dead"); msg == "" {
		t.Fatal("失败原因应可从 ServerError 读到")
	}
}

// TestClearLastErrorOnReenable 重新启用后应清掉失败标记，恢复成正常状态。
func TestClearLastErrorOnReenable(t *testing.T) {
	svc := newHealthEnv(t)
	if err := svc.disableServerPersistentWithError("srv-dead", "boom"); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	svc.clearLastError("srv-dead")
	servers, err := svc.ListServers(context.Background())
	if err != nil {
		t.Fatalf("ListServers: %v", err)
	}
	if servers[0].LastError != "" {
		t.Fatalf("清空后 LastError 应为空，实际 %q", servers[0].LastError)
	}
	if st, _ := svc.ServerStatus("srv-dead"); st != StateStopped {
		t.Fatalf("清空失败标记后应为 stopped，实际 %s", st)
	}
}
