//go:build !windows

// Package cmdutil 提供跨平台命令执行辅助函数。
//
// 桌面版（Wails exe）以 windowsgui 模式运行，本身无控制台；
// 但子进程（npx/cmd 等控制台程序）默认会新开一个黑色终端窗口。
// HideWindow 在 Windows 上为子进程设置 CREATE_NO_WINDOW 标志实现静默执行，
// 其他平台为空操作，保证 plugins（Linux server 也编译）可跨平台编译。
package cmdutil

import "os/exec"

// HideWindow 在 Windows 上隐藏子进程控制台窗口；其他平台为空操作。
func HideWindow(*exec.Cmd) {}
