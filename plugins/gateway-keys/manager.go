package gatewaykeys

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"loadout/core/auth"
	"loadout/core/db"
	"loadout/core/store"
	"loadout/plugins/types"
)

// defaultHeaderName MCP key 校验时使用的默认 header 名。
const defaultHeaderName = "X-Loadout-Key"

// ctxAPIKey SkKeyMiddleware 把命中的 key 记录写入 request context 所用的 key。
const ctxAPIKey = "api-key"

// ContextWithAPIKey 把命中的 API key 记录写入 context，供下游处理器读取。
func ContextWithAPIKey(ctx context.Context, key types.APIKey) context.Context {
	return context.WithValue(ctx, ctxAPIKey, key)
}

// APIKeyFromContext 从 context 取出 SkKeyMiddleware 命中的 API key 记录；
// 未命中（如单测直接调用 handler）返回 false，调用方按「不限制」处理。
func APIKeyFromContext(ctx context.Context) (types.APIKey, bool) {
	key, ok := ctx.Value(ctxAPIKey).(types.APIKey)
	return key, ok
}

// AllowedModel 判断 model 是否被 key 的 models 白名单允许：
// 白名单为空或含 "*" 表示不限制；否则按 * / prefix* / 精确匹配。
func AllowedModel(allowed []string, model string) bool {
	if len(allowed) == 0 {
		return true
	}
	return types.MatchModels(allowed, model)
}

// Manager 管理 sk- key 与 MCP endpoint key（签发 + 校验中间件）。
type Manager struct {
	mu   sync.Mutex   // mu 串行化读-改-写，避免并发竞争。
	st   *store.Store // st 数据目录。
	repo *db.Repository // SQLite 密钥数据源（装配后注入；nil 时回退 JSON）
}

// NewManager 基于数据目录构造 Manager。
func NewManager(st *store.Store) *Manager {
	return &Manager{st: st}
}

// SetRepository 注入 SQLite 仓储（由装配层在 db 就绪后调用；测试可省略）。
func (m *Manager) SetRepository(repo *db.Repository) { m.repo = repo }

// CreateAPIKey 生成 sk- key：完整 key 只返回一次（full），落盘只存哈希 + 前缀。
func (m *Manager) CreateAPIKey(name string, models []string) (full, prefix string, err error) {
	full, hash, err := auth.GenerateSecretKey("sk-")
	if err != nil {
		return "", "", err
	}
	id, err := newID()
	if err != nil {
		return "", "", err
	}
	prefix = prefixOf(full)

	m.mu.Lock()
	defer m.mu.Unlock()

	keys, err := m.readAPIKeys()
	if err != nil {
		return "", "", err
	}
	keys = append(keys, types.APIKey{
		ID:        id,
		Name:      name,
		Prefix:    prefix,
		Hash:      hash,
		Models:    models,
		Enabled:   true,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	})
	if err := m.writeAPIKeys(keys); err != nil {
		return "", "", fmt.Errorf("gateway-keys: 写入 %s 失败: %w", types.FileAPIKeys, err)
	}
	return full, prefix, nil
}

// ListAPIKeys 返回全部 sk- key（不含完整 key，只有哈希/前缀）。
func (m *Manager) ListAPIKeys() ([]types.APIKey, error) {
	return m.readAPIKeys()
}

