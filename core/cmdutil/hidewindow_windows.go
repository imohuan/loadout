package cmdutil

import (
	"os/exec"
	"syscall"
)

// HideWindow 设置子进程不创建控制台窗口（桌面 APP 静默执行，不弹黑色终端框）。
func HideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
