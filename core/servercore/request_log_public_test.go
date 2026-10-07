package servercore

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"loadout/core/store"
)

// TestRequestLogDetailPublicWithoutAuth 验证完整请求日志详情接口无需会话认证。
//
// 该接口按 UUID 直取一条日志，前端「复制日志链接」把地址贴到外部浏览器打开，
// 因此必须公开：未登录请求一个不存在的 id 应得到 404（路由已注册且未挂鉴权、
// 未命中记录），而不是 401。若返回 401 说明 session 中间件仍挂在该路由上。
func TestRequestLogDetailPublicWithoutAuth(t *testing.T) {
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

	resp, err := http.Get(srv.URL + "/api/request-logs/0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatalf("GET /api/request-logs/{id} -> 401：接口仍要求会话认证: %s", body)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /api/request-logs/{id} -> %d, want 404（未登录、未命中记录）: %s", resp.StatusCode, body)
	}
}
