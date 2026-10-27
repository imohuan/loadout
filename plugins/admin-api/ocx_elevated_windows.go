//go:build windows

package adminapi

import (
	"fmt"
	"os"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ============================================================================
// 用 Go 直接触发 UAC 提权（不走 PowerShell）。
//
// 之前的实现把命令拼进 PowerShell 的 -Command 字符串，引号、制表符、
// 中文注释任何一处出错都会报 "Unexpected token '}'"，窗口一闪就退，
// 用户完全看不到失败原因。这里改用系统 API：
//   ShellExecuteExW(verb="runas", file=<临时 .cmd>)
// 提权交给 Windows，命令由 CMD 执行，输出重定向到日志文件；
// Go 侧只负责跟随这个文件并把内容推给前端。
// ============================================================================

// shellExecuteInfo 对应 SHELLEXECUTEINFOW。
type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         windows.Handle
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     windows.Handle
	lpIDList     unsafe.Pointer
	lpClass      *uint16
	hkeyClass    windows.Handle
	dwHotKey     uint32
	hIcon        windows.Handle
	hProcess     windows.Handle
}

const (
	// SEE_MASK_NOCLOSEPROCESS 让 ShellExecuteEx 把进程句柄写回 hProcess，
	// 我们据此等待结束并拿退出码。
	seeMaskNoCloseProcess = 0x00000040
	// SW_HIDE：不弹控制台黑框，所有结果都进日志。
	swHide = 0
)

// shellExecuteEx 调用 shell32!ShellExecuteExW。
func shellExecuteEx(info *shellExecuteInfo) error {
	proc := windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")
	if err := proc.Find(); err != nil {
		return fmt.Errorf("找不到 ShellExecuteExW: %w", err)
	}
	r, _, callErr := proc.Call(uintptr(unsafe.Pointer(info)))
	if r != 0 {
		return nil
	}
	if callErr != nil && callErr.Error() != "The operation completed successfully." {
		return callErr
	}
	return fmt.Errorf("ShellExecuteExW 失败（通常是用户取消了 UAC 授权）")
}

// startElevated 以管理员身份运行一个临时 .cmd，返回可等待的进程句柄。
//
// 参数：batchPath 是已经写好的 .cmd 路径。
// 返回：进程句柄（用于 Wait），错误。
func startElevated(batchPath string) (uintptr, error) {
	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return 0, err
	}
	file, err := windows.UTF16PtrFromString(batchPath)
	if err != nil {
		return 0, err
	}
	info := shellExecuteInfo{
		fMask:  seeMaskNoCloseProcess,
		lpVerb: verb,
		lpFile: file,
		nShow:  swHide,
	}
	info.cbSize = uint32(unsafe.Sizeof(info))
	if err := shellExecuteEx(&info); err != nil {
		return 0, err
	}
	if info.hProcess == 0 {
		return 0, fmt.Errorf("提权进程未返回句柄")
	}
	return uintptr(info.hProcess), nil
}

// waitElevated 等待提权进程结束，返回退出码（-1 表示取不到）。
func waitElevated(h uintptr) int {
	handle := windows.Handle(h)
	defer windows.CloseHandle(handle)
	_, _ = windows.WaitForSingleObject(handle, windows.INFINITE)
	var code uint32
	if err := windows.GetExitCodeProcess(handle, &code); err != nil {
		return -1
	}
	return int(code)
}

// buildElevatedBatch 生成提权用的 .cmd 内容。
//
// CMD 的语法只有三件事：逐个执行、> 重定向、%ERRORLEVEL%。
// 没有花括号、没有子表达式，因此不会出现 PowerShell 那类解析陷阱。
func buildElevatedBatch(exe string, args []string, logPath string) string {
	var b strings.Builder
	b.WriteString("@echo off\r\n")
	// 先写一行抬头，失败时也能看到到底执行了什么。
	b.WriteString("echo [] ")
	b.WriteString(cmdQuote(exe))
	for _, a := range args {
		b.WriteString(" ")
		b.WriteString(cmdQuote(a))
	}
	b.WriteString("\r\n")
	b.WriteString("call ")
	b.WriteString(cmdQuote(exe))
	for _, a := range args {
		b.WriteString(" ")
		b.WriteString(cmdQuote(a))
	}
	b.WriteString(" >> ")
	b.WriteString(cmdQuote(logPath))
	b.WriteString(" 2>&1\r\n")
	b.WriteString("set CODE=%ERRORLEVEL%\r\n")
	b.WriteString("echo [] exit code: %CODE% >> ")
	b.WriteString(cmdQuote(logPath))
	b.WriteString("\r\n")
	b.WriteString("exit /b %CODE%\r\n")
	return b.String()
}

// cmdQuote 按 CMD 规则给参数加双引号。
// CMD 只认双引号；参数内的双引号对 .cmd 场景基本不会出现（路径/子命令）。
func cmdQuote(s string) string {
	if s == "" {
		return "\"\""
	}
	if !strings.ContainsAny(s, " \t") {
		return s
	}
	return "\"" + s + "\""
}

// tailLog 跟随日志文件，逐行回调，直到 stop 关闭或文件稳定且 done 为真。
// offset 从 0 开始；返回时确保把最后一行也推出去。
func tailLog(path string, offset int64, onLine func(string), stop <-chan struct{}, finished func() bool) {
	var buf []byte
	idleTicks := 0
	for {
		select {
		case <-stop:
			return
		default:
		}

		grew := false
		if f, err := os.Open(path); err == nil {
			if st, statErr := f.Stat(); statErr == nil && st.Size() > offset {
				if _, seekErr := f.Seek(offset, 0); seekErr == nil {
					chunk := make([]byte, st.Size()-offset)
					n, _ := f.Read(chunk)
					offset += int64(n)
					buf = append(buf, chunk[:n]...)
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
			}
			f.Close()
		}

		if !grew && finished != nil && finished() {
			// 进程已退出且文件不再增长：把残留的最后一行推出去即可结束。
			if s := strings.TrimSpace(string(buf)); s != "" {
				onLine(s)
			}
			return
		}
		if grew {
			idleTicks = 0
		} else {
			idleTicks++
		}
		time.Sleep(200 * time.Millisecond)
	}
}
