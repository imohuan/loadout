//go:build !windows

package adminapi

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// 非 Windows：ocx 走 systemd/launchd，没有 UAC 概念。
// 这里提供同签名实现，让 ocxRunElevatedImpl 与测试能跨平台编译；
// 实际执行时 ocxElevatedCommand 会先判断 GOOS 并降级为普通权限。

func buildElevatedBatch(exe string, args []string, logPath string) string {
	var b strings.Builder
	b.WriteString("@echo off\r\n")
	b.WriteString("call " + cmdQuote(exe))
	for _, a := range args {
		b.WriteString(" " + cmdQuote(a))
	}
	b.WriteString(" >> " + cmdQuote(logPath) + " 2>&1\r\n")
	b.WriteString("echo %ERRORLEVEL% > " + cmdQuote(logPath+".exit") + "\r\n")
	b.WriteString("exit /b %ERRORLEVEL%\r\n")
	return b.String()
}

// cmdQuote 与 windows 版语义一致：含空格加双引号。
func cmdQuote(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t") {
		return s
	}
	return `"` + s + `"`
}

func startElevated(batchPath string) (uintptr, error) {
	return 0, fmt.Errorf("当前平台不支持 UAC 提权（batch=%s）", batchPath)
}

func waitElevated(h uintptr) int {
	_ = h
	return -1
}

// tailLog 跨平台实现：轮询文件增量，逐行回调，直到 stop 关闭或文件稳定且 finished 为真。
// 与 windows 版语义一致，供测试覆盖同一逻辑。
func tailLog(path string, offset int64, onLine func(string), stop <-chan struct{}, finished func() bool) {
	var buf []byte
	for {
		select {
		case <-stop:
			return
		default:
		}

		grew := false
		if data, err := os.ReadFile(path); err == nil && int64(len(data)) > offset {
			chunk := data[offset:]
			offset += int64(len(chunk))
			buf = append(buf, chunk...)
			grew = true
			for {
				i := strings.IndexByte(string(buf), '\n')
				if i < 0 {
					break
				}
				line := strings.TrimRight(string(buf[:i]), "\r")
				buf = buf[i+1:]
				if strings.TrimSpace(line) != "" {
					onLine(line)
				}
			}
		}

		if !grew && finished != nil && finished() {
			if s := strings.TrimSpace(string(buf)); s != "" {
				onLine(s)
			}
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}
