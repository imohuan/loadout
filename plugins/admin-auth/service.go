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
	"loadout/core/db"
	"loadout/core/store"
	"loadout/plugins/types"
)

// claimsContextKey 会话 Claims 在 request context 中的键名。
const claimsContextKey = "claims"

// Service 管理员认证服务。
type Service struct {
	st   *store.Store
	lg   *slog.Logger
	repo *db.Repository // SQLite 用户数据源（装配后注入；nil 时回退 JSON）
}

// NewService 创建管理员认证服务。lg 为 nil 时回落到 slog.Default()。
func NewService(st *store.Store, lg *slog.Logger) *Service {
	if lg == nil {
		lg = slog.Default()
	}
	return &Service{st: st, lg: lg}
}

// SetRepository 注入 SQLite 仓储（由装配层在 db 就绪后调用；测试可省略）。
func (s *Service) SetRepository(repo *db.Repository) { s.repo = repo }

// readUsers 读管理员账号（SQLite 优先，fallback users.json）。
func (s *Service) readUsers() ([]types.User, error) {
	if s.repo != nil {
		users, err := s.repo.ListUsers(context.Background())
		if err == nil {
			return users, nil
		}
		s.lg.Warn("adminauth: 从 SQLite 读用户失败，回退 JSON", "err", err)
	}
	var users []types.User
	if err := s.st.Read(types.FileUsers, &users); err != nil {
		if errors.Is(err, store.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return users, nil
}

// writeUsers 写管理员账号（SQLite 优先，fallback users.json）。
func (s *Service) writeUsers(users []types.User) error {
	if s.repo != nil {
		if err := s.repo.ReplaceUsers(context.Background(), users); err == nil {
			return nil
		} else {
			s.lg.Warn("adminauth: 写用户到 SQLite 失败，回退 JSON", "err", err)
		}
	}
	return s.st.Write(types.FileUsers, users)
}

// Login 校验用户名密码（users 的 bcrypt 哈希），成功签发 JWT 会话 token。
func (s *Service) Login(username, password string) (token string, err error) {
	users, err := s.readUsers()
	if err != nil {
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
//
// 额外支持「查询参数 sse_token」回退校验：EventSource 无法携带自定义 Header，
// 而桌面壳内 SSE 直连 127.0.0.1:<port> 时不会带上 wails.localhost 的登录 Cookie，
// 因此前端先用带 Cookie 的 fetch 换取短期 sse_token，再以 ?sse_token=xxx 建立 SSE。
// 两种方式任一一通过即放行，互不影响。
func (s *Service) SessionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, err := s.authenticate(r)
		if err != nil {
			writeUnauthorized(w)
			return
		}
		ctx := context.WithValue(r.Context(), claimsContextKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// authenticate 从 Cookie 或查询参数 sse_token 解析会话 JWT。
// 两者都缺失/无效时返回错误。
func (s *Service) authenticate(r *http.Request) (*auth.Claims, error) {
	// 优先 Cookie（普通同源请求）。
	if c, err := r.Cookie(auth.SessionCookieName); err == nil {
		if claims, err := auth.ParseToken(s.st.SecretKey(), c.Value); err == nil {
			return claims, nil
		}
	}
	// 回退：SSE 等无法带 Cookie 的场景用查询参数 token。
	if tok := r.URL.Query().Get("sse_token"); tok != "" {
		if claims, err := auth.ParseToken(s.st.SecretKey(), tok); err == nil {
			return claims, nil
		}
	}
	return nil, errors.New("adminauth: 未认证")
}

// ClaimsFromContext 从 request context 取会话 Claims（由 SessionMiddleware 注入）。
// 未注入时返回 nil。
func (s *Service) ClaimsFromContext(ctx context.Context) *auth.Claims {
	if v := ctx.Value(claimsContextKey); v != nil {
		if c, ok := v.(*auth.Claims); ok {
			return c
		}
	}
	return nil
}

// ChangePassword 校验旧密码后改新密码；成功后删除 config.AdminPasswordFile（若存在）。
func (s *Service) ChangePassword(username, oldPw, newPw string) error {
	users, err := s.readUsers()
	if err != nil {
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

	if err := s.writeUsers(users); err != nil {
		return err
	}
	if err := os.Remove(config.AdminPasswordFile); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// HasUsers 判断是否已有账号（供首启判断）。
func (s *Service) HasUsers() bool {
	users, err := s.readUsers()
	if err != nil {
		return false
	}
	return len(users) > 0
}

// EnsureFirstRun 首启流程：无账号时生成随机密码（auth.GeneratePassword(16)）、
// 创建 admin 用户、把明文密码写入 config.AdminPasswordFile（0600）、日志打印提示。
// 已有账号则不做任何事。返回是否执行了首启。
func (s *Service) EnsureFirstRun() (bool, error) {
	if s.HasUsers() {
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
	if err := s.writeUsers(users); err != nil {
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
