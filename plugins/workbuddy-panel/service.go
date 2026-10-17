package workbuddypanel

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
)

// Service WorkBuddy 面板服务。
type Service struct {
	db     *sql.DB
	st     *store.Store
	lg     *slog.Logger
	client *codebuddyClient
}

// NewService 创建面板服务。
func NewService(database *sql.DB, st *store.Store, lg *slog.Logger) *Service {
	if lg == nil {
		lg = defaultLogger
	}
	return &Service{db: database, st: st, lg: lg, client: newCodebuddyClient()}
}

// HandleProxyUpstreamSucceeded 成功事件：从网关提取好的 Usage 里读 credit 落库
// （只记 workbuddy 渠道组；插件不自行解析上游响应体，避免响应格式耦合）。
func (s *Service) HandleProxyUpstreamSucceeded(payload any) (any, error) {
	p, ok := payload.(*modelgateway.ProxySuccessPayload)
	if !ok || p == nil || p.Pipe == nil || p.ChannelID == "" {
		return payload, nil
	}
	if !isWorkbuddyChannel(s.db, p.ChannelID) {
		return payload, nil
	}
	usage := p.Usage
	if usage.Credit == 0 && usage.TotalTokens == 0 {
		return payload, nil
	}
	model := p.Model
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.Exec(`INSERT INTO wb_panel_usage(request_id, channel_id, model, credit, prompt_tokens, completion_tokens, cached_tokens, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Pipe.RequestID, p.ChannelID, model, usage.Credit, usage.PromptTokens, usage.CompletionTokens, usage.CachedTokens, now)
	if err != nil {
		s.lg.Warn("workbuddy-panel: 写消耗记录失败", "err", err)
	}
	return payload, nil
}

var wbBaseURLPattern = regexp.MustCompile(`(?i)(copilot\.tencent\.com|workbuddy\.cn|codebuddy\.(cn|ai))`)

// isWorkbuddyChannel 渠道 base_url 是否属于 CodeBuddy / WorkBuddy 域名组。
func isWorkbuddyChannel(database *sql.DB, channelID string) bool {
	var baseURL string
	if err := database.QueryRow(`SELECT COALESCE(base_url, '') FROM channels WHERE id = ?`, channelID).Scan(&baseURL); err != nil {
		return false
	}
	return wbBaseURLPattern.MatchString(baseURL)
}

// ===== 管理后台接口 =====

// HandleData GET /api/workbuddy-panel/data：缓存积分 + 本地消耗聚合。
func (s *Service) HandleData(w http.ResponseWriter, r *http.Request) {
	data, err := s.BuildData(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, data)
}

// HandleRefresh POST /api/workbuddy-panel/refresh：手动拉一次上游积分与模型。
func (s *Service) HandleRefresh(w http.ResponseWriter, r *http.Request) {
	result, err := s.RefreshAll(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// writeJSON 输出 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError 输出错误 JSON。
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"message": message}})
}

// formatSlash 积分接口要斜杠时间。
func formatSlash(t time.Time) string { return t.Format("2006/01/02 15:04:05") }

// formatDash 流水接口要连字符时间。
func formatDash(t time.Time) string { return t.Format("2006-01-02 15:04:05") }

// truncate 截断字符串。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// 按 rune 截断，避免切在 UTF-8 中间出现乱码（中文错误信息常见）。
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}
