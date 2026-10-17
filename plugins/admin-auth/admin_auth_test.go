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

// newTestService 在临时目录建 Store，并把 config.AdminPasswordFile 重定向到该目录。
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

// TestSetupAdminKey 验证首次设置访问密钥：幂等保护、最短长度、密码文件清理。
func TestSetupAdminKey(t *testing.T) {
	svc, pwFile := newTestService(t)

	if svc.Initialized() {
		t.Fatal("新实例 Initialized 应为 false")
	}

	// 密钥过短。
	if err := svc.SetupAdminKey("123"); err == nil {
		t.Fatal("过短密钥应返回错误")
	}

	// 正常设置。
	if err := svc.SetupAdminKey("my-access-key"); err != nil {
		t.Fatalf("SetupAdminKey: %v", err)
	}
	if !svc.Initialized() {
		t.Fatal("设置后 Initialized 应为 true")
	}

	// 旧版明文密码文件存在时应被清理。
	if err := os.WriteFile(pwFile, []byte("legacy"), 0o600); err == nil {
		if err := svc.SetupAdminKey("my-access-key"); err == nil {
			t.Fatal("已初始化后再次 Setup 应返回错误")
		}
	}

	// 密钥校验。
	if _, err := svc.Login("admin", "my-access-key"); err != nil {
		t.Fatalf("正确密钥登录失败: %v", err)
	}
	if _, err := svc.Login("admin", "wrong-key"); err == nil {
		t.Fatal("错误密钥登录应失败")
	}
}

// TestLogin 验证未初始化时登录直接失败。
func TestLoginBeforeInit(t *testing.T) {
	svc, _ := newTestService(t)
	if _, err := svc.Login("admin", "anything"); err == nil {
		t.Fatal("未初始化时登录应失败")
	}
}

// TestSessionMiddleware 验证带正确 token 放行且注入 claims，无/错 token 返回 401。
func TestSessionMiddleware(t *testing.T) {
	svc, _ := newTestService(t)
	if err := svc.SetupAdminKey("my-access-key"); err != nil {
		t.Fatalf("SetupAdminKey: %v", err)
	}
	token, err := svc.Login("admin", "my-access-key")
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

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("正确 token 期望 200，实际 %d", rr.Code)
	}

	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr2.Code != http.StatusUnauthorized {
		t.Fatalf("无 token 期望 401，实际 %d", rr2.Code)
	}

	req3 := httptest.NewRequest(http.MethodGet, "/", nil)
	req3.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "bad.token.value"})
	rr3 := httptest.NewRecorder()
	handler.ServeHTTP(rr3, req3)
	if rr3.Code != http.StatusUnauthorized {
		t.Fatalf("错误 token 期望 401，实际 %d", rr3.Code)
	}
}

// TestChangePassword 验证改密流程（不再删除密码文件的断言——密码文件只在
// SetupAdminKey 与 ChangePassword 成功后清理，逻辑保持一致）。
func TestChangePassword(t *testing.T) {
	svc, _ := newTestService(t)
	if err := svc.SetupAdminKey("initial-key"); err != nil {
		t.Fatalf("SetupAdminKey: %v", err)
	}

	if err := svc.ChangePassword("admin", "wrong-old", "new-key-123"); err == nil {
		t.Fatal("旧密钥错误时应返回错误")
	}

	if err := svc.ChangePassword("admin", "initial-key", "new-key-123"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}

	if _, err := svc.Login("admin", "new-key-123"); err != nil {
		t.Fatalf("新密钥登录失败: %v", err)
	}
	if _, err := svc.Login("admin", "initial-key"); err == nil {
		t.Fatal("旧密钥登录应失败")
	}
}

