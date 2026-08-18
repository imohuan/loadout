package adminauth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"loadout/core/auth"
	"loadout/core/config"
	"loadout/core/store"
	"loadout/plugins/types"
)

// claimsContextKey 会话 Claims 在 request context 中的键名。
const claimsContextKey = "claims"

// Service 管理员认证服务。
type Service struct {
	st *store.Store
	lg *slog.Logger
}

// NewService 创建管理员认证服务。lg 为 nil 时回落到 slog.Default()。
func NewService(st *store.Store, lg *slog.Logger) *Service {
	if lg == nil {
		lg = slog.Default()
	}
	return &Service{st: st, lg: lg}
}

// Login 校验用户名密码（users.json 的 bcrypt 哈希），成功签发 JWT 会话 token。
func (s *Service) Login(username, password string) (token string, err error) {
	var users []types.User
	if err := s.st.Read(types.FileUsers, &users); err != nil {
		return "", err
	}
	for _, u := range users {
		if u.Username == username && auth.CheckPassword(u.PasswordHash, password) {
			return auth.SignToken(s.st.SecretKey(), username, time.Duration(config.SessionTTLHours)*time.Hour)
		}
	}
	return "", errors.New("adminauth: 用户名或密码错误")
}

// SessionMiddleware 校验 HttpOnly Cookie（auth.SessionCookieName）里的 JWT。
// 通过则把 *auth.Claims 存入 request context（key 为字符串 "claims"）；失败返回 401。
func (s *Service) SessionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(auth.SessionCookieName)
		if err != nil {
			writeUnauthorized(w)
			return
		}
		claims, err := auth.ParseToken(s.st.SecretKey(), cookie.Value)
		if err != nil {
			writeUnauthorized(w)
			return
		}
		ctx := context.WithValue(r.Context(), claimsContextKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ChangePassword 校验旧密码后改新密码；成功后删除 config.AdminPasswordFile（若存在）。
func (s *Service) ChangePassword(username, oldPw, newPw string) error {
	var users []types.User
	if err := s.st.Read(types.FileUsers, &users); err != nil {
		return err
	}

	found := false
	for i, u := range users {
		if u.Username != username {
			continue
		}
		found = true
		if !auth.CheckPassword(u.PasswordHash, oldPw) {
			return errors.New("adminauth: 旧密码错误")
		}
		hash, err := auth.HashPassword(newPw)
		if err != nil {
			return err
		}
		users[i].PasswordHash = hash
		users[i].PasswordChanged = true
	}
	if !found {
		return errors.New("adminauth: 用户不存在")
	}

	if err := s.st.Write(types.FileUsers, users); err != nil {
		return err
	}
	if err := os.Remove(config.AdminPasswordFile); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// HasUsers 判断 users.json 是否已有账号（供首启判断）。
func (s *Service) HasUsers() bool {
	var users []types.User
	if err := s.st.Read(types.FileUsers, &users); err != nil {
		return false
	}
	return len(users) > 0
}

// EnsureFirstRun 首启流程：users.json 不存在时生成随机密码（auth.GeneratePassword(16)）、
// 创建 admin 用户、把明文密码写入 config.AdminPasswordFile（0600）、日志打印提示。
// 已存在则不做任何事。返回是否执行了首启。
func (s *Service) EnsureFirstRun() (bool, error) {
	if s.st.Exists(types.FileUsers) {
		return false, nil
	}

	pw, err := auth.GeneratePassword(16)
	if err != nil {
		return false, err
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return false, err
	}

	users := []types.User{{
		Username:        "admin",
		PasswordHash:    hash,
		PasswordChanged: false,
	}}
	if err := s.st.Write(types.FileUsers, users); err != nil {
		return false, err
	}

	// 明文写入 admin-password 文件（0600，目录不存在先建）。
	if dir := filepath.Dir(config.AdminPasswordFile); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return false, err
		}
	}
	if err := os.WriteFile(config.AdminPasswordFile, []byte(pw), 0o600); err != nil {
		return false, err
	}

	s.lg.Info("首次启动，管理员账号 admin，初始密码见 " + config.AdminPasswordFile)
	return true, nil
}

// writeUnauthorized 写出 401 的 JSON 错误响应。
func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": "未登录或会话已过期",
			"type":    "invalid_request_error",
		},
	})
}
