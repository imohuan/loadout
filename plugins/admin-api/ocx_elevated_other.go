//go:build !windows

package adminapi

import (
	"fmt"
	"strings"
)

// 非 Windows：ocx 走 systemd/launchd，没有 UAC 概念。
// 这里只提供同签名占位，让 ocxRunElevatedImpl 能跨平台编译；
// 实际执行时 ocxElevatedCommand 会先判断 GOOS 并降级为普通权限。

func buildElevatedBatch(exe string, args []string, logPath string) string {
	_ = logPath
	return strings.Join(append([]string{exe}, args...), " ")
}

func startElevated(batchPath string) (uintptr, error) {
	return 0, fmt.Errorf("当前平台不支持 UAC 提权（batch=%s）", batchPath)
}

func waitElevated(h uintptr) int {
	_ = h
	return -1
}

func tailLog(path string, offset int64, onLine func(string), stop <-chan struct{}, finished func() bool) {
	_, _, _, _ = path, offset, onLine, stop
	_ = finished
}
