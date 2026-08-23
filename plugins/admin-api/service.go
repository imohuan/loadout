// Package adminapi 实现 Loadout 管理后台 REST API 插件：登录会话、概览，
// 以及渠道、能力路由、MCP 服务器、工具状态、分组、密钥、技能、预设、
// 设置等运行时数据的增删改查。
package adminapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"loadout/core/auth"
	"loadout/core/config"
	"loadout/core/db"
	"loadout/core/mcpkit"
	"loadout/core/plugin"
	"loadout/core/store"
	"loadout/plugins/admin-auth"
	"loadout/plugins/contracts"
	"loadout/plugins/gateway-keys"
	mcphub "loadout/plugins/mcp-hub"
	"loadout/plugins/skills"
	"loadout/plugins/types"
	unifyai "loadout/plugins/unifyai"
)

// sessionCookieName 管理后台会话 Cookie 名（与 core/auth.SessionCookieName 保持一致）。
const sessionCookieName = "loadout_session"

// Service 管理后台 REST API 服务。
type Service struct {
	st             *store.Store
	lg             *slog.Logger
	auth           *adminauth.Service
	keys           *gatewaykeys.Manager
	skill          *skills.Service
	hub            *mcphub.Service // MCP 聚合网关（配置变更后失效其索引缓存）
	unify          *unifyai.Service
	routing        *db.Repository
	health         contracts.ModelHealth
	routeLog       contracts.RouteLog
	sqlDB          *sql.DB                     // 共享 SQLite 连接（route-log 统计直接查询 route_requests 用）
	pluginCount    int                         // 已装配的插件总数（由 server 装配层注入，概览页展示）
	checksProvider func() []plugin.PluginCheck // 插件自检结果提供者（由 server 装配层注入）
}

// NewService 组装管理后台服务。lg 为 nil 时回落到 slog.Default()；hub 可为 nil（测试）。
func NewService(st *store.Store, lg *slog.Logger, auth *adminauth.Service, keys *gatewaykeys.Manager, skill *skills.Service, hub *mcphub.Service, unify *unifyai.Service) *Service {
	if lg == nil {
		lg = slog.Default()
	}
	return &Service{st: st, lg: lg, auth: auth, keys: keys, skill: skill, hub: hub, unify: unify}
}

// SetPluginCount 注入已装配插件总数（由 apps/server 装配完成后调用）。
func (s *Service) SetPluginCount(n int) { s.pluginCount = n }

// SetRoutingServices connects the routing-only SQLite contracts after plugin
// assembly. Other legacy admin data remains outside this migration scope.
func (s *Service) SetRoutingServices(database *sql.DB, routing *db.Repository, health contracts.ModelHealth, routeLog contracts.RouteLog) {
	s.sqlDB, s.routing, s.health, s.routeLog = database, routing, health, routeLog
}

// invalidateHub 在 MCP/分组/工具开关配置变更后失效 mcp-hub 的索引缓存（hub 为 nil 时安全跳过）。
func (s *Service) invalidateHub() {
	if s.hub != nil {
		s.hub.Invalidate()
	}
}

// SetChecksProvider 注入插件自检结果提供者（由 apps/server 装配完成后调用）。
func (s *Service) SetChecksProvider(fn func() []plugin.PluginCheck) { s.checksProvider = fn }

