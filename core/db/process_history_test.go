package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"loadout/core/procreg"
)

// TestProcessHistoryPersistsAcrossRestart 验证进程历史经 SQLite 持久化后，
// 重建注册表（模拟后端重启）仍能通过 Snapshot 恢复完整历史，且不受内存上限限制。
func TestProcessHistoryPersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proc-history.db")

	// —— 第一次"运行期"：记录进程历史 ——
	db1 := mustOpen(t, path)
	repo1, err := NewProcessHistoryRepository(db1)
	if err != nil {
		t.Fatal(err)
	}
	r1 := procreg.New()
	r1.SetHistoryStore(repo1)

	// 模拟若干历史进程结束（status done / error 混合）。
	for i := 0; i < 7; i++ {
		pid := i + 1
		r1.RegisterExisting("proc-"+itoa(pid), "任务"+itoa(i), "skill", nil)
		var runErr error
		if i%2 == 1 {
			runErr = sql.ErrNoRows // 模拟失败进程
		}
		r1.Unregister("proc-"+itoa(pid), runErr)
	}
	// 一个仍在运行的进程。
	r1.RegisterExisting("proc-running", "运行中任务", "mcp", nil)

	// 首次 Snapshot 应包含 7 条历史 + 1 条运行中。
	first := r1.Snapshot()
	if len(first) != 8 {
		t.Fatalf("first snapshot = %d, want 8", len(first))
	}
	db1.Close()

	// —— 第二次"运行期"：模拟后端重启，历史全清空，仅持久化保留 ——
	db2 := mustOpen(t, path)
	defer db2.Close()
	repo2, err := NewProcessHistoryRepository(db2)
	if err != nil {
		t.Fatal(err)
	}
	r2 := procreg.New() // 全新注册表，内存历史为空
	r2.SetHistoryStore(repo2)

	restored := r2.Snapshot()
	if len(restored) != 7 {
		t.Fatalf("restored snapshot = %d, want 7 (6 done + 1 error 历史保留)", len(restored))
	}
	// 新→旧排序校验：最近结束的在前。
	if restored[0].ID != "proc-7" {
		t.Errorf("want newest first (proc-7), got %s", restored[0].ID)
	}
	// 混合状态都应保留（不管 running/error/done）。
	statusSeen := map[procreg.ProcStatus]bool{}
	for _, p := range restored {
		statusSeen[p.Status] = true
	}
	if !statusSeen[procreg.StatusError] || !statusSeen[procreg.StatusDone] {
		t.Errorf("history should retain all statuses, got %v", statusSeen)
	}
}

// TestProcessHistoryStoreInjectedSnapshot 验证注入 store 后，运行中进程仍实时
// 出现在 Snapshot 顶部，且历史来自 store。
func TestProcessHistoryStoreInjectedSnapshot(t *testing.T) {
	db := mustOpen(t, filepath.Join(t.TempDir(), "ph.db"))
	defer db.Close()
	repo, err := NewProcessHistoryRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	r := procreg.New()
	r.SetHistoryStore(repo)

	// 结束一条历史。
	r.RegisterExisting("p1", "旧任务", "skill", nil)
	r.Unregister("p1", nil)
	// 启动一条运行中。
	r.RegisterExisting("p2", "新任务", "skill", nil)

	snap := r.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("snapshot = %d, want 2", len(snap))
	}
	// 运行中进程应排在最前（其 StartedAt 最新）。
	if snap[0].ID != "p2" {
		t.Errorf("running process should be newest, got %s", snap[0].ID)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}
