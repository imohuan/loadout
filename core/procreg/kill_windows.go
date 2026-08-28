//go:build windows

package procreg

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"time"

	"loadout/core/cmdutil"
)

// killProcessTree 终止指定 PID 的整棵进程树（Windows）。
// 用 Taskkill /T 连子进程一起杀，避免孤儿进程残留。
//
// 三重防护：
//  1. 无控制台不阻塞：桌面版以 windowsgui 模式运行，无控制台句柄；
//     taskkill 若向继承的 stdout/stderr 写输出可能挂起，故显式丢弃其输出。
//  2. 超时：taskkill 杀进程树（尤其 Chrome 这类多子进程）可能挂起，
//     导致退出流程阻塞、终端无法关闭。加 5 秒超时，超时即返回。
//  3. 进程不存在：目标 PID 已退出时 taskkill 返回退出码 128，
//     "无需再杀"正是期望状态，视为成功而非错误。
func killProcessTree(pid int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "taskkill", "/PID", strconv.Itoa(pid), "/T", "/F")
	cmdutil.HideWindow(cmd)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	err := cmd.Run()
	if err == nil {
		return nil
	}
	// taskkill 对不存在的 PID 返回 ERROR_PROC_NOT_FOUND(128)，
	// 说明进程早已退出，按"已清理"处理。
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 128 {
		return nil
	}
	return err
}
