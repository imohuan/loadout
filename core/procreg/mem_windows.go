//go:build windows

package procreg

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// processMemoryCounters 对应 PROCESS_MEMORY_COUNTERS_EX（部分字段）。
type processMemoryCounters struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
	PrivateUsage               uintptr
}

var (
	kernel32        = syscall.NewLazyDLL("kernel32.dll")
	psapi           = syscall.NewLazyDLL("psapi.dll")
	procK32GetProcM = psapi.NewProc("K32GetProcessMemoryInfo")
)

const processQueryLimitedInformation = 0x1000

// sampleMem 采样进程内存（Windows，取工作集 WorkingSetSize）。
// 返回 0 表示无法读取（权限不足/进程已退出），前端显示「—」。
func sampleMem(pid int) uint64 {
	handle, err := windows.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(handle)

	var m processMemoryCounters
	m.CB = uint32(unsafe.Sizeof(m))
	r1, _, err := procK32GetProcM.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(&m)),
		uintptr(m.CB),
	)
	if r1 == 0 {
		_ = err
		return 0
	}
	return uint64(m.WorkingSetSize)
}
