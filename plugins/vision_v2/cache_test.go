package visionv2

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"loadout/core/config"
)

// TestVisionCacheKey 验证缓存 key 的确定性：
// 同 id 同 prompt → 同 key；同 id 不同 prompt → 不同 key；不同 id 同 prompt → 不同 key；
// prompt 首尾空白被 trim（" 看颜色 " 与 "看颜色" 同 key）。
func TestVisionCacheKey(t *testing.T) {
	same := visionCacheKey("img1", "看颜色")
	if same != visionCacheKey("img1", "看颜色") {
		t.Fatal("同 id 同 prompt 应得到相同 key")
	}
	if same == visionCacheKey("img1", "看形状") {
		t.Fatal("同 id 不同 prompt 应得到不同 key")
	}
	if same == visionCacheKey("img2", "看颜色") {
		t.Fatal("不同 id 同 prompt 应得到不同 key")
	}
	if visionCacheKey("img1", " 看颜色 ") != same {
		t.Fatal("prompt 首尾空白应被 trim")
	}
}

// TestCacheReadWrite 验证写入后可读回，未写入的 key miss。
func TestCacheReadWrite(t *testing.T) {
	dir := t.TempDir()
	svc := &Service{cacheDir: dir}

	if err := svc.writeCache("k1", "描述"); err != nil {
		t.Fatalf("writeCache 失败: %v", err)
	}
	got, ok := svc.readCache("k1")
	if !ok || got != "描述" {
		t.Fatalf("readCache(k1) = (%q, %v), want (\"描述\", true)", got, ok)
	}
	if got, ok := svc.readCache("missing"); ok || got != "" {
		t.Fatalf("readCache(missing) = (%q, %v), want (\"\", false)", got, ok)
	}
}

// TestCacheTTLExpiry 验证过期文件 miss：TTL 临时改为 1 小时，再把文件 mtime 改成 2 小时前。
func TestCacheTTLExpiry(t *testing.T) {
	dir := t.TempDir()
	svc := &Service{cacheDir: dir}

	old := config.VisionCacheTTLHours
	config.VisionCacheTTLHours = 1
	defer func() { config.VisionCacheTTLHours = old }()

	if err := svc.writeCache("k1", "描述"); err != nil {
		t.Fatalf("writeCache 失败: %v", err)
	}
	path := filepath.Join(dir, "k1.txt")
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatalf("Chtimes 失败: %v", err)
	}

	if got, ok := svc.readCache("k1"); ok || got != "" {
		t.Fatalf("过期缓存 readCache(k1) = (%q, %v), want (\"\", false)", got, ok)
	}
}
