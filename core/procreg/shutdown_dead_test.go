//go:build windows

package procreg

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestShutdownWithDeadRegisteredProcess 复现用户场景：
// MCP 子进程已退出但仍登记在 running 表（如 RegisterExisting 登记后进程悄然退出），
// Shutdown 时 kill 会命中不存在的 PID。
// 验证修复后：不阻塞（超时兜底）且不把"进程不存在"当错误返回。
func TestShutdownWithDeadRegisteredProcess(t *testing.T) {
	r := New()
	// 启动一个瞬进程并等它自己退出，拿到一个"已退出"的 PID。
	cmd := exec.Command("cmd", "/c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatalf("setup: %v", err)
	}
	deadPID := cmd.ProcessState.Pid()

	// 登记它，模拟 MCP 已死但仍占用 running 表。
	r.RegisterExisting("dead-mcp", "MCP", "mcp", &exec.Cmd{Path: "cmd", Args: []string{"cmd"}, Process: &os.Process{Pid: deadPID}})

	// Shutdown 必须在超时内返回，且不因"进程不存在"报错。
	start := time.Now()
	err := r.Shutdown()
	elapsed := time.Since(start)
	if elapsed > 6*time.Second {
		t.Fatalf("Shutdown blocked: took %v, exceeds 5s timeout guard", elapsed)
	}
	if err != nil {
		t.Fatalf("Shutdown error = %v, want nil (dead process should be treated as cleaned)", err)
	}
}
