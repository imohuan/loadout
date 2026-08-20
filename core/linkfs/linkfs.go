// Package linkfs 提供跨平台链接能力：符号链接、Windows junction（目录联接）以及降级递归复制。
//
// 使用场景见 DESIGN.md 第 10 节：把技能仓库里的真实目录链接到目标目录，
// 在无法创建链接（例如 Windows 无管理员权限时 symlink 不可用）时降级为复制目录。
package linkfs

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Mode 表示链接/复制最终实际落盘的方式。
type Mode string

const (
	ModeSymlink  Mode = "symlink"  // 符号链接（Linux 首选 / Windows 有权限时）
	ModeJunction Mode = "junction" // Windows 目录联接（mklink /J，免管理员）
	ModeCopy     Mode = "copy"     // 降级：递归复制目录
)

// Link 在 dst 处创建指向 src 的链接。
//
// 策略按顺序尝试：os.Symlink → Windows junction（mklink /J）→ 递归复制目录。
// 返回实际采用的方式；src 不存在或不是目录时返回错误。
func Link(src, dst string) (Mode, error) {
	// 先校验 src：必须存在且为目录。
	fi, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("linkfs: src 不是目录: %s", src)
	}

	// 1. 优先尝试符号链接。
	if err := os.Symlink(src, dst); err == nil {
		return ModeSymlink, nil
	}

	// 2. Windows 下再尝试免管理员的目录联接（junction）。
	if runtime.GOOS == "windows" {
		if err := makeJunction(src, dst); err == nil {
			return ModeJunction, nil
		}
	}

	// 3. 两者都失败则降级为递归复制目录。
	if err := copyDir(src, dst); err != nil {
		return "", err
	}
	return ModeCopy, nil
}

// IsLink 判断路径是否为符号链接或 Windows junction（目录联接）。
//
// 符号链接通过 Lstat 的 ModeSymlink 位判定；Windows 上 mklink /J 创建的
// junction 是 reparse point，不设置 ModeSymlink，需额外用 isReparsePoint 识别
// （见 linkfs_windows.go / linkfs_other.go）。无法 Lstat（不存在等）时返回 false。
func IsLink(path string) bool {
	fi, err := os.Lstat(path)
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeSymlink != 0 || isReparsePoint(fi)
}

// EnsureDir 递归创建目录（含父目录），目录已存在时直接返回 nil。
func EnsureDir(path string) error {
	return os.MkdirAll(path, 0755)
}

// makeJunction 通过 cmd /c mklink /J 创建 Windows 目录联接。
// mklink /J 的参数顺序为「联接路径 目标路径」，即 dst 在前、src 在后。
func makeJunction(src, dst string) error {
	cmd := exec.Command("cmd", "/c", "mklink", "/J", dst, src)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("linkfs: mklink /J 失败: %v: %s", err, out)
	}
	return nil
}

// copyDir 递归复制目录内容：创建子目录、复制文件（含权限），跳过符号链接本身。
func copyDir(src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, srcInfo.Mode().Perm()); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		// 跳过链接（符号链接 / junction）本身，避免复制时循环或越界。
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(srcPath, dstPath); err != nil {
			return err
		}
	}
	return nil
}

// copyFile 复制单个文件，保留其权限位。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	// 显式回写权限，确保与源文件一致（Windows 上仅影响只读位）。
	return out.Chmod(info.Mode())
}