// Routes 返回插件的全部路由规格。session 路由已用 admin-auth 的会话中间件包装，
// public 路由（登录）不包装；供 Apply 注册与测试复用。
func (s *Service) Routes() []plugin.RouteSpec {
	return []plugin.RouteSpec{
		// 认证
		{Method: http.MethodPost, Pattern: "POST /api/login", Auth: plugin.AuthPublic, Handler: http.HandlerFunc(s.handleLogin)},
		{Method: http.MethodPost, Pattern: "POST /api/sso/login", Auth: plugin.AuthPublic, Handler: http.HandlerFunc(s.handleSSOLogin)},
		// 登出幂等：无论当前是否有会话都返回成功、清空 Cookie；避免 UI 在会话失效态调登出时 401。
		{Method: http.MethodPost, Pattern: "POST /api/logout", Auth: plugin.AuthPublic, Handler: http.HandlerFunc(s.handleLogout)},

		// 概览
		{Method: http.MethodGet, Pattern: "GET /api/overview", Auth: plugin.AuthSession, Handler: s.session(s.handleOverview)},
		{Method: http.MethodGet, Pattern: "GET /api/plugins", Auth: plugin.AuthSession, Handler: s.session(s.handlePlugins)},

		// 渠道
		{Method: http.MethodGet, Pattern: "GET /api/channels", Auth: plugin.AuthSession, Handler: s.session(s.handleChannelsList)},
		{Method: http.MethodPost, Pattern: "POST /api/channels", Auth: plugin.AuthSession, Handler: s.session(s.handleChannelCreate)},
		{Method: http.MethodPost, Pattern: "POST /api/channels/probe", Auth: plugin.AuthSession, Handler: s.session(s.handleChannelProbe)},
		{Method: http.MethodPost, Pattern: "POST /api/channels/test", Auth: plugin.AuthSession, Handler: s.session(s.handleChannelTest)},

		// 模型测试（后台代理上游，规避跨域；测试请求不写转发日志，访问摘要随响应回带）
		{Method: http.MethodPost, Pattern: "POST /api/test/models", Auth: plugin.AuthSession, Handler: s.session(s.handleTestModels)},
		{Method: http.MethodPost, Pattern: "POST /api/test/chat", Auth: plugin.AuthSession, Handler: s.session(s.handleTestChat)},
		{Method: http.MethodPut, Pattern: "PUT /api/channels/{id}", Auth: plugin.AuthSession, Handler: s.session(s.handleChannelUpdate)},
		{Method: http.MethodDelete, Pattern: "DELETE /api/channels/{id}", Auth: plugin.AuthSession, Handler: s.session(s.handleChannelDelete)},
		{Method: http.MethodPost, Pattern: "POST /api/channels/{id}/refresh-models", Auth: plugin.AuthSession, Handler: s.session(s.handleChannelRefreshModels)},
		{Method: http.MethodPut, Pattern: "PUT /api/channels/{id}/models", Auth: plugin.AuthSession, Handler: s.session(s.handleChannelModelsReplace)},
		{Method: http.MethodPost, Pattern: "POST /api/channels/{id}/move", Auth: plugin.AuthSession, Handler: s.session(s.handleChannelMove)},
		{Method: http.MethodPost, Pattern: "POST /api/channels/reorder", Auth: plugin.AuthSession, Handler: s.session(s.handleChannelsReorder)},

		// 能力路由
		{Method: http.MethodGet, Pattern: "GET /api/capability-routes", Auth: plugin.AuthSession, Handler: s.session(s.handleCapabilityRoutesList)},
		{Method: http.MethodPost, Pattern: "POST /api/capability-routes", Auth: plugin.AuthSession, Handler: s.session(s.handleCapabilityRouteCreate)},
		{Method: http.MethodPut, Pattern: "PUT /api/capability-routes", Auth: plugin.AuthSession, Handler: s.session(s.handleCapabilityRoutesReplace)},
		{Method: http.MethodDelete, Pattern: "DELETE /api/capability-routes", Auth: plugin.AuthSession, Handler: s.session(s.handleCapabilityRouteDelete)},

		// MCP 服务器
		{Method: http.MethodGet, Pattern: "GET /api/mcp-servers", Auth: plugin.AuthSession, Handler: s.session(s.handleMCPServersList)},
		{Method: http.MethodPost, Pattern: "POST /api/mcp-servers", Auth: plugin.AuthSession, Handler: s.session(s.handleMCPServerCreate)},
		{Method: http.MethodPut, Pattern: "PUT /api/mcp-servers", Auth: plugin.AuthSession, Handler: s.session(s.handleMCPServersReplace)},
		{Method: http.MethodPut, Pattern: "PUT /api/mcp-servers/{id}", Auth: plugin.AuthSession, Handler: s.session(s.handleMCPServerUpdate)},
		{Method: http.MethodDelete, Pattern: "DELETE /api/mcp-servers", Auth: plugin.AuthSession, Handler: s.session(s.handleMCPServerDelete)},
		{Method: http.MethodPost, Pattern: "POST /api/mcp-servers/test", Auth: plugin.AuthSession, Handler: s.session(s.handleMCPServerTest)},
		{Method: http.MethodGet, Pattern: "GET /api/mcp-tools", Auth: plugin.AuthSession, Handler: s.session(s.handleMCPToolsList)},
		{Method: http.MethodGet, Pattern: "GET /api/mcp-tools/schema", Auth: plugin.AuthSession, Handler: s.session(s.handleMCPToolSchema)},
		{Method: http.MethodPost, Pattern: "POST /api/mcp-tools/call", Auth: plugin.AuthSession, Handler: s.session(s.handleMCPToolCall)},

		// 工具状态
		{Method: http.MethodGet, Pattern: "GET /api/tools-state", Auth: plugin.AuthSession, Handler: s.session(s.handleToolsStateGet)},
		{Method: http.MethodPut, Pattern: "PUT /api/tools-state", Auth: plugin.AuthSession, Handler: s.session(s.handleToolsStatePut)},

		// 分组
		{Method: http.MethodGet, Pattern: "GET /api/groups", Auth: plugin.AuthSession, Handler: s.session(s.handleGroupsList)},
		{Method: http.MethodPost, Pattern: "POST /api/groups", Auth: plugin.AuthSession, Handler: s.session(s.handleGroupCreate)},
		{Method: http.MethodPut, Pattern: "PUT /api/groups", Auth: plugin.AuthSession, Handler: s.session(s.handleGroupsReplace)},
		{Method: http.MethodDelete, Pattern: "DELETE /api/groups", Auth: plugin.AuthSession, Handler: s.session(s.handleGroupDelete)},

		// 密钥
		{Method: http.MethodGet, Pattern: "GET /api/keys", Auth: plugin.AuthSession, Handler: s.session(s.handleKeysList)},
		{Method: http.MethodPost, Pattern: "POST /api/keys/sk", Auth: plugin.AuthSession, Handler: s.session(s.handleCreateSKKey)},
		{Method: http.MethodDelete, Pattern: "DELETE /api/keys/sk/{id}", Auth: plugin.AuthSession, Handler: s.session(s.handleDeleteSKKey)},
		{Method: http.MethodPost, Pattern: "POST /api/keys/mcp", Auth: plugin.AuthSession, Handler: s.session(s.handleCreateMCPKey)},
		{Method: http.MethodDelete, Pattern: "DELETE /api/keys/mcp", Auth: plugin.AuthSession, Handler: s.session(s.handleDeleteMCPKey)},

		// 技能
		{Method: http.MethodGet, Pattern: "GET /api/skills", Auth: plugin.AuthSession, Handler: s.session(s.handleSkillsList)},
		{Method: http.MethodPost, Pattern: "POST /api/skills", Auth: plugin.AuthSession, Handler: s.session(s.handleSkillRegister)},
		{Method: http.MethodPost, Pattern: "POST /api/skills/install", Auth: plugin.AuthSession, Handler: s.session(s.handleSkillInstall)},
		{Method: http.MethodPost, Pattern: "POST /api/skills/import-zip", Auth: plugin.AuthSession, Handler: s.session(s.handleSkillImportZip)},
		{Method: http.MethodDelete, Pattern: "DELETE /api/skills/{name}", Auth: plugin.AuthSession, Handler: s.session(s.handleSkillDelete)},
		{Method: http.MethodGet, Pattern: "GET /api/skills/status", Auth: plugin.AuthSession, Handler: s.session(s.handleSkillsStatus)},
		{Method: http.MethodPost, Pattern: "POST /api/skills/sync", Auth: plugin.AuthSession, Handler: s.session(s.handleSkillSync)},
		{Method: http.MethodPost, Pattern: "POST /api/skills/check-updates", Auth: plugin.AuthSession, Handler: s.session(s.handleSkillCheckUpdates)},
		{Method: http.MethodGet, Pattern: "GET /api/skills/update-stream", Auth: plugin.AuthSession, Handler: s.session(s.handleSkillUpdateStream)},
		{Method: http.MethodPost, Pattern: "POST /api/skills/restore", Auth: plugin.AuthSession, Handler: s.session(s.handleSkillRestore)},
		{Method: http.MethodPost, Pattern: "POST /api/skills/restore-all", Auth: plugin.AuthSession, Handler: s.session(s.handleSkillRestoreAll)},
		{Method: http.MethodGet, Pattern: "GET /api/presets", Auth: plugin.AuthSession, Handler: s.session(s.handlePresetsList)},
		{Method: http.MethodPost, Pattern: "POST /api/presets", Auth: plugin.AuthSession, Handler: s.session(s.handlePresetCreate)},
		{Method: http.MethodDelete, Pattern: "DELETE /api/presets", Auth: plugin.AuthSession, Handler: s.session(s.handlePresetDelete)},
		{Method: http.MethodPost, Pattern: "POST /api/presets/apply", Auth: plugin.AuthSession, Handler: s.session(s.handlePresetApply)},

		// UnifyAI 配置同步
		{Method: http.MethodGet, Pattern: "GET /api/unifyai/platforms", Auth: plugin.AuthSession, Handler: s.session(s.handleUnifyaiPlatforms)},
		{Method: http.MethodPost, Pattern: "POST /api/unifyai/run", Auth: plugin.AuthSession, Handler: s.session(s.handleUnifyaiRun)},
		{Method: http.MethodGet, Pattern: "GET /api/unifyai/stream", Auth: plugin.AuthSession, Handler: s.session(s.handleUnifyaiStream)},
		{Method: http.MethodGet, Pattern: "GET /api/unifyai/mcp-servers", Auth: plugin.AuthSession, Handler: s.session(s.handleUnifyaiMcpServersList)},
		{Method: http.MethodPut, Pattern: "PUT /api/unifyai/mcp-servers", Auth: plugin.AuthSession, Handler: s.session(s.handleUnifyaiMcpServersSave)},

		// 聚合模型
		{Method: http.MethodGet, Pattern: "GET /api/aggregates", Auth: plugin.AuthSession, Handler: s.session(s.handleAggregatesList)},
		{Method: http.MethodPost, Pattern: "POST /api/aggregates", Auth: plugin.AuthSession, Handler: s.session(s.handleAggregateCreate)},
		{Method: http.MethodPut, Pattern: "PUT /api/aggregates", Auth: plugin.AuthSession, Handler: s.session(s.handleAggregatesReplace)},
		{Method: http.MethodDelete, Pattern: "DELETE /api/aggregates", Auth: plugin.AuthSession, Handler: s.session(s.handleAggregateDelete)},
		{Method: http.MethodGet, Pattern: "GET /api/model-health", Auth: plugin.AuthSession, Handler: s.session(s.handleModelHealthList)},
		{Method: http.MethodGet, Pattern: "GET /api/model-status", Auth: plugin.AuthSession, Handler: s.session(s.handleModelStatusList)},
		{Method: http.MethodPatch, Pattern: "PATCH /api/model-status/models/{channel_id}/{model}", Auth: plugin.AuthSession, Handler: s.session(s.handleModelStatusSet)},
		{Method: http.MethodPatch, Pattern: "PATCH /api/model-status/models/{channel_id}", Auth: plugin.AuthSession, Handler: s.session(s.handleModelStatusSetBatch)},
		{Method: http.MethodDelete, Pattern: "DELETE /api/model-status/models/{channel_id}/{model}", Auth: plugin.AuthSession, Handler: s.session(s.handleModelStatusDelete)},
		{Method: http.MethodDelete, Pattern: "DELETE /api/model-status/models/{channel_id}", Auth: plugin.AuthSession, Handler: s.session(s.handleModelStatusDeleteBatch)},
		{Method: http.MethodPatch, Pattern: "PATCH /api/model-status/channels/{channel_id}", Auth: plugin.AuthSession, Handler: s.session(s.handleChannelStatusSet)},
		{Method: http.MethodPost, Pattern: "POST /api/model-status/models/{channel_id}/{model}/recover", Auth: plugin.AuthSession, Handler: s.session(s.handleModelStatusRecover)},
		{Method: http.MethodPost, Pattern: "POST /api/model-status/models/{channel_id}/recover", Auth: plugin.AuthSession, Handler: s.session(s.handleModelStatusRecoverBatch)},
		{Method: http.MethodPost, Pattern: "POST /api/model-status/channels/{channel_id}/recover", Auth: plugin.AuthSession, Handler: s.session(s.handleChannelStatusRecover)},
		{Method: http.MethodPost, Pattern: "POST /api/model-status/check", Auth: plugin.AuthSession, Handler: s.session(s.handleModelStatusCheck)},
		{Method: http.MethodPost, Pattern: "POST /api/model-status/recover-all", Auth: plugin.AuthSession, Handler: s.session(s.handleModelStatusRecoverAll)},
		{Method: http.MethodPost, Pattern: "POST /api/model-status/channels/{channel_id}/recover-all", Auth: plugin.AuthSession, Handler: s.session(s.handleModelStatusRecoverAllChannel)},
		{Method: http.MethodPost, Pattern: "POST /api/model-status/recover-all-channels", Auth: plugin.AuthSession, Handler: s.session(s.handleModelStatusRecoverAllChannels)},
		{Method: http.MethodGet, Pattern: "GET /api/route-logs", Auth: plugin.AuthSession, Handler: s.session(s.handleRouteLogsList)},
		{Method: http.MethodGet, Pattern: "GET /api/route-logs/{request_id}", Auth: plugin.AuthSession, Handler: s.session(s.handleRouteLogDetail)},
		{Method: http.MethodDelete, Pattern: "DELETE /api/route-logs", Auth: plugin.AuthSession, Handler: s.session(s.handleRouteLogsClear)},

		// 统计
		{Method: http.MethodGet, Pattern: "GET /api/stats/mcp", Auth: plugin.AuthSession, Handler: s.session(s.handleStatsMcp)},
		{Method: http.MethodGet, Pattern: "GET /api/stats/models", Auth: plugin.AuthSession, Handler: s.session(s.handleStatsModels)},

		// 设置
		{Method: http.MethodGet, Pattern: "GET /api/settings", Auth: plugin.AuthSession, Handler: s.session(s.handleSettingsGet)},
		{Method: http.MethodPut, Pattern: "PUT /api/settings", Auth: plugin.AuthSession, Handler: s.session(s.handleSettingsPut)},
		{Method: http.MethodPost, Pattern: "POST /api/change-password", Auth: plugin.AuthSession, Handler: s.session(s.handleChangePassword)},

		// 配置导入导出（设置页）
		{Method: http.MethodPost, Pattern: "POST /api/config/export", Auth: plugin.AuthSession, Handler: s.session(s.handleConfigExport)},
		{Method: http.MethodPost, Pattern: "POST /api/config/import/preview", Auth: plugin.AuthSession, Handler: s.session(s.handleConfigImportPreview)},
		{Method: http.MethodPost, Pattern: "POST /api/config/import", Auth: plugin.AuthSession, Handler: s.session(s.handleConfigImport)},
	}
}

