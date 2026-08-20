package adminauth

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"loadout/core/auth"
	"loadout/core/config"
	"loadout/core/store"
)

// newTestService 在临时目录建 Store，并把 config.AdminPasswordFile 重定向到该目录，
// 返回认证服务与密码文件路径（密码文件由 EnsureFirstRun 生成）。
func newTestService(t *testing.T) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	old := config.AdminPasswordFile
	config.AdminPasswordFile = filepath.Join(dir, "admin-password")
	t.Cleanup(func() { config.AdminPasswordFile = old })
	return NewService(st, slog.Default()), config.AdminPasswordFile
}

// readPasswordFile 读取明文密码文件内容。
func readPasswordFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取密码文件失败: %v", err)
	}
	return string(data)
}

// TestEnsureFirstRun 验证首启流程：生成账号、写出明文密码、幂等。
func TestEnsureFirstRun(t *testing.T) {
	svc, pwFile := newTestService(t)

	ok, err := svc.EnsureFirstRun()
	if err != nil {
		t.Fatalf("EnsureFirstRun: %v", err)
	}
	if !ok {
		t.Fatal("首次调用 EnsureFirstRun 应返回 true")
	}
	if !svc.HasUsers() {
		t.Fatal("首启后 HasUsers 应返回 true")
	}

	pw := readPasswordFile(t, pwFile)
	if pw == "" {
		t.Fatal("admin-password 文件应包含明文密码")
	}

	// 第二次调用应幂等，不重复执行。
	ok2, err := svc.EnsureFirstRun()
	if err != nil {
		t.Fatalf("第二次 EnsureFirstRun: %v", err)
	}
	if ok2 {
		t.Fatal("users.json 已存在时 EnsureFirstRun 应返回 false")
	}
}

// TestLogin 验证首启密码可登录，错误密码与未知用户失败。
func TestLogin(t *testing.T) {
	svc, pwFile := newTestService(t)
	if _, err := svc.EnsureFirstRun(); err != nil {
		t.Fatalf("EnsureFirstRun: %v", err)
	}
	pw := readPasswordFile(t, pwFile)

	token, err := svc.Login("admin", pw)
	if err != nil {
		t.Fatalf("正确密码登录失败: %v", err)
	}
	if token == "" {
		t.Fatal("登录成功应返回非空 token")
	}

	if _, err := svc.Login("admin", "wrong-password"); err == nil {
		t.Fatal("错误密码登录应返回错误")
	}
	if _, err := svc.Login("nobody", pw); err == nil {
		t.Fatal("未知用户登录应返回错误")
	}
}

// TestSessionMiddleware 验证带正确 token 放行且注入 claims，无/错 token 返回 401。
func TestSessionMiddleware(t *testing.T) {
	svc, pwFile := newTestService(t)
	if _, err := svc.EnsureFirstRun(); err != nil {
		t.Fatalf("EnsureFirstRun: %v", err)
	}
	pw := readPasswordFile(t, pwFile)
	token, err := svc.Login("admin", pw)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	handler := svc.SessionMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := r.Context().Value(claimsContextKey).(*auth.Claims)
		if !ok || claims == nil {
			t.Error("context 中缺少 claims")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if claims.Username != "admin" {
			t.Errorf("claims.Username = %q，期望 admin", claims.Username)
		}
		w.WriteHeader(http.StatusOK)
	}))

	// 正确 token → 200。
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("正确 token 期望 200，实际 %d", rr.Code)
	}

	// 无 token → 401。
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr2.Code != http.StatusUnauthorized {
		t.Fatalf("无 token 期望 401，实际 %d", rr2.Code)
	}

	// 错误 token → 401。
	req3 := httptest.NewRequest(http.MethodGet, "/", nil)
	req3.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "bad.token.value"})
	rr3 := httptest.NewRecorder()
	handler.ServeHTTP(rr3, req3)
	if rr3.Code != http.StatusUnauthorized {
		t.Fatalf("错误 token 期望 401，实际 %d", rr3.Code)
	}
}

// TestChangePassword 验证旧密码校验、改密成功、删除密码文件、新旧密码登录行为。
func TestChangePassword(t *testing.T) {
	svc, pwFile := newTestService(t)
	if _, err := svc.EnsureFirstRun(); err != nil {
		t.Fatalf("EnsureFirstRun: %v", err)
	}
	initialPw := readPasswordFile(t, pwFile)

	// 旧密码错误应失败。
	if err := svc.ChangePassword("admin", "wrong-old", "newpass123!"); err == nil {
		t.Fatal("旧密码错误时 ChangePassword 应返回错误")
	}

	// 正确改密。
	if err := svc.ChangePassword("admin", initialPw, "newpass123!"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}

	// 改密成功后 admin-password 文件应被删除。
	if _, err := os.Stat(config.AdminPasswordFile); !os.IsNotExist(err) {
		t.Fatal("改密成功后 admin-password 文件应被删除")
	}

	// 新密码登录成功，旧密码失败。
	if _, err := svc.Login("admin", "newpass123!"); err != nil {
		t.Fatalf("新密码登录失败: %v", err)
	}
	if _, err := svc.Login("admin", initialPw); err == nil {
		t.Fatal("旧密码登录应失败")
	}
}
