package procreg

import (
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func isWindows() bool { return runtime.GOOS == "windows" }

// sampleProc helper: 取全局最近广播的 update。
type recorder struct {
	mu     sync.Mutex
	events []Event
}

func (rc *recorder) collect(r *Registry) chan Event {
	ch := r.Subscribe()
	go func() {
		for ev := range ch {
			rc.mu.Lock()
			rc.events = append(rc.events, ev)
			rc.mu.Unlock()
		}
	}()
	return ch
}

func (rc *recorder) updates() []Event {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	out := make([]Event, len(rc.events))
	copy(out, rc.events)
	return out
}

func waitProc(t *testing.T, h *Handle) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- h.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for process to finish")
		return nil
	}
}

func TestRunRecordsAndFinishes(t *testing.T) {
	r := New()
	rc := &recorder{}
	rc.collect(r)

	// 用本进程可用的命令：Windows 用 cmd /c echo；非 Windows 用 echo。
	var o Options
	if isWindows() {
		o = Options{Name: "测试命令", Kind: "test", Cmd: "cmd", Args: []string{"/c", "echo hello && echo world"}}
	} else {
		o = Options{Name: "测试命令", Kind: "test", Cmd: "echo", Args: []string{"hello"}}
	}

	var got []string
	o.OnLog = func(line string) { got = append(got, line) }

	h, err := r.Run(o)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if err := waitProc(t, h); err != nil {
		t.Fatalf("command error: %v", err)
	}

	// 运行中已被移除，历史里有一条 done。
	snap := r.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("want 1 process (history), got %d", len(snap))
	}
	p := snap[0]
	if p.Status != StatusDone {
		t.Errorf("want status done, got %s", p.Status)
	}
	if p.Name != "测试命令" {
		t.Errorf("want name 测试命令, got %s", p.Name)
	}
	if !strings.Contains(p.Cmd, "echo") {
		t.Errorf("cmd not recorded: %q", p.Cmd)
	}
	if p.EndedAt.IsZero() {
		t.Errorf("EndedAt not set")
	}
}

func TestRunLogCaptured(t *testing.T) {
	r := New()
	var o Options
	if isWindows() {
		o = Options{Name: "日志", Cmd: "cmd", Args: []string{"/c", "echo LINE-A && echo LINE-B"}}
	} else {
		o = Options{Name: "日志", Cmd: "echo", Args: []string{"LINE-A"}}
	}
	h, err := r.Run(o)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	waitProc(t, h)

	p := r.Snapshot()[0]
	found := false
	for _, l := range p.Log {
		if strings.Contains(l, "LINE-A") {
			found = true
		}
	}
	if !found {
		t.Errorf("log did not capture LINE-A: %v", p.Log)
	}
}

func TestRunErrorStatus(t *testing.T) {
	r := New()
	var o Options
	if isWindows() {
		o = Options{Name: "失败命令", Cmd: "cmd", Args: []string{"/c", "exit 2"}}
	} else {
		o = Options{Name: "失败命令", Cmd: "false"}
	}
	h, err := r.Run(o)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	waitProc(t, h) // err != nil 是预期的

	p := r.Snapshot()[0]
	if p.Status != StatusError {
		t.Errorf("want status error, got %s", p.Status)
	}
}

func TestEmptyNameRejected(t *testing.T) {
	r := New()
	_, err := r.Run(Options{Cmd: "echo"})
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestRegisterUnregister(t *testing.T) {
	r := New()
	h := r.RegisterExisting("mcp:github", "MCP: github", "mcp", nil)
	if h.ID() != "mcp:github" {
		t.Fatalf("id mismatch: %s", h.ID())
	}
	if p := r.Running("mcp:github"); p == nil || p.Status != StatusRunning {
		t.Fatalf("expected running process")
	}

	r.Unregister("mcp:github", nil)
	if p := r.Running("mcp:github"); p != nil {
		t.Fatalf("should be removed from running")
	}
	snap := r.Snapshot()
	if len(snap) != 1 || snap[0].ID != "mcp:github" || snap[0].Status != StatusDone {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
}

func TestHistoryLimit(t *testing.T) {
	r := New()
	r.historyMax = 3
	for i := 0; i < 5; i++ {
		h := r.RegisterExisting("id-"+itoa(i), "p", "t", nil)
		_ = h
		r.Unregister("id-"+itoa(i), nil)
	}
	snap := r.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("want history limited to 3, got %d", len(snap))
	}
	if snap[0].ID != "id-4" {
		t.Fatalf("want newest first (id-4), got %s", snap[0].ID)
	}
}

func TestKillRunning(t *testing.T) {
	r := New()
	var o Options
	if isWindows() {
		o = Options{Name: "常驻", Cmd: "cmd", Args: []string{"/c", "ping -n 30 127.0.0.1 >nul"}}
	} else {
		o = Options{Name: "常驻", Cmd: "sleep", Args: []string{"30"}}
	}
	h, err := r.Run(o)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := r.Kill(h.ID()); err != nil {
		t.Fatalf("Kill error: %v", err)
	}
	// 等待进程结束并被移入历史
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if r.Running(h.ID()) == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("process not removed from running after kill")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

// TestSampleMem 验证采样函数对当前进程不 panic，返回 ≥0。
func TestSampleMem(t *testing.T) {
	mem := sampleMem(os.Getpid())
	if mem == ^uint64(0) { // 不会出现哨兵值
		t.Fatalf("sampleMem 返回异常值 %d", mem)
	}
	// Windows 下同用户进程通常能读到工作集；非 Windows 返回 0（显示「—」）。
	_ = mem
}

// TestSetMemAndSnapshot 验证 SetMem 更新内存并反映到快照。
func TestSetMemAndSnapshot(t *testing.T) {
	r := New()
	h := r.RegisterExisting("m:1", "p", "t", nil)
	defer r.Unregister("m:1", nil)
	r.SetMem(h.ID(), 12345)
	snap := r.Snapshot()
	if len(snap) != 1 || snap[0].MemBytes != 12345 {
		t.Fatalf("SetMem 未反映到快照: %+v", snap)
	}
}
