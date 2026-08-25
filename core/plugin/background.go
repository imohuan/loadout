package plugin

import (
	"fmt"
	"log/slog"
)

// RunBackground 在后台 goroutine 执行 fn，主流程立即返回、不被阻塞。
//
// 适用场景：启动装配路径上那些「重要但不该拖慢服务上线」的工作
//（如 fsnotify 文件监听初始化、MCP 进程拉起）。调用方可任选：
//   - 完全不等：_ = RunBackground(name, fn)
//   - 带超时等：select { case err := <-ch: ... case <-time.After(d): 降级 }
//
// 行为约定：
//   - goroutine 内任何 panic 都会被 recover 并记录为 slog.Error，绝不向上崩溃进程；
//   - fn 返回的 error 通过有缓冲(1)的 channel 回传，写入永不阻塞；
//   - name 仅用于日志标识，无唯一性要求。
func RunBackground(name string, fn func() error) <-chan error {
	ch := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- fmt.Errorf("background %q panic: %v", name, r)
				slog.Error("后台任务 panic，已捕获", "task", name, "panic", r)
			}
		}()
		err := fn()
		if err != nil {
			slog.Warn("后台任务返回错误", "task", name, "err", err)
		}
		ch <- err
	}()
	return ch
}