// Handler 返回包含全部路由的 HTTP 多路复用器（单端口分发与测试复用）。
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, spec := range s.Routes() {
		mux.Handle(spec.Pattern, spec.Handler)
	}
	return mux
}

// session 用 admin-auth 的 SessionMiddleware 包装 handler，要求有效管理员会话。
func (s *Service) session(h http.HandlerFunc) http.Handler {
	return s.auth.SessionMiddleware(h)
}

// selfCheck 自检核心配置是否可读（概览页与启动日志展示）。
// SQLite 装配时检查各表可查询；否则回退检查 JSON 文件。
func (s *Service) selfCheck() []plugin.Issue {
	names := []string{types.FileChannels, types.FileCapabilityRoutes, types.FileMCPServers, types.FileSettings}
	var issues []plugin.Issue
	if s.routing != nil {
		ctx := context.Background()
		if _, err := s.routing.ListCapabilityRoutes(ctx); err != nil {
			issues = append(issues, plugin.Issue{Level: "error", Message: "capability_routes 表读取失败: " + err.Error()})
		}
		if _, err := s.routing.ListMCPServers(ctx); err != nil {
			issues = append(issues, plugin.Issue{Level: "error", Message: "mcp_servers 表读取失败: " + err.Error()})
		}
		if _, err := s.routing.GetSettings(ctx); err != nil {
			issues = append(issues, plugin.Issue{Level: "error", Message: "settings 表读取失败: " + err.Error()})
		}
		return issues
	}
	for _, name := range names {
		var v json.RawMessage
		if err := s.st.Read(name, &v); err != nil && !errors.Is(err, store.ErrNotExist) {
			issues = append(issues, plugin.Issue{Level: "error", Message: name + " 读取失败: " + err.Error()})
		}
	}
	return issues
}

// ===== 认证 =====

// handleLogin 校验用户名密码，成功后签发会话并写入 HttpOnly Cookie。
func (s *Service) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	token, err := s.auth.Login(req.Username, req.Password)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode, // Lax 允许顶级导航携带 Cookie，Desktop 开发模式需要
	})
	// 来源是桌面端 WebView 才记录「桌面已登录」标记（托盘据此决定是否签发免登录 token）。
	if isDesktopOrigin(r) {
		s.markDesktopSession(req.Username)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// desktopSession 桌面登录标记内容。
type desktopSession struct {
	Username string `json:"username"`
	LoginAt  string `json:"login_at"`
}

// isDesktopOrigin 判断请求是否来自桌面端 WebView（wails.localhost 或 vite dev 9245），
// 而非浏览器（127.0.0.1:3000）。用于区分「软件中登录」与「网页登录」。
func isDesktopOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Referer()
	}
	return strings.Contains(origin, "wails.localhost") || strings.Contains(origin, ":9245")
}

// markDesktopSession 记录桌面 WebView 登录（登录成功且来源为桌面端时调用）。
func (s *Service) markDesktopSession(username string) {
	data, err := json.Marshal(desktopSession{Username: username, LoginAt: time.Now().Format(time.RFC3339)})
	if err != nil {
		return
	}
	path := filepath.Join(config.DataDir, config.DesktopSessionFile)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		s.lg.Warn("adminapi: 记录桌面会话标记失败", "err", err)
	}
}

// clearDesktopSession 删除桌面登录标记（登出时调用，幂等）。
func (s *Service) clearDesktopSession() {
	path := filepath.Join(config.DataDir, config.DesktopSessionFile)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.lg.Warn("adminapi: 清除桌面会话标记失败", "err", err)
	}
}

// handleSSOLogin 用短时效 JWT 换取完整会话 Cookie（桌面端托盘「打开网页」免登录用）。
// 桌面端签发的 token 仅 30 秒有效、且只作为「入场券」：校验通过后按 token 里的
// 用户名重新签发正常 TTL 的会话，避免换到的 cookie 跟着 30 秒一起过期。
// 仅允许本机回环来源调用，防止局域网内其他人拿 token 换会话。
func (s *Service) handleSSOLogin(w http.ResponseWriter, r *http.Request) {
	if !isLoopback(r.RemoteAddr) {
		writeError(w, http.StatusForbidden, "仅允许本机调用")
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if !decodeJSON(w, r, &req) || req.Token == "" {
		writeError(w, http.StatusBadRequest, "缺少 token")
		return
	}
	claims, err := auth.ParseToken(s.st.SecretKey(), req.Token)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "登录凭证无效或已过期")
		return
	}
	token, err := auth.SignToken(s.st.SecretKey(), claims.Username, time.Duration(config.SessionTTLHours)*time.Hour)
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode, // 与 handleLogin 保持一致
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// isLoopback 判断请求来源是否为回环地址（127.0.0.1 / ::1 / localhost）。
func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// handleLogout 清除会话 Cookie，完成登出（幂等：无会话也返回成功）。
// 桌面登录标记（desktop-session.json）只在「桌面 WebView 登出」时删除，
// 浏览器登出不影响桌面登录态——保证托盘「打开网页」只要桌面 webview 还登录
// 就能继续免登录。
func (s *Service) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
		SameSite: http.SameSiteLaxMode,
	})
	if isDesktopOrigin(r) {
		s.clearDesktopSession()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleOverview 返回后台概览：应用名、版本、自检项数、渠道数与当前预设。
func (s *Service) handleOverview(w http.ResponseWriter, r *http.Request) {
	channels := 0
	if s.routing != nil {
		items, err := s.listDBChannels(r.Context())
		if err != nil {
			s.writeServerError(w, err)
			return
		}
		channels = len(items)
	} else {
		items, err := readSlice[types.Channel](s.st, types.FileChannels)
		if err != nil {
			s.writeServerError(w, err)
			return
		}
		channels = len(items)
	}
	var settings types.Settings
	settings, err := s.readSettings(r.Context())
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"app":                   config.AppName,
		"version":               config.Version,
		"plugins":               s.pluginCount,
		"channels":              channels,
		"active_preset":         settings.ActivePreset,
		"active_preset_target":  settings.ActivePresetTarget,
		"active_preset_targets": settings.ActivePresetTargets,
	})
}

// handlePlugins 返回全部插件的自检结果（按插件分组，每次调用实时重跑自检）。
func (s *Service) handlePlugins(w http.ResponseWriter, r *http.Request) {
	checks := []plugin.PluginCheck{}
	if s.checksProvider != nil {
		checks = s.checksProvider()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"plugins": checks,
		"count":   s.pluginCount,
	})
}

// ===== 渠道 =====