// DeleteAPIKey 按 id 删除；不存在返回错误。
func (m *Manager) DeleteAPIKey(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	keys, err := m.readAPIKeys()
	if err != nil {
		return err
	}
	idx := -1
	for i := range keys {
		if keys[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("gateway-keys: API key 不存在: %s", id)
	}
	keys = append(keys[:idx], keys[idx+1:]...)
	if err := m.writeAPIKeys(keys); err != nil {
		return fmt.Errorf("gateway-keys: 写入 %s 失败: %w", types.FileAPIKeys, err)
	}
	return nil
}

// SetAPIKeyEnabled 启用/禁用某 key；不存在返回错误。
func (m *Manager) SetAPIKeyEnabled(id string, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	keys, err := m.readAPIKeys()
	if err != nil {
		return err
	}
	found := false
	for i := range keys {
		if keys[i].ID == id {
			keys[i].Enabled = enabled
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("gateway-keys: API key 不存在: %s", id)
	}
	if err := m.writeAPIKeys(keys); err != nil {
		return fmt.Errorf("gateway-keys: 写入 %s 失败: %w", types.FileAPIKeys, err)
	}
	return nil
}

// SetMCPKey 为某端点生成/重置一把 key（header 名默认 X-Loadout-Key），返回完整 key。
func (m *Manager) SetMCPKey(endpoint string) (full string, err error) {
	full, hash, err := auth.GenerateSecretKey("")
	if err != nil {
		return "", err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	keys, err := m.readMCPKeys()
	if err != nil {
		return "", err
	}
	found := false
	for i := range keys {
		if keys[i].Endpoint == endpoint {
			keys[i].Hash = hash
			keys[i].HeaderName = defaultHeaderName
			found = true
			break
		}
	}
	if !found {
		keys = append(keys, types.MCPKey{
			Endpoint:   endpoint,
			HeaderName: defaultHeaderName,
			Hash:       hash,
		})
	}
	if err := m.writeMCPKeys(keys); err != nil {
		return "", fmt.Errorf("gateway-keys: 写入 %s 失败: %w", types.FileMCPKeys, err)
	}
	return full, nil
}

// DisableMCPKey 关闭某端点的认证（删除其 key 记录）；无记录时返回 nil。
func (m *Manager) DisableMCPKey(endpoint string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	keys, err := m.readMCPKeys()
	if err != nil {
		return err
	}
	var remaining []types.MCPKey
	for _, k := range keys {
		if k.Endpoint != endpoint {
			remaining = append(remaining, k)
		}
	}
	if len(remaining) == len(keys) {
		return nil
	}
	if err := m.writeMCPKeys(remaining); err != nil {
		return fmt.Errorf("gateway-keys: 写入 %s 失败: %w", types.FileMCPKeys, err)
	}
	return nil
}

// MCPKeyEnabled 判断某端点是否已开启认证（存在 key 记录）。
func (m *Manager) MCPKeyEnabled(endpoint string) bool {
	keys, err := m.readMCPKeys()
	if err != nil {
		return false
	}
	for _, k := range keys {
		if k.Endpoint == endpoint {
			return true
		}
	}
	return false
}

// SkKeyMiddleware 校验 Authorization: Bearer sk-xxx：查 api_keys.json 中 enabled 且
// 哈希匹配的 key。通过则放行并把 key 记录存入 request context（key 名 "api-key"）；
// 失败返回 401 + JSON。
func (m *Manager) SkKeyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		full, ok := bearerToken(r)
		if !ok {
			writeAuthError(w, "缺少或非法的 Authorization 头（期望 Bearer sk-xxx）")
			return
		}
		hash := auth.HashSecretKey(full)

		keys, err := m.readAPIKeys()
		if err != nil {
			writeServerError(w)
			return
		}
		var matched *types.APIKey
		for i := range keys {
			if keys[i].Enabled && keys[i].Hash == hash {
				matched = &keys[i]
				break
			}
		}
		if matched == nil {
			writeAuthError(w, "无效的 API key")
			return
		}

		ctx := ContextWithAPIKey(r.Context(), *matched)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// MCPKeyMiddleware 按请求路径（endpoint）查 mcp_keys.json：
//   - 该端点无 key 记录 → 放行（认证未开启）；
//   - 有记录 → 校验自定义 header（header_name，默认 X-Loadout-Key）值哈希匹配，失败 401。
func (m *Manager) MCPKeyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		endpoint := r.URL.Path

		keys, err := m.readMCPKeys()
		if err != nil {
			writeServerError(w)
			return
		}
		var rec *types.MCPKey
		for i := range keys {
			if keys[i].Endpoint == endpoint {
				rec = &keys[i]
				break
			}
		}
		if rec == nil {
			next.ServeHTTP(w, r)
			return
		}

		headerName := rec.HeaderName
		if headerName == "" {
			headerName = defaultHeaderName
		}
		provided := r.Header.Get(headerName)
		if provided == "" || auth.HashSecretKey(provided) != rec.Hash {
			writeAuthError(w, "无效的 MCP key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// readAPIKeys 读 API key 列表（SQLite 优先，fallback api_keys.json）；文件不存在视为空数组。
func (m *Manager) readAPIKeys() ([]types.APIKey, error) {
	if m.repo != nil {
		keys, err := m.repo.ListAPIKeys(context.Background())
		if err == nil {
			return keys, nil
		}
	}
	var keys []types.APIKey
	if err := m.st.Read(types.FileAPIKeys, &keys); err != nil {
		if errors.Is(err, store.ErrNotExist) {
			return []types.APIKey{}, nil
		}
		return nil, fmt.Errorf("gateway-keys: 读取 %s 失败: %w", types.FileAPIKeys, err)
	}
	return keys, nil
}

// writeAPIKeys 写 API key 列表（SQLite 优先，fallback api_keys.json）。
func (m *Manager) writeAPIKeys(keys []types.APIKey) error {
	if m.repo != nil {
		if err := m.repo.ReplaceAPIKeys(context.Background(), keys); err == nil {
			return nil
		}
	}
	if m.repo != nil {
		if err := m.repo.ReplaceAPIKeys(context.Background(), keys); err == nil {
			return nil
		}
	}
	return m.st.Write(types.FileAPIKeys, keys)
}

// readMCPKeys 读 MCP endpoint key 列表（SQLite 优先，fallback mcp_keys.json）；文件不存在视为空数组。
func (m *Manager) readMCPKeys() ([]types.MCPKey, error) {
	if m.repo != nil {
		keys, err := m.repo.ListMCPKeys(context.Background())
		if err == nil {
			return keys, nil
		}
	}
	var keys []types.MCPKey
	if err := m.st.Read(types.FileMCPKeys, &keys); err != nil {
		if errors.Is(err, store.ErrNotExist) {
			return []types.MCPKey{}, nil
		}
		return nil, fmt.Errorf("gateway-keys: 读取 %s 失败: %w", types.FileMCPKeys, err)
	}
	return keys, nil
}

// writeMCPKeys 写 MCP endpoint key 列表（SQLite 优先，fallback mcp_keys.json）。
func (m *Manager) writeMCPKeys(keys []types.MCPKey) error {
	if m.repo != nil {
		if err := m.repo.ReplaceMCPKeys(context.Background(), keys); err == nil {
			return nil
		}
	}
	if m.repo != nil {
		if err := m.repo.ReplaceMCPKeys(context.Background(), keys); err == nil {
			return nil
		}
	}
	return m.st.Write(types.FileMCPKeys, keys)
}

// bearerToken 解析 Authorization: Bearer <token>，返回 token；非法返回 false。
func bearerToken(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) {
		return "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(h, prefix))
	if token == "" {
		return "", false
	}
	return token, true
}

// writeAuthError 返回 401 + 标准 OpenAI 风格错误 JSON。
func writeAuthError(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "invalid_request_error",
		},
	})
}

// writeServerError 返回 500 + 内部错误 JSON。
func writeServerError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": "服务器内部错误",
			"type":    "internal_error",
		},
	})
}

// prefixOf 取 full 的前 6 个字符作为展示前缀（"sk-abc" 风格）。
func prefixOf(full string) string {
	if len(full) <= 6 {
		return full
	}
	return full[:6]
}

// newID 生成 8 字节 crypto/rand 的 hex 编码作为唯一 ID。
func newID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("gateway-keys: 生成随机 ID 失败: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
