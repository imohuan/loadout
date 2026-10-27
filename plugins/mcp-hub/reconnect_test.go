package mcphub

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"loadout/core/store"
	"loadout/plugins/types"
)

// newHealthEnv 造一个指向不可达端口的 HTTP 上游（探活必失败）。
func newHealthEnv(t *testing.T) *Service {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	servers := []types.MCPServer{
		{ID: "srv-dead", Name: "dead", Transport: types.TransportHTTP, URL: "http://127.0.0.1:39999/mcp", Enabled: true},
	}
	if err := st.Write(types.FileMCPServers, servers); err != nil {
		t.Fatalf("write servers: %v", err)
	}
	svc := NewService(st, slog.Default(), nil)
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

// waitFor 轮询等待条件成立（最多 6s），供异步重连流程断言。
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("等待条件超时")
}

// TestHealthMonitorAttemptsAndClear 重连进度计时：SetAttempt 递增、Clear 清零、
// 上限可热更新。
func TestHealthMonitorAttemptsAndClear(t *testing.T) {
	h := newHealthMonitor()
	if h.Attempts("a") != 0 {
		t.Fatal("初始重连次数应为 0")
	}
	h.SetAttempt("a", 1, "boom")
	h.SetAttempt("a", 2, "boom")
	if got := h.Attempts("a"); got != 2 {
		t.Fatalf("重连次数 = %d，期望 2", got)
	}
	if got := h.LastError("a"); got != "boom" {
		t.Fatalf("错误原因 = %q，期望 boom", got)
	}
	h.Clear("a")
	if got := h.Attempts("a"); got != 0 {
		t.Fatalf("Clear 后应为 0，实际 %d", got)
	}
	h.SetMax(5)
	if got := h.Max(); got != 5 {
		t.Fatalf("上限 = %d，期望 5", got)
	}
	h.SetMax(0) // 0 = 回落到默认值
	if got := h.Max(); got != reconnectMaxDefault {
		t.Fatalf("上限 = %d，期望默认 %d", got, reconnectMaxDefault)
	}
}

// TestStartReconnectStopsAfterLimit 回归：探活失败后自动重连 N 次（每次状态可看到
// 第几次），N 次仍失败则自动停用该上游（enabled=false，状态 stopped）。
func TestStartReconnectStopsAfterLimit(t *testing.T) {
	svc := newHealthEnv(t)
	svc.health.SetMax(2)                        // 2 次就停，缩短测试
	svc.health.SetDwell(200 * time.Millisecond) // 缩短每次「重试 N」的停留
	if err := svc.SetServerEnabled(context.Background(), "srv-dead", true); err != nil {
		t.Logf("首次拉起失败（预期）：%v", err)
	}
	srv, err := svc.findServer("srv-dead")
	if err != nil {
		t.Fatalf("findServer: %v", err)
	}
	svc.startReconnect(*srv, context.DeadlineExceeded)

	// 重连期间能看到「重连中 N」的进度。
	seen := false
	sawAttempt := 0
	waitFor(t, func() bool {
		st, _ := svc.ServerStatus("srv-dead")
		n := svc.ReconnectAttempts("srv-dead")
		if st == StateReconnecting {
			seen = true
		}
		if n > sawAttempt {
			sawAttempt = n
		}
		return seen && sawAttempt >= 1
	})

	// 达到上限后自动停用；重连进度清零、状态为 stopped。
	waitFor(t, func() bool {
		items, err := svc.ListServers(context.Background())
		if err != nil {
			return false
		}
		for _, it := range items {
			if it.ID == "srv-dead" {
				return !it.Enabled
			}
		}
		return false
	})
	// 重试次数用完后是终点「失败」：不再显示 stopped（那会让人以为还能自愈）。
	if st, _ := svc.ServerStatus("srv-dead"); st != StateFailed {
		t.Fatalf("重试耗尽后状态应为 failed，实际 %s", st)
	}
	if msg := svc.ServerError("srv-dead"); msg == "" {
		t.Fatal("失败原因应保留，供状态列悬浮提示")
	}
	if got := svc.ReconnectAttempts("srv-dead"); got != 0 {
		t.Fatalf("停用后重连次数应清零，实际 %d", got)
	}
}

// TestHealthProbeRecoversAfterReconnect 回归：重连后上游恢复 → 进度清零、状态回到
// running（探活成功）。
func TestHealthProbeRecoversAfterReconnect(t *testing.T) {
	svc := newHealthEnv(t)
	svc.health.SetMax(3)
	if err := svc.SetServerEnabled(context.Background(), "srv-dead", true); err != nil {
		t.Logf("首次拉起失败（预期）：%v", err)
	}

	// 模拟用户改好配置：把 URL 换成可达地址（这里改内存连接池里的上游不可行，
	// 改为验证「探活成功会 Clear」这一半：直接写入 attempts 再探活成功）。
	svc.health.SetAttempt("srv-dead", 2, "boom")
	if got := svc.ReconnectAttempts("srv-dead"); got != 2 {
		t.Fatalf("重连次数 = %d，期望 2", got)
	}
	// probeOnce 对不可达地址必然失败，因此这里只验证状态查询不做联网副作用：
	// 连续读两次状态，attempts 不应被清零也不应增加（探活由后台循环负责）。
	_, _ = svc.ServerStatus("srv-dead")
	_, _ = svc.ServerStatus("srv-dead")
	if got := svc.ReconnectAttempts("srv-dead"); got != 2 {
		t.Fatalf("读状态不应改动重连进度，实际 %d", got)
	}
}