// handleChannelsList 返回全部渠道。
func (s *Service) handleChannelsList(w http.ResponseWriter, r *http.Request) {
	if s.routing != nil {
		s.handleChannelsListDB(w, r)
		return
	}
	items, err := readSlice[types.Channel](s.st, types.FileChannels)
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// fetchChannelModels 请求渠道的 /v1/models，返回模型 id 列表；失败返回 error。
// 兼容 OpenAI 标准 {"data":[{"id":"..."}]} 与字符串数组 {"data":["..."]}。
func fetchChannelModels(ctx context.Context, baseURL, apiKey string, timeout time.Duration) ([]string, error) {
	url := strings.TrimRight(baseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("models 接口返回 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	var ids []string
	for _, raw := range parsed.Data {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			ids = append(ids, s)
			continue
		}
		var obj struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &obj); err == nil && obj.ID != "" {
			ids = append(ids, obj.ID)
		}
	}
	return ids, nil
}

// probeChannelModels 探测渠道 /v1/models，返回模型列表与错误文本（错误文本空 = 成功）。
func probeChannelModels(baseURL, apiKey string) (models []string, modelsErr string) {
	m, err := fetchChannelModels(context.Background(), baseURL, apiKey, 8*time.Second)
	if err != nil {
		return nil, err.Error()
	}
	return m, ""
}

// handleChannelCreate 新建渠道：明文 api_key 用 store.Encrypt 落盘为 api_key_cipher。
func (s *Service) handleChannelCreate(w http.ResponseWriter, r *http.Request) {
	if s.routing != nil {
		s.handleChannelCreateDB(w, r)
		return
	}
	var req struct {
		Name        string `json:"name"`
		ChannelName string `json:"channel_name"`
		BaseURL     string `json:"base_url"`
		APIKey      string `json:"api_key"`
		Enabled     bool   `json:"enabled"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.BaseURL) == "" {
		writeError(w, http.StatusBadRequest, "名称和地址必填")
		return
	}
	cipher, err := s.st.Encrypt(req.APIKey)
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	id, err := newID()
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	items, err := readSlice[types.Channel](s.st, types.FileChannels)
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	// 渠道名称：未显式提供时，同 Base URL 已有渠道则继承其渠道名（添加 Key），否则回退 Key 名。
	channelName := req.ChannelName
	if channelName == "" {
		for i := range items {
			if strings.TrimRight(items[i].BaseURL, "/") == strings.TrimRight(req.BaseURL, "/") {
				channelName = items[i].ChannelName
				break
			}
		}
		if channelName == "" {
			channelName = req.Name
		}
	}
	ch := types.Channel{
		ID:           id,
		Name:         req.Name,
		ChannelName:  channelName,
		BaseURL:      req.BaseURL,
		APIKeyCipher: cipher,
		Enabled:      req.Enabled,
	}
	// 探测 /v1/models（失败不阻塞创建，留空 = 未知兜底）。
	ch.Models, ch.ModelsError = probeChannelModels(req.BaseURL, req.APIKey)
	items = append(items, ch)
	if err := s.st.Write(types.FileChannels, items); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ch)
}

// handleChannelUpdate 按 id 更新渠道；api_key 非空时重新加密，否则保留原密文。
func (s *Service) handleChannelUpdate(w http.ResponseWriter, r *http.Request) {
	if s.routing != nil {
		s.handleChannelUpdateDB(w, r)
		return
	}
	id := r.PathValue("id")
	var req struct {
		Name        string `json:"name"`
		ChannelName string `json:"channel_name"`
		BaseURL     string `json:"base_url"`
		APIKey      string `json:"api_key"`
		Enabled     bool   `json:"enabled"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	items, err := readSlice[types.Channel](s.st, types.FileChannels)
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	found := false
	changedName := ""
	for i := range items {
		if items[i].ID != id {
			continue
		}
		found = true
		oldBase := strings.TrimRight(items[i].BaseURL, "/")
		items[i].Name = req.Name
		items[i].BaseURL = req.BaseURL
		items[i].Enabled = req.Enabled
		newBase := strings.TrimRight(items[i].BaseURL, "/")
		if oldBase != newBase {
			// 挪到别的组：渠道名跟随新组（忽略前端回传的旧组名），避免破坏"同组一致"。
			items[i].ChannelName = ""
			for j := range items {
				if j != i && strings.TrimRight(items[j].BaseURL, "/") == newBase && items[j].ChannelName != "" {
					items[i].ChannelName = items[j].ChannelName
					break
				}
			}
			if items[i].ChannelName == "" {
				items[i].ChannelName = req.Name
			}
		} else if req.ChannelName != "" && req.ChannelName != items[i].ChannelName {
			// 渠道名称变化：同 Base URL 组全部渠道同步（含自身）。
			changedName = req.ChannelName
			items[i].ChannelName = req.ChannelName
		}
		plainKey := req.APIKey
		if req.APIKey != "" {
			cipher, err := s.st.Encrypt(req.APIKey)
			if err != nil {
				s.writeServerError(w, err)
				return
			}
			items[i].APIKeyCipher = cipher
		} else if items[i].APIKeyCipher != "" {
			if k, err := s.st.Decrypt(items[i].APIKeyCipher); err == nil {
				plainKey = k
			}
		}
		// 重新探测 /v1/models。
		items[i].Models, items[i].ModelsError = probeChannelModels(req.BaseURL, plainKey)
		break
	}
	if !found {
		writeError(w, http.StatusNotFound, "渠道不存在")
		return
	}
	if changedName != "" {
		target := strings.TrimRight(req.BaseURL, "/")
		for i := range items {
			if items[i].ChannelName != changedName && strings.TrimRight(items[i].BaseURL, "/") == target {
				items[i].ChannelName = changedName
			}
		}
	}
	if err := s.st.Write(types.FileChannels, items); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleChannelDelete 按 id 删除渠道。
func (s *Service) handleChannelDelete(w http.ResponseWriter, r *http.Request) {
	if s.routing != nil {
		s.handleChannelDeleteDB(w, r)
		return
	}
	id := r.PathValue("id")
	items, err := readSlice[types.Channel](s.st, types.FileChannels)
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	out := items[:0]
	found := false
	for _, it := range items {
		if it.ID == id {
			found = true
			continue
		}
		out = append(out, it)
	}
	if !found {
		writeError(w, http.StatusNotFound, "渠道不存在")
		return
	}
	if err := s.st.Write(types.FileChannels, out); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleChannelRefreshModels 手动重新探测渠道 /v1/models，返回最新模型列表与错误文本。
func (s *Service) handleChannelRefreshModels(w http.ResponseWriter, r *http.Request) {
	if s.routing != nil {
		s.handleChannelRefreshModelsDB(w, r)
		return
	}
	id := r.PathValue("id")
	items, err := readSlice[types.Channel](s.st, types.FileChannels)
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	for i := range items {
		if items[i].ID != id {
			continue
		}
		plainKey := ""
		if items[i].APIKeyCipher != "" {
			if k, err := s.st.Decrypt(items[i].APIKeyCipher); err == nil {
				plainKey = k
			}
		}
		items[i].Models, items[i].ModelsError = probeChannelModels(items[i].BaseURL, plainKey)
		if err := s.st.Write(types.FileChannels, items); err != nil {
			s.writeServerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"models":       items[i].Models,
			"models_error": items[i].ModelsError,
		})
		return
	}
	writeError(w, http.StatusNotFound, "渠道不存在")
}

// handleChannelProbe 按表单当前值直接探测上游 /v1/models（不落库、不改渠道），
// 供"添加渠道/编辑渠道"弹窗内的"获取模型"按钮使用。
// 新建时传 base_url + api_key；编辑时 API Key 不回显，改传 id ——
// 后台按 id 查出已存的 API Key 参与探测（api_key 非空时优先用表单值）。
func (s *Service) handleChannelProbe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID      string `json:"id"`
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.BaseURL) == "" {
		writeError(w, http.StatusBadRequest, "Base URL 必填")
		return
	}
	key := req.APIKey
	if key == "" && req.ID != "" {
		// 编辑模式：表单没回显 Key，按 id 取已存 Key。
		// 渠道存在但没有 Key（建渠道时未填）→ 空 key 探测，由上游决定成败。
		var cipher string
		found := false
		if s.routing != nil {
			channels, err := s.routing.ListChannels(r.Context())
			if err != nil {
				s.writeServerError(w, err)
				return
			}
			for _, ch := range channels {
				if ch.ID == req.ID {
					cipher = ch.APIKeyCipher
					found = true
					break
				}
			}
		} else {
			items, err := readSlice[types.Channel](s.st, types.FileChannels)
			if err != nil {
				s.writeServerError(w, err)
				return
			}
			for i := range items {
				if items[i].ID == req.ID {
					cipher = items[i].APIKeyCipher
					found = true
					break
				}
			}
		}
		if !found {
			writeError(w, http.StatusNotFound, "渠道不存在")
			return
		}
		if cipher != "" {
			plain, err := s.st.Decrypt(cipher)
			if err != nil {
				s.writeServerError(w, fmt.Errorf("解密渠道 Key 失败: %w", err))
				return
			}
			key = plain
		}
	}
	models, modelsError := probeChannelModels(req.BaseURL, key)
	writeJSON(w, http.StatusOK, map[string]any{
		"models":       models,
		"models_error": modelsError,
	})
}

// handleChannelModelsReplace 全量编辑渠道模型清单（添加/删除/禁用/启用一接口）。
func (s *Service) handleChannelModelsReplace(w http.ResponseWriter, r *http.Request) {
	if s.routing != nil {
		s.handleChannelModelsReplaceDB(w, r)
		return
	}
	// JSON 兜底：渠道模型是纯字符串列表，编辑 = 全量替换启用的模型名。
	var input []channelModelInput
	if !decodeJSON(w, r, &input) {
		return
	}
	id := r.PathValue("id")
	items, err := readSlice[types.Channel](s.st, types.FileChannels)
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	for i := range items {
		if items[i].ID != id {
			continue
		}
		models := make([]string, 0, len(input))
		for _, in := range input {
			if in.Model != "" {
				models = append(models, in.Model)
			}
		}
		items[i].Models = models
		if err := s.st.Write(types.FileChannels, items); err != nil {
			s.writeServerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "models": models})
		return
	}
	writeError(w, http.StatusNotFound, "渠道不存在")
}

