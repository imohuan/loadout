//go:build !windows

package procreg

import (
	"os"
	"syscall"
)

// killProcessTree 终止指定 PID 的进程（非 Windows 降级为直接杀进程）。
func killProcessTree(pid int) error {
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Signal(syscall.SIGTERM)
		_ = p.Signal(syscall.SIGKILL)
	}
	return nil
}
