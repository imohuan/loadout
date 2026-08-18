//go:build windows

// Package app Windows 任务栏/Alt+Tab 图标设置（WM_SETICON）。
// Wails v3 的 application.Options.Icon 在 Windows 上只影响 system tray，
// 需要额外通过 Win32 API 设置任务栏和 Alt+Tab 图标。
package app

import (
	"os"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32           = syscall.NewLazyDLL("user32.dll")
	procFindWindowW  = user32.NewProc("FindWindowW")
	procSendMessageW = user32.NewProc("SendMessageW")
	procLoadImageW   = user32.NewProc("LoadImageW")
)

const (
	WM_SETICON      = 0x0080 // 设置窗口图标
	ICON_SMALL      = 0      // 小图标（任务栏）
	ICON_BIG        = 1      // 大图标（Alt+Tab）
	IMAGE_ICON      = 1      // 加载图标类型
	LR_LOADFROMFILE = 0x00000010 // 从文件加载
)

// setWindowIconAfterCreate 通过 FindWindowW 定位窗口 HWND，然后用 WM_SETICON 设置图标。
// 异步执行，最多等待 5 秒窗口创建完成。
func setWindowIconAfterCreate(className, windowTitle string, icoBytes []byte) {
	go func() {
		// LoadImageW 需要文件路径，将图标字节写入临时文件
		tmpDir := os.TempDir()
		icoPath := filepath.Join(tmpDir, "app_icon.ico")
		if err := os.WriteFile(icoPath, icoBytes, 0644); err != nil {
			return
		}
		defer os.Remove(icoPath)

		// 通过窗口类名定位 HWND，最多重试 50 次（5 秒）
		cls, _ := syscall.UTF16PtrFromString(className)
		var hwnd uintptr
		for i := 0; i < 50; i++ {
			time.Sleep(100 * time.Millisecond)
			hwnd, _, _ = procFindWindowW.Call(uintptr(unsafe.Pointer(cls)), 0)
			if hwnd != 0 {
				break
			}
		}
		if hwnd == 0 {
			return
		}

		// LoadImageW: cx=cy=0 表示加载图标实际尺寸（不要传指针）
		path, _ := syscall.UTF16PtrFromString(icoPath)
		hIcon, _, _ := procLoadImageW.Call(0, uintptr(unsafe.Pointer(path)), IMAGE_ICON, 0, 0, LR_LOADFROMFILE)
		if hIcon != 0 {
			procSendMessageW.Call(hwnd, WM_SETICON, ICON_BIG, hIcon)   // Alt+Tab 大图标
			procSendMessageW.Call(hwnd, WM_SETICON, ICON_SMALL, hIcon) // 任务栏小图标
		}
	}()
}
