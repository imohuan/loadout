package servercore

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"loadout/core/store"
)

// TestHealthEndpointReadyWithoutAuth 验证 /api/health 已挂载、无需认证即可访问。
// 桌面壳前端靠它判断「后端是否已就绪」，因此必须公开且稳定返回 200。
func TestHealthEndpointReadyWithoutAuth(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	lg := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn}))
	asm, handler, err := Assemble(lg, st)
	if err != nil {
		t.Fatal(err)
	}
	defer asm.Unload()

	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/health -> %d, want 200: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "ok") {
		t.Fatalf("GET /api/health body = %q, want it to contain status ok", body)
	}
}
