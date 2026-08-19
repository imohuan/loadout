package linkfs

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLinkAndIsLink 验证 Link 的三种可能落盘方式及 IsLink 的判定。
// Windows 上 symlink 可能因权限不足自动落到 junction 或 copy，
// 因此这里不假设固定 mode，只校验行为正确。
func TestLinkAndIsLink(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0755); err != nil {
		t.Fatalf("创建 src 子目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "file.txt"), []byte("hello linkfs"), 0644); err != nil {
		t.Fatalf("创建 src 文件失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "nested.txt"), []byte("nested content"), 0644); err != nil {
		t.Fatalf("创建 src 嵌套文件失败: %v", err)
	}

	dst := filepath.Join(root, "dst")
	mode, err := Link(src, dst)
	if err != nil {
		t.Fatalf("Link(%q, %q) 失败: %v", src, dst, err)
	}

	switch mode {
	case ModeSymlink, ModeJunction, ModeCopy:
		// 合法 mode。
	default:
		t.Fatalf("非预期的 mode: %q", mode)
	}

	// dst 下文件内容必须与 src 一致（链接时读取会跟随到 src）。
	got, err := os.ReadFile(filepath.Join(dst, "file.txt"))
	if err != nil {
		t.Fatalf("读取 dst/file.txt 失败（mode=%s）: %v", mode, err)
	}
	if string(got) != "hello linkfs" {
		t.Fatalf("dst/file.txt 内容不一致: 期望 %q，实际 %q", "hello linkfs", got)
	}

	gotNested, err := os.ReadFile(filepath.Join(dst, "sub", "nested.txt"))
	if err != nil {
		t.Fatalf("读取 dst/sub/nested.txt 失败（mode=%s）: %v", mode, err)
	}
	if string(gotNested) != "nested content" {
		t.Fatalf("dst/sub/nested.txt 内容不一致: 期望 %q，实际 %q", "nested content", gotNested)
	}

	// symlink / junction 时 IsLink 应为 true；copy 时为 false。
	wantLink := mode != ModeCopy
	if gotLink := IsLink(dst); gotLink != wantLink {
		t.Fatalf("IsLink(%q) = %v，期望 %v（mode=%s）", dst, gotLink, wantLink, mode)
	}
}

// TestEnsureDir 验证递归创建目录及重复调用幂等。
func TestEnsureDir(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b", "c")

	if err := EnsureDir(nested); err != nil {
		t.Fatalf("EnsureDir 新建嵌套目录失败: %v", err)
	}
	info, err := os.Stat(nested)
	if err != nil {
		t.Fatalf("Stat 新建目录失败: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("%q 不是目录", nested)
	}

	// 重复调用应当成功（幂等）。
	if err := EnsureDir(nested); err != nil {
		t.Fatalf("EnsureDir 重复调用失败: %v", err)
	}
}

// TestLinkMissingSrc 验证 src 不存在时 Link 返回错误。
func TestLinkMissingSrc(t *testing.T) {
	root := t.TempDir()
	if _, err := Link(filepath.Join(root, "no-such-src"), filepath.Join(root, "dst")); err == nil {
		t.Fatal("src 不存在时 Link 应返回错误，实际成功")
	}
}

// TestLinkNonDirSrc 验证 src 是文件（非目录）时 Link 返回错误。
func TestLinkNonDirSrc(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "file.txt")
	if err := os.WriteFile(src, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Link(src, filepath.Join(root, "dst")); err == nil {
		t.Fatal("src 不是目录时 Link 应返回错误，实际成功")
	}
}
