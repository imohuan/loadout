//go:build windows

package procreg

import (
	"testing"
	"time"
)

// TestKillProcessTreeNonexistent 验证对不存在的 PID 调用 killProcessTree：
//   - 不应报错（taskkill 对不存在的进程返回 128，应视为"已退出"）
//   - 不应阻塞（超时保护生效）
func TestKillProcessTreeNonexistent(t *testing.T) {
	// 使用一个几乎不可能存在的超大 PID，保证进程不存在。
	const bigPID = 999999999
	start := time.Now()
	if err := killProcessTree(bigPID); err != nil {
		t.Fatalf("killProcessTree(nonexistent) error = %v, want nil", err)
	}
	if elapsed := time.Since(start); elapsed > 6*time.Second {
		t.Fatalf("killProcessTree(nonexistent) took %v, exceeded 5s timeout guard", elapsed)
	}
}
