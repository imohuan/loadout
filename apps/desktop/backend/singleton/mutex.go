// Package singleton 提供单实例控制，启动时自动杀掉已运行的旧进程。
package singleton

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// KillExistingInstance 通过 tasklist 查找同名进程并终止（跳过自身）。
// 仅在 Windows 下有效。
func KillExistingInstance() {
	currentExe, _ := os.Executable()
	currentName := strings.ToLower(fileName(currentExe))

	cmd := exec.Command("tasklist", "/FO", "CSV", "/NH")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return
	}

	pid := os.Getpid()
	for _, line := range strings.Split(string(out), "\n") {
		// CSV 格式: "name.exe","pid",...
		parts := strings.SplitN(line, ",", 3)
		if len(parts) < 2 {
			continue
		}
		name := strings.Trim(parts[0], `"`)
		if !strings.EqualFold(name, currentName) {
			continue
		}
		pidStr := strings.Trim(parts[1], `"`)
		var otherPid int
		if _, err := fmt.Sscanf(pidStr, "%d", &otherPid); err != nil {
			continue
		}
		if otherPid == pid {
			continue // 跳过自身
		}
		proc, _ := os.FindProcess(otherPid)
		if proc != nil {
			proc.Kill()
			proc.Wait()
		}
	}
}

// fileName 从完整路径中提取文件名。
func fileName(path string) string {
	path = strings.ReplaceAll(path, "\\", "/")
	i := strings.LastIndex(path, "/")
	if i >= 0 {
		return path[i+1:]
	}
	return path
}
