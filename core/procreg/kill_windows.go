//go:build windows

package procreg

import (
	"os/exec"
	"strconv"

	"loadout/core/cmdutil"
)

// killProcessTree 终止指定 PID 的整棵进程树（Windows）。
// 用 Taskkill /T 连子进程一起杀，避免孤儿进程残留。
func killProcessTree(pid int) error {
	cmd := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F")
	cmdutil.HideWindow(cmd)
	return cmd.Run()
}
