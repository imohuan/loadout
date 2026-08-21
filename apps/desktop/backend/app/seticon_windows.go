//go:build windows

// Package app Windows 任务栏/Alt+Tab/标题栏图标设置（WM_SETICON）。
//
// Wails v3 alpha 的 webview_window_windows.setIcon 只设 ICON_BIG（Alt+Tab 大图标），
// 而 application.init() 注册窗口类时 IconSm = IDI_APPLICATION（系统默认占位），
// 导致窗口标题栏左侧小图标 + 任务栏小图标一直是浅色方块。
// 本文件补 ICON_SMALL（+ 重复 ICON_BIG 兜底），用 Wails v3 默认窗口类名
// "WailsWebviewWindow" 找 HWND。
// application.Options.Icon 在 Wails v3 上不直接驱动 webview2 窗口图标（issue #3487），
// 所以传 embed 的 appicon.ico bytes（icons.AppIcon）作为图标源。
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
