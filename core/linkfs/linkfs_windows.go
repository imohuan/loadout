//go:build windows

package linkfs

import (
	"os"
	"syscall"
)

// isReparsePoint 判断 FileInfo 是否为 Windows reparse point。
// mklink /J 创建的 junction 是 mount point 类型的 reparse point，
// 其文件属性含 FILE_ATTRIBUTE_REPARSE_POINT，但 Lstat 不设置 ModeSymlink，
// 因此需要在此识别。
func isReparsePoint(fi os.FileInfo) bool {
	attr, ok := fi.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return false
	}
	return attr.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