// handleChannelMove 上移/下移渠道，调整路由优先级（channels.json 数组顺序即优先级）。
func (s *Service) handleChannelMove(w http.ResponseWriter, r *http.Request) {
	if s.routing != nil {
		s.handleChannelMoveDB(w, r)
		return
	}
	id := r.PathValue("id")
	var req struct {
		Direction string `json:"direction"` // "up" | "down"
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	items, err := readSlice[types.Channel](s.st, types.FileChannels)
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	idx := -1
	for i := range items {
		if items[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		writeError(w, http.StatusNotFound, "渠道不存在")
		return
	}
	target := -1
	if req.Direction == "up" && idx > 0 {
		target = idx - 1
	} else if req.Direction == "down" && idx < len(items)-1 {
		target = idx + 1
	}
	if target < 0 {
		writeError(w, http.StatusBadRequest, "无法移动：已在边界或方向非法")
		return
	}
	items[idx], items[target] = items[target], items[idx]
	if err := s.st.Write(types.FileChannels, items); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// handleChannelsReorder 全量重排渠道顺序（DB/JSON 双分支）：按提交的 id 数组顺序
// 设置 position。前端按 base_url 分组后整组移动，提交全量顺序即可。
func (s *Service) handleChannelsReorder(w http.ResponseWriter, r *http.Request) {
	if s.routing != nil {
		s.handleChannelsReorderDB(w, r)
		return
	}
	var req struct {
		IDs []string `json:"ids"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	items, err := readSlice[types.Channel](s.st, types.FileChannels)
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	byID := make(map[string]int, len(items))
	for i := range items {
		byID[items[i].ID] = i
	}
	out := make([]types.Channel, 0, len(items))
	seen := make(map[string]bool, len(req.IDs))
	for _, id := range req.IDs {
		idx, ok := byID[id]
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, items[idx])
	}
	for i := range items {
		if !seen[items[i].ID] {
			out = append(out, items[i])
		}
	}
	if err := s.st.Write(types.FileChannels, out); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleChannelTest 测试渠道/模型连通性：向渠道发一个最小 chat 请求（可选视觉），返回结果。
func (s *Service) handleChannelTest(w http.ResponseWriter, r *http.Request) {
	if s.routing != nil {
		s.handleChannelTestDB(w, r)
		return
	}
	var req struct {
		ID     string `json:"id"`     // 渠道 id（必填）
		Model  string `json:"model"`  // 模型名；空 = gpt-4o
		Vision bool   `json:"vision"` // 是否带图测试视觉能力
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "渠道 id 必填")
		return
	}

	items, err := readSlice[types.Channel](s.st, types.FileChannels)
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	var found *types.Channel
	for i := range items {
		if items[i].ID == req.ID {
			found = &items[i]
			break
		}
	}
	if found == nil {
		writeError(w, http.StatusNotFound, "渠道不存在")
		return
	}
	baseURL := found.BaseURL
	apiKey := ""
	if found.APIKeyCipher != "" {
		k, err := s.st.Decrypt(found.APIKeyCipher)
		if err != nil {
			s.writeServerError(w, err)
			return
		}
		apiKey = k
	}

	model := req.Model
	if model == "" {
		model = "gpt-4o"
	}

	// 构造消息：视觉测试带一张 1x1 图片，否则纯文本 ping。
	content := []map[string]any{}
	if req.Vision {
		content = append(content, map[string]any{
			"type":      "image_url",
			"image_url": map[string]any{"url": "data:image/png;base64," + testPNG},
		})
		content = append(content, map[string]any{"type": "text", "text": "这张图片里有什么？用一句话回答。"})
	} else {
		content = append(content, map[string]any{"type": "text", "text": "ping"})
	}

	payload, _ := json.Marshal(map[string]any{
		"model":    model,
		"messages": []map[string]any{{"role": "user", "content": content}},
		"stream":   false,
	})

	httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}

	start := time.Now()
	resp, err := (&http.Client{Timeout: config.VisionTimeout}).Do(httpReq)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error(), "latency_ms": latency})
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "status": resp.StatusCode, "latency_ms": latency, "body": string(respBody),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "status": resp.StatusCode, "latency_ms": latency, "reply": extractReply(respBody),
	})
}

// extractReply 从 OpenAI chat completion JSON 里提取 choices[0].message.content；失败返回原始 body。
func extractReply(body []byte) string {
	var v struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &v); err != nil || len(v.Choices) == 0 {
		return string(body)
	}
	return v.Choices[0].Message.Content
}

// testPNG 1x1 红色 PNG（视觉连通性测试用）。
const testPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

// ===== 能力路由 =====

// handleCapabilityRoutesList 返回全部能力路由。
func (s *Service) handleCapabilityRoutesList(w http.ResponseWriter, r *http.Request) {
	items, err := s.readCapabilityRoutes(r.Context())
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// handleCapabilityRouteCreate 追加一条能力路由。
func (s *Service) handleCapabilityRouteCreate(w http.ResponseWriter, r *http.Request) {
	var req types.CapabilityRoute
	if !decodeJSON(w, r, &req) {
		return
	}
	items, err := s.readCapabilityRoutes(r.Context())
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	items = append(items, req)
	if err := s.writeCapabilityRoutes(r.Context(), items); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, req)
}

// handleCapabilityRoutesReplace 用请求体数组整体替换能力路由表。
func (s *Service) handleCapabilityRoutesReplace(w http.ResponseWriter, r *http.Request) {
	var req []types.CapabilityRoute
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.writeCapabilityRoutes(r.Context(), req); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleCapabilityRouteDelete 按 capability + models + channel_ids（数组精确匹配）定位并删除一条能力路由。
func (s *Service) handleCapabilityRouteDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Models     []string `json:"models"`
		ChannelIDs []string `json:"channel_ids"`
		Capability string   `json:"capability"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	items, err := s.readCapabilityRoutes(r.Context())
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	out := items[:0]
	found := false
	for _, it := range items {
		if it.Capability == req.Capability &&
			equalStrings(it.Models, req.Models) &&
			equalStrings(it.ChannelIDs, req.ChannelIDs) {
			found = true
			continue
		}
		out = append(out, it)
	}
	if !found {
		writeError(w, http.StatusNotFound, "能力路由不存在")
		return
	}
	if err := s.writeCapabilityRoutes(r.Context(), out); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// equalStrings 判断两个字符串切片是否相等（长度与顺序一致）。
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ===== MCP 服务器 =====

// handleMCPServersList 返回全部上游 MCP 服务器。
// mcpServerWithStatus 管理后台返回的 MCP 服务器条目（嵌入配置 + 进程运行状态）。
type mcpServerWithStatus struct {
	types.MCPServer
	// Status 进程运行状态：running / stopped / failed（见 mcphub.ServerRunState）。
	Status string `json:"status"`
	// Error 失败原因（仅 status=failed 时非空）。
	Error string `json:"error,omitempty"`
}

// withStatus 合并进程状态到服务器条目（hub 为 nil 时仅返回配置，Status 置空）。
func (s *Service) withStatus(it types.MCPServer) mcpServerWithStatus {
	out := mcpServerWithStatus{MCPServer: it}
	if s.hub == nil {
		return out
	}
	st, err := s.hub.ServerStatus(it.ID)
	if err != nil {
		out.Status = string(mcphub.StateFailed)
		out.Error = err.Error()
		return out
	}
	out.Status = string(st)
	if st == mcphub.StateFailed {
		out.Error = s.hub.ServerError(it.ID)
	}
	return out
}

func (s *Service) handleMCPServersList(w http.ResponseWriter, r *http.Request) {
	items, err := s.readMCPServers(r.Context())
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	out := make([]mcpServerWithStatus, 0, len(items))
	for _, it := range items {
		out = append(out, s.withStatus(it))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleMCPServerCreate 新建一个上游 MCP 服务器（自动生成 id）。
func (s *Service) handleMCPServerCreate(w http.ResponseWriter, r *http.Request) {
	var req types.MCPServer
	if !decodeJSON(w, r, &req) {
		return
	}
	id, err := newID()
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	req.ID = id
	items, err := s.readMCPServers(r.Context())
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	items = append(items, req)
	if err := s.writeMCPServers(r.Context(), items); err != nil {
		s.writeServerError(w, err)
		return
	}
	s.invalidateHub()
	// enabled 时立即拉起 stdio 进程；失败不阻断创建，状态经 withStatus 展示为 failed。
	if req.Enabled {
		s.ensureServerRunning(r.Context(), req)
	}
	writeJSON(w, http.StatusOK, s.withStatus(req))
}

// ensureServerRunning 拉起指定 server 的进程（hub 为 nil 时安全跳过），失败仅记日志。
func (s *Service) ensureServerRunning(ctx context.Context, srv types.MCPServer) {
	if s.hub == nil {
		return
	}
	if err := s.hub.SetServerEnabled(ctx, srv.ID, true); err != nil {
		s.lg.Warn("admin-api: 拉起 MCP 进程失败", "server", srv.Name, "err", err)
	}
}

// handleMCPServersReplace 用请求体数组整体替换 MCP 服务器列表。
// 进程生命周期随替换 diff：消失/改为禁用的先停，新增/重新启用的拉起。
func (s *Service) handleMCPServersReplace(w http.ResponseWriter, r *http.Request) {
	var req []types.MCPServer
	if !decodeJSON(w, r, &req) {
		return
	}
	// 先停掉旧配置中「已消失」或「改为禁用」的进程。
	old, err := s.readMCPServers(r.Context())
	if err == nil {
		kept := map[string]bool{}
		for _, it := range req {
			if it.Enabled {
				kept[it.ID] = true
			}
		}
		for _, it := range old {
			if it.Enabled && !kept[it.ID] && s.hub != nil {
				if err := s.hub.SetServerEnabled(r.Context(), it.ID, false); err != nil {
					s.lg.Warn("admin-api: 替换后停止 MCP 进程失败", "server", it.Name, "err", err)
				}
			}
		}
	}
	if err := s.writeMCPServers(r.Context(), req); err != nil {
		s.writeServerError(w, err)
		return
	}
	s.invalidateHub()
	// 再拉起新增/重新启用的进程。
	for _, it := range req {
		if it.Enabled {
			s.ensureServerRunning(r.Context(), it)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleMCPServerDelete 按请求体里的 id 删除一个 MCP 服务器。
func (s *Service) handleMCPServerDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	items, err := s.readMCPServers(r.Context())
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	out := items[:0]
	found := false
	for _, it := range items {
		if it.ID == req.ID {
			found = true
			continue
		}
		out = append(out, it)
	}
	if !found {
		writeError(w, http.StatusNotFound, "MCP 服务器不存在")
		return
	}
	// 先杀进程再删配置，避免留下孤儿 stdio 子进程。
	if s.hub != nil {
		if err := s.hub.SetServerEnabled(r.Context(), req.ID, false); err != nil {
			s.lg.Warn("admin-api: 删除前停止 MCP 进程失败", "id", req.ID, "err", err)
		}
	}
	if err := s.writeMCPServers(r.Context(), out); err != nil {
		s.writeServerError(w, err)
		return
	}
	s.invalidateHub()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleMCPServerUpdate 按 id 更新单个 MCP 服务器（编辑）。
func (s *Service) handleMCPServerUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req types.MCPServer
	if !decodeJSON(w, r, &req) {
		return
	}
	items, err := s.readMCPServers(r.Context())
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	found := false
	for i := range items {
		if items[i].ID != id {
			continue
		}
		found = true
		req.ID = id // 保留原 id，忽略请求体里的 id
		items[i] = req
		break
	}
	if !found {
		writeError(w, http.StatusNotFound, "MCP 服务器不存在")
		return
	}
	if err := s.writeMCPServers(r.Context(), items); err != nil {
		s.writeServerError(w, err)
		return
	}
	s.invalidateHub()
	// 编辑可能改了命令/URL/开关：先停旧进程，再按新 enabled 拉起，保证新配置生效。
	// 启动失败不回滚 DB（保留用户意图），状态经 withStatus 展示为 failed。
	if s.hub != nil {
		_ = s.hub.SetServerEnabled(r.Context(), id, false)
		if req.Enabled {
			if err := s.hub.SetServerEnabled(r.Context(), id, true); err != nil {
				s.lg.Warn("admin-api: 更新后拉起 MCP 进程失败", "server", req.Name, "err", err)
			}
		}
	}
	out := make([]mcpServerWithStatus, 0, len(items))
	for _, it := range items {
		out = append(out, s.withStatus(it))
	}
	writeJSON(w, http.StatusOK, out)
}

// mcpToolBrief 连接测试/工具列表返回的工具摘要（不含完整 schema，避免响应过大）。
type mcpToolBrief struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// mcpConnectTimeout 连接测试与工具枚举的超时上限（远小于 UpstreamTimeout）。
const mcpConnectTimeout = 20 * time.Second

// handleMCPServerTest 按请求体里的完整配置测试上游连通性并列出工具（无需先保存）。
func (s *Service) handleMCPServerTest(w http.ResponseWriter, r *http.Request) {
	var req types.MCPServer
	if !decodeJSON(w, r, &req) {
		return
	}
	tools, err := s.listUpstreamTools(r, req)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tools": tools})
}

// handleMCPToolsList 聚合列出所有 enabled 上游 MCP 的工具（供分组挑选）。逐个连接，单个失败不中断整体。
func (s *Service) handleMCPToolsList(w http.ResponseWriter, r *http.Request) {
	servers, err := s.readMCPServers(r.Context())
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	type serverTools struct {
		ID        string         `json:"id"`
		Name      string         `json:"name"`
		Transport string         `json:"transport"`
		URL       string         `json:"url"`
		Error     string         `json:"error,omitempty"`
		Tools     []mcpToolBrief `json:"tools"`
	}
	out := make([]serverTools, 0, len(servers))
	for _, srv := range servers {
		if !srv.Enabled {
			continue
		}
		entry := serverTools{ID: srv.ID, Name: srv.Name, Transport: srv.Transport, URL: srv.URL}
		tools, err := s.listUpstreamTools(r, srv)
		if err != nil {
			entry.Error = err.Error()
			entry.Tools = []mcpToolBrief{}
		} else {
			entry.Tools = tools
		}
		out = append(out, entry)
	}
	writeJSON(w, http.StatusOK, out)
}

// listUpstreamTools 用给定配置建连并枚举工具；超时或失败返回错误。
func (s *Service) listUpstreamTools(r *http.Request, srv types.MCPServer) ([]mcpToolBrief, error) {
	up := mcpkit.NewUpstream(mcpkit.UpstreamConfig{
		Name:      srv.Name,
		Transport: srv.Transport,
		Command:   srv.Command,
		Args:      srv.Args,
		Env:       srv.Env,
		URL:       srv.URL,
		Headers:   srv.Headers,
	})
	defer up.Close()

	ctx, cancel := context.WithTimeout(r.Context(), mcpConnectTimeout)
	defer cancel()

	infos, err := up.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]mcpToolBrief, 0, len(infos))
	for _, info := range infos {
		out = append(out, mcpToolBrief{Name: info.Name, Description: info.Description})
	}
	return out, nil
}

func (s *Service) findMCPServer(id string) (types.MCPServer, error) {
	servers, err := s.readMCPServers(context.Background())
	if err != nil {
		return types.MCPServer{}, err
	}
	for _, server := range servers {
		if server.ID == id {
			return server, nil
		}
	}
	return types.MCPServer{}, fmt.Errorf("MCP 服务器不存在")
}

func newMCPUpstream(server types.MCPServer) *mcpkit.Upstream {
	return mcpkit.NewUpstream(mcpkit.UpstreamConfig{
		Name: server.Name, Transport: server.Transport, Command: server.Command, Args: server.Args,
		Env: server.Env, URL: server.URL, Headers: server.Headers,
	})
}

func (s *Service) handleMCPToolSchema(w http.ResponseWriter, r *http.Request) {
	serverID := strings.TrimSpace(r.URL.Query().Get("server_id"))
	toolName := strings.TrimSpace(r.URL.Query().Get("tool_name"))
	if serverID == "" || toolName == "" {
		writeError(w, http.StatusBadRequest, "缺少 server_id 或 tool_name")
		return
	}
	server, err := s.findMCPServer(serverID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	up := newMCPUpstream(server)
	defer up.Close()
	ctx, cancel := context.WithTimeout(r.Context(), mcpConnectTimeout)
	defer cancel()
	tools, err := up.ListTools(ctx)
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	for _, tool := range tools {
		if tool.Name == toolName {
			writeJSON(w, http.StatusOK, map[string]any{"name": tool.Name, "description": tool.Description, "inputSchema": tool.InputSchema})
			return
		}
	}
	writeError(w, http.StatusNotFound, "工具不存在")
}

func (s *Service) handleMCPToolCall(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServerID  string         `json:"server_id"`
		ToolName  string         `json:"tool_name"`
		Arguments map[string]any `json:"arguments"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.ServerID) == "" || strings.TrimSpace(req.ToolName) == "" {
		writeError(w, http.StatusBadRequest, "缺少 server_id 或 tool_name")
		return
	}
	server, err := s.findMCPServer(req.ServerID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	up := newMCPUpstream(server)
	defer up.Close()
	ctx, cancel := context.WithTimeout(r.Context(), mcpConnectTimeout)
	defer cancel()
	result, err := up.CallTool(ctx, req.ToolName, req.Arguments)
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// ===== 工具状态 =====

// handleToolsStateGet 返回全部单工具开关与分类。
func (s *Service) handleToolsStateGet(w http.ResponseWriter, r *http.Request) {
	items, err := s.readToolStates(r.Context())
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// handleToolsStatePut 用请求体数组整体替换工具状态表。
func (s *Service) handleToolsStatePut(w http.ResponseWriter, r *http.Request) {
	var req []types.ToolState
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.writeToolStates(r.Context(), req); err != nil {
		s.writeServerError(w, err)
		return
	}
	s.invalidateHub()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ===== 分组 =====

// handleGroupsList 返回全部分组。
func (s *Service) handleGroupsList(w http.ResponseWriter, r *http.Request) {
	items, err := s.readGroups(r.Context())
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// handleGroupCreate 追加一个分组。
func (s *Service) handleGroupCreate(w http.ResponseWriter, r *http.Request) {
	var req types.Group
	if !decodeJSON(w, r, &req) {
		return
	}
	items, err := s.readGroups(r.Context())
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	items = append(items, req)
	if err := s.writeGroups(r.Context(), items); err != nil {
		s.writeServerError(w, err)
		return
	}
	s.invalidateHub()
	writeJSON(w, http.StatusOK, req)
}

// handleGroupsReplace 用请求体数组整体替换分组列表。
func (s *Service) handleGroupsReplace(w http.ResponseWriter, r *http.Request) {
	var req []types.Group
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.writeGroups(r.Context(), req); err != nil {
		s.writeServerError(w, err)
		return
	}
	s.invalidateHub()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleGroupDelete 按请求体里的 name 删除一个分组。
func (s *Service) handleGroupDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	items, err := s.readGroups(r.Context())
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	out := items[:0]
	found := false
	for _, it := range items {
		if it.Name == req.Name {
			found = true
			continue
		}
		out = append(out, it)
	}
	if !found {
		writeError(w, http.StatusNotFound, "分组不存在")
		return
	}
	if err := s.writeGroups(r.Context(), out); err != nil {
		s.writeServerError(w, err)
		return
	}
	s.invalidateHub()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ===== 密钥 =====

// handleKeysList 聚合返回 sk- key 列表与 MCP endpoint key 状态。
func (s *Service) handleKeysList(w http.ResponseWriter, r *http.Request) {
	skKeys, err := s.keys.ListAPIKeys()
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	mcpKeys, err := s.readMCPKeys(r.Context())
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	// 空列表统一输出 []，避免 nil slice 编码为 JSON null 导致前端 .length 崩溃。
	if skKeys == nil {
		skKeys = []types.APIKey{}
	}
	if mcpKeys == nil {
		mcpKeys = []types.MCPKey{}
	}
	// Cipher 是服务端测试用的加密明文，绝不回传前端。
	for i := range skKeys {
		skKeys[i].Cipher = ""
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sk_keys":  skKeys,
		"mcp_keys": mcpKeys,
	})
}

// handleCreateSKKey 创建 sk- key，完整 key 仅此一次随响应返回。
func (s *Service) handleCreateSKKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string   `json:"name"`
		Models []string `json:"models"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	full, prefix, err := s.keys.CreateAPIKey(req.Name, req.Models)
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"key":    full,
		"prefix": prefix,
	})
}

// handleDeleteSKKey 按 id 删除一把 sk- key。
func (s *Service) handleDeleteSKKey(w http.ResponseWriter, r *http.Request) {
	if err := s.keys.DeleteAPIKey(r.PathValue("id")); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleCreateMCPKey 为某端点签发/重置 MCP key，完整 key 随响应返回。
func (s *Service) handleCreateMCPKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Endpoint string `json:"endpoint"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	full, err := s.keys.SetMCPKey(req.Endpoint)
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": full})
}

// handleDeleteMCPKey 关闭某端点的 MCP key 认证。
func (s *Service) handleDeleteMCPKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Endpoint string `json:"endpoint"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.keys.DisableMCPKey(req.Endpoint); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ===== 技能与预设 =====

// handleSkillsList 返回技能仓库清单。
func (s *Service) handleSkillsList(w http.ResponseWriter, r *http.Request) {
	items, err := s.skill.List()
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// handleSkillRegister 登记（或更新）一个技能。
func (s *Service) handleSkillRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Source  string `json:"source"`
		Version string `json:"version"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.skill.Register(req.Name, req.Source, req.Version); err != nil {
		s.writeServerError(w, err)
		return
	}
	s.invalidateHub()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSkillDelete 按名字从技能仓库移除一个技能。
func (s *Service) handleSkillDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.skill.Remove(r.PathValue("name")); err != nil {
		s.writeServerError(w, err)
		return
	}
	s.invalidateHub()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSkillInstall 下载安装一个技能（按 SkillInstallMode 用 git / npx 落到仓库并登记）。
func (s *Service) handleSkillInstall(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Source  string `json:"source"`
		Version string `json:"version"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.skill.Install(req.Name, req.Source, req.Version); err != nil {
		s.writeServerError(w, err)
		return
	}
	s.invalidateHub()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSkillImportZip 接收 multipart 上传的 zip 文件并导入为技能。
func (s *Service) handleSkillImportZip(w http.ResponseWriter, r *http.Request) {
	// 限制整个请求体，略大于 skills.MaxZipBytes，留出 multipart 头部与 name 字段余量。
	r.Body = http.MaxBytesReader(w, r.Body, skills.MaxZipBytes+(1<<20))

	file, _, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "缺少 file 上传字段")
		return
	}
	defer file.Close()

	name := strings.TrimSpace(r.FormValue("name"))
	if err := s.skill.ImportZip(file, name); err != nil {
		s.writeServerError(w, err)
		return
	}
	s.invalidateHub()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handlePresetsList 返回全部技能预设。
func (s *Service) handlePresetsList(w http.ResponseWriter, r *http.Request) {
	items, err := s.skill.ListPresets()
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// handlePresetCreate 创建（或同名覆盖）一个技能预设。targets 支持多平台，
// 旧客户端传单值 target 时自动兼容。
func (s *Service) handlePresetCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string   `json:"name"`
		Skills  []string `json:"skills"`
		Target  string   `json:"target"`  // 兼容旧客户端：目标平台（空=通用；codex/claudecode/opencode…）
		Targets []string `json:"targets"` // 新格式：目标平台列表（空=通用），可多选
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	targets := req.Targets
	if len(targets) == 0 && req.Target != "" {
		targets = []string{req.Target}
	}
	if err := s.skill.CreatePreset(req.Name, req.Skills, targets); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handlePresetDelete 按请求体里的 name 删除一个技能预设。
func (s *Service) handlePresetDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.skill.DeletePreset(req.Name); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handlePresetApply 切换当前预设（把技能库技能复制部署到目标目录）。
func (s *Service) handlePresetApply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.skill.ApplyPreset(req.Name); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSkillsStatus 返回通用 + 各平台的技能数量与备份状态（供 UI 渲染恢复按钮）。
func (s *Service) handleSkillsStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.skill.Status())
}

// handleSkillRestore 恢复指定目标（空=通用，或平台名）的备份：删当前 + 备份改回原名。
func (s *Service) handleSkillRestore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Target string `json:"target"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.skill.RestoreBackup(req.Target); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSkillSync 主动全量同步：把 ~/.agents/skills 所有技能同步到技能库。
func (s *Service) handleSkillSync(w http.ResponseWriter, r *http.Request) {
	synced, err := s.skill.SyncAll()
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "synced": synced})
}

// handleSkillCheckUpdates 启动更新任务（单实例），立即返回；日志走
// GET /api/skills/update-stream 的 SSE 流实时推送。返回更新列表（若已完成）。
func (s *Service) handleSkillCheckUpdates(w http.ResponseWriter, r *http.Request) {
	ch, err := s.skill.SubscribeUpdate()
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	// 非阻塞消费：如果任务已瞬间完成，拿终态；否则返回已启动。
	select {
	case ev := <-ch:
		if ev.Type == "done" {
			names := splitNonEmpty(ev.Data)
			writeJSON(w, http.StatusOK, map[string]any{"updates": names})
			return
		}
		if ev.Type == "error" {
			writeJSON(w, http.StatusOK, map[string]any{"error": ev.Data})
			return
		}
	default:
	}
	writeJSON(w, http.StatusOK, map[string]any{"started": true})
}

// evJSON 序列化更新事件为 JSON（SSE data 字段）。
func evJSON(ev skills.UpdateEvent) string {
	b, _ := json.Marshal(ev)
	return string(b)
}

// jsonString 序列化字符串为 JSON（安全转义）。
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// splitNonEmpty 按逗号分割并过滤空项。
func splitNonEmpty(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// handleSkillUpdateStream SSE：实时推送更新任务日志流（data: UpdateEvent JSON，
// 客户端用 onmessage + JSON.type 分桶）。不写 event: 行以保证 onmessage 必触发
// （部分 EventSource 实现对命名事件分发不可靠）。
func (s *Service) handleSkillUpdateStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, err := s.skill.SubscribeUpdate()
	if err != nil {
		fmt.Fprintf(w, "data: %s\n\n", jsonString(`{"type":"error","data":"`+err.Error()+`"}`))
		flusher.Flush()
		return
	}

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return // 任务结束，订阅 channel 已关闭
			}
			fmt.Fprintf(w, "data: %s\n\n", evJSON(ev))
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// handleUnifyaiPlatforms 返回 unifyai 平台能力列表（--list-platforms --json）。
func (s *Service) handleUnifyaiPlatforms(w http.ResponseWriter, r *http.Request) {
	res, err := s.unify.PlatformInfo()
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleUnifyaiRun 启动 unifyai 任务（单实例），立即返回；日志走
// GET /api/unifyai/stream 的 SSE 流实时推送。body: {"args": ["--all", "--dry-run"]}。
func (s *Service) handleUnifyaiRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Args []string `json:"args"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	s.unify.SetArgs(req.Args)
	ch, err := s.unify.Subscribe()
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	// 非阻塞消费：如果任务已瞬间完成，拿终态；否则返回已启动。
	select {
	case ev := <-ch:
		if ev.Type == "done" {
			writeJSON(w, http.StatusOK, map[string]any{"done": true})
			return
		}
		if ev.Type == "error" {
			writeJSON(w, http.StatusOK, map[string]any{"error": ev.Data})
			return
		}
	default:
	}
	writeJSON(w, http.StatusOK, map[string]any{"started": true})
}

// handleUnifyaiStream SSE：实时推送 unifyai 任务日志流（data: RunEvent JSON）。
// 支持 ?args=<json> 查询参数（args 数组 JSON 编码），SSE 连接即触发任务启动，
// 避免 POST/SSE 双调用之间的任务竞态。
func (s *Service) handleUnifyaiStream(w http.ResponseWriter, r *http.Request) {
	if raw := r.URL.Query().Get("args"); raw != "" {
		var args []string
		if err := json.Unmarshal([]byte(raw), &args); err == nil {
			s.unify.SetArgs(args)
		}
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, err := s.unify.Subscribe()
	if err != nil {
		fmt.Fprintf(w, "data: %s\n\n", jsonString(`{"type":"error","data":"`+err.Error()+`"}`))
		flusher.Flush()
		return
	}

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return // 任务结束，订阅 channel 已关闭
			}
			b, _ := json.Marshal(ev)
			fmt.Fprintf(w, "data: %s\n\n", string(b))
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// handleUnifyaiMcpServersList 返回 mcp.json 中的服务器列表（含 disabled）。
func (s *Service) handleUnifyaiMcpServersList(w http.ResponseWriter, r *http.Request) {
	servers, err := s.unify.McpServers()
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"servers": servers})
}

// handleUnifyaiMcpServersSave 全量写回 mcp.json（启用/禁用、添加、删除、导入共用）。
func (s *Service) handleUnifyaiMcpServersSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Servers []unifyai.McpServer `json:"servers"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.unify.SaveMcpServers(req.Servers); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSkillRestoreAll 对所有目标（通用 + 各平台）执行恢复，返回成功恢复的目标列表。
func (s *Service) handleSkillRestoreAll(w http.ResponseWriter, r *http.Request) {
	restored := s.skill.RestoreAllBackups()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "restored": restored})
}

// ===== 聚合模型 =====

// handleAggregatesList 返回全部聚合模型配置。
func (s *Service) handleAggregatesList(w http.ResponseWriter, r *http.Request) {
	if s.routing != nil {
		s.handleAggregatesListDB(w, r)
		return
	}
	items, err := readSlice[types.AggregateModel](s.st, types.FileAggregates)
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// handleAggregateCreate 追加一个聚合模型。
func (s *Service) handleAggregateCreate(w http.ResponseWriter, r *http.Request) {
	if s.routing != nil {
		s.handleAggregateCreateDB(w, r)
		return
	}
	var req types.AggregateModel
	if !decodeJSON(w, r, &req) {
		return
	}
	items, err := readSlice[types.AggregateModel](s.st, types.FileAggregates)
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	items = append(items, req)
	if err := s.st.Write(types.FileAggregates, items); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAggregatesReplace 整体替换聚合模型列表。
func (s *Service) handleAggregatesReplace(w http.ResponseWriter, r *http.Request) {
	if s.routing != nil {
		s.handleAggregatesReplaceDB(w, r)
		return
	}
	var req []types.AggregateModel
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.st.Write(types.FileAggregates, req); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAggregateDelete 删除指定名称的聚合模型。
func (s *Service) handleAggregateDelete(w http.ResponseWriter, r *http.Request) {
	if s.routing != nil {
		s.handleAggregateDeleteDB(w, r)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	items, err := readSlice[types.AggregateModel](s.st, types.FileAggregates)
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	filtered := items[:0]
	for _, item := range items {
		if item.Name != req.Name {
			filtered = append(filtered, item)
		}
	}
	if err := s.st.Write(types.FileAggregates, filtered); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleModelHealthList 返回聚合模型目标的实时健康状态。
func (s *Service) handleModelHealthList(w http.ResponseWriter, r *http.Request) {
	if s.health != nil {
		items, err := s.health.List(r.Context())
		if err != nil {
			s.writeServerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
		return
	}
	items, err := readSlice[types.ModelHealth](s.st, types.FileModelHealth)
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// ===== 统计 =====

// handleStatsMcp 返回 MCP 调用统计（trend + rank_aggregates + rank_tools）。
// days 为统计天数（1-365，默认 30），top 为各排行条数（1-50，默认 5）。
func (s *Service) handleStatsMcp(w http.ResponseWriter, r *http.Request) {
	if s.hub == nil {
		writeError(w, http.StatusServiceUnavailable, "mcp-hub 未装配")
		return
	}
	days, top := 30, 5
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 365 {
			days = n
		}
	}
	if v := r.URL.Query().Get("top"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 50 {
			top = n
		}
	}
	stats, err := s.hub.Stats(r.Context(), days, top)
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// ===== 设置 =====

// handleSettingsGet 返回运行时设置；文件不存在时返回零值。
func (s *Service) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	settings, err := s.readSettings(r.Context())
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

// handleSettingsPut 整体替换运行时设置。
func (s *Service) handleSettingsPut(w http.ResponseWriter, r *http.Request) {
	var req types.Settings
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.writeSettings(r.Context(), req); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleChangePassword 修改管理员密码（单账号，固定用户名 admin）。
func (s *Service) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.auth.ChangePassword("admin", req.Old, req.New); err != nil {
		writeError(w, http.StatusBadRequest, "修改密码失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ===== 辅助函数 =====

// readSlice 读取 JSON 数组文件；文件不存在视为空数组。
func readSlice[T any](st *store.Store, name string) ([]T, error) {
	var items []T
	if err := st.Read(name, &items); err != nil {
		if errors.Is(err, store.ErrNotExist) {
			return []T{}, nil
		}
		return nil, err
	}
	if items == nil {
		items = []T{}
	}
	return items, nil
}

// writeJSON 以 JSON 写出响应。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError 写出标准错误 JSON（invalid_request_error）。
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "invalid_request_error",
		},
	})
}

// writeServerError 记录错误日志并返回 500。
func (s *Service) writeServerError(w http.ResponseWriter, err error) {
	s.lg.Error("admin-api 请求处理失败", "err", err)
	writeJSON(w, http.StatusInternalServerError, map[string]any{
		"error": map[string]any{
			"message": "服务器内部错误",
			"type":    "internal_error",
		},
	})
}

// decodeJSON 解析请求体 JSON；失败时写出 400 并返回 false。
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return false
	}
	return true
}

// newID 生成 8 字节 crypto/rand 的 hex 编码作为唯一 ID。
func newID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("admin-api: 生成随机 ID 失败: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
