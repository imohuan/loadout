//go:build !windows

package procreg

// sampleMem 采样进程内存（非 Windows 平台暂不支持，返回 0，前端显示「—」）。
func sampleMem(pid int) uint64 {
	return 0
}
