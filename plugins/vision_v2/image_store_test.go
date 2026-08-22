package visionv2

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loadout/core/config"
)

// 1x1 红色 PNG 的 base64（mime image/png）。
const tinyPNGDataURI = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

// newTestService 构造 cacheDir 指向 t.TempDir() 的 Service。
func newTestService(t *testing.T) *Service {
	t.Helper()
	svc := NewService(nil, nil, slog.Default())
	svc.cacheDir = t.TempDir()
	return svc
}

func TestSaveImageDataURI(t *testing.T) {
	svc := newTestService(t)

	id, err := svc.SaveImageDataURI(tinyPNGDataURI)
	if err != nil {
		t.Fatalf("SaveImageDataURI 报错: %v", err)
	}
	if len(id) != idLen {
		t.Fatalf("id 长度 = %d, want %d", len(id), idLen)
	}
	for _, c := range id {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Fatalf("id 含非十六进制字符: %q", id)
		}
	}

	// files/ 下存在 {id}.bin。
	dst := filepath.Join(svc.imageFilesDir(), id+".bin")
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("文件 %s 不存在: %v", dst, err)
	}

	// 幂等：再次保存同图返回同 id。
	again, err := svc.SaveImageDataURI(tinyPNGDataURI)
	if err != nil {
		t.Fatalf("再次 SaveImageDataURI 报错: %v", err)
	}
	if again != id {
		t.Fatalf("幂等失败: 第二次 id = %q, want %q", again, id)
	}

	// 非法输入报错。
	if _, err := svc.SaveImageDataURI("junk"); err == nil {
		t.Fatal("非法 data URI 未报错")
	}
}

func TestLoadImageBytesDetectMime(t *testing.T) {
	svc := newTestService(t)

	id, err := svc.SaveImageDataURI(tinyPNGDataURI)
	if err != nil {
		t.Fatalf("SaveImageDataURI 报错: %v", err)
	}

	raw, mime, err := svc.loadImageBytes(id)
	if err != nil {
		t.Fatalf("loadImageBytes 报错: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("raw 为空")
	}
	if !strings.Contains(mime, "image/png") {
		t.Fatalf("mime = %q, want 含 image/png", mime)
	}

	// 不存在的 id 应报错。
	if _, _, err := svc.loadImageBytes(strings.Repeat("0", idLen)); err == nil {
		t.Fatal("loadImageBytes 不存在的 id 未报错")
	}
}

func TestSaveImageURL(t *testing.T) {
	img := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00}

	// 正常：返回固定字节。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(img)
	}))
	defer srv.Close()

	svc := newTestService(t)
	id, err := svc.SaveImageURL(t.Context(), srv.URL)
	if err != nil {
		t.Fatalf("SaveImageURL 报错: %v", err)
	}
	if want := imageID(img); id != want {
		t.Fatalf("id = %q, want %q", id, want)
	}
	got, err := os.ReadFile(filepath.Join(svc.imageFilesDir(), id+".bin"))
	if err != nil {
		t.Fatalf("读取落盘文件报错: %v", err)
	}
	if string(got) != string(img) {
		t.Fatalf("落盘内容 = %v, want %v", got, img)
	}

	// 500：报错。
	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer errSrv.Close()

	if _, err := svc.SaveImageURL(t.Context(), errSrv.URL); err == nil {
		t.Fatal("server 返回 500 未报错")
	}
}

func TestCleanupStaleFiles(t *testing.T) {
	// TTL 临时改为 1 小时，测试结束恢复。
	origTTL := config.VisionCacheTTLHours
	config.VisionCacheTTLHours = 1
	t.Cleanup(func() { config.VisionCacheTTLHours = origTTL })

	svc := newTestService(t)
	dir := svc.imageFilesDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll 报错: %v", err)
	}

	// 新文件：mtime 为现在，TTL 内，应保留。
	fresh := filepath.Join(dir, "fresh.bin")
	if err := os.WriteFile(fresh, []byte("new"), 0o600); err != nil {
		t.Fatalf("写 fresh 文件报错: %v", err)
	}

	// 旧文件：mtime 改到 10 小时前，超 TTL，应删除。
	stale := filepath.Join(dir, "stale.bin")
	if err := os.WriteFile(stale, []byte("old"), 0o600); err != nil {
		t.Fatalf("写 stale 文件报错: %v", err)
	}
	old := time.Now().Add(-10 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatalf("Chtimes 报错: %v", err)
	}

	svc.cleanupStaleFiles()

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("超 TTL 文件未被删除, stat err = %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("TTL 内文件被误删: %v", err)
	}
}
