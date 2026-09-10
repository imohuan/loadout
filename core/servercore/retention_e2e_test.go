package servercore

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loadout/core/plugin"
	"loadout/core/store"
)

// TestE2ERetentionEndpoints 端到端：真实装配 → 建真库 → 打真接口。
// 验证 request-log 的三个新接口在完整装配链路下可用，且装配顺序约束成立。
func TestE2ERetentionEndpoints(t *testing.T) {
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

	order := plugin.OrderOf(asm)
	t.Logf("assembly order: %s", strings.Join(order, " "))
	routeIdx, reqIdx := -1, -1
	for i, name := range order {
		switch name {
		case "route-log":
			routeIdx = i
		case "request-log":
			reqIdx = i
		}
	}
	if routeIdx < 0 || reqIdx < 0 {
		t.Fatalf("missing plugins in order: %v", order)
	}
	if routeIdx > reqIdx {
		t.Fatalf("route-log must assemble before request-log, got %v", order)
	}

	srv := httptest.NewServer(handler)
	defer srv.Close()

	do := func(method, path string) (int, string) {
		req, err := http.NewRequest(method, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	// 这些接口都要求会话认证：未登录时正确的响应是 401（路由已注册），
	// 若返回 404 则说明路由没挂上。
	for _, tc := range []struct{ m, p string }{
		{http.MethodGet, "/api/request-logs/stats"},
		{http.MethodPost, "/api/request-logs/retention/apply"},
		{http.MethodDelete, "/api/request-logs"},
		{http.MethodGet, "/api/route-logs"},
	} {
		code, body := do(tc.m, tc.p)
		if len(body) > 300 {
			body = body[:300] + "..."
		}
		fmt.Printf("%s %s -> %d %s\n", tc.m, tc.p, code, body)
		if code == http.StatusNotFound {
			t.Fatalf("%s %s -> 404: route not registered", tc.m, tc.p)
		}
		if code != http.StatusUnauthorized {
			t.Fatalf("%s %s -> %d, want 401 (auth required): %s", tc.m, tc.p, code, body)
		}
	}

	// request-log.db 在插件装配时就该建好，位置与 loadout.db 同级（store 目录的父级）。
	parent := filepath.Dir(st.Dir())
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "request-log.db") {
			info, _ := e.Info()
			t.Logf("file: %s %d bytes (in %s)", e.Name(), info.Size(), parent)
			found = true
		}
	}
	if !found {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("request-log.db not created in %s: %v", parent, names)
	}
}
