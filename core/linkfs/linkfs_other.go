//go:build !windows

package linkfs

import "os"

// isReparsePoint 非 Windows 平台不存在 reparse point 概念，恒返回 false。
func isReparsePoint(fi os.FileInfo) bool {
	return false
}
