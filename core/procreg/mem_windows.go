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

const processQueryLimitedInformation = 0x1000

// getMemProc 指向可用的 GetProcessMemoryInfo 过程（K32GetProcessMemoryInfo 或
// GetProcessMemoryInfo，视系统而定）；加载失败时为零值，sampleMem 直接返回 0。
var getMemProc = func() *syscall.LazyProc {
	psapi := syscall.NewLazyDLL("psapi.dll")
	for _, name := range []string{"K32GetProcessMemoryInfo", "GetProcessMemoryInfo"} {
		p := psapi.NewProc(name)
		if err := p.Find(); err == nil {
			return p
		}
	}
	return nil
}()

// sampleMem 采样进程内存（Windows，取工作集 WorkingSetSize）。
// 无法读取（DLL 缺失/权限不足/进程已退出）返回 0，前端显示「—」，不 panic。
func sampleMem(pid int) uint64 {
	if getMemProc == nil {
		return 0
	}
	handle, err := windows.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(handle)

	var m processMemoryCounters
	m.CB = uint32(unsafe.Sizeof(m))
	r1, _, _ := getMemProc.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(&m)),
		uintptr(m.CB),
	)
	if r1 == 0 {
		return 0
	}
	return uint64(m.WorkingSetSize)
}
