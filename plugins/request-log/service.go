// Package requestlog 实现 Loadout 的完整请求日志插件（plugins/request-log）。
//
// 把每次 AI 模型请求的完整输入输出（请求体、响应体、流式逐块拼接）落库到
// 独立 SQLite 文件 request-log.db（单表 request_logs，主键 UUID），提供
// GET /api/request-logs 列表搜索与 GET /api/request-logs/{id} 详情。
//
// 订阅 model-gateway 的四个 waterfall 事件：
//   - proxy:before-attempt：请求发出之前抓完整请求 + 生成 UUID（用户拍板）；
//   - proxy:after-upstream：非流式 2xx 响应收尾；
//   - proxy:upstream-failed：失败收尾（4xx/5xx/无渠道，仅 2xx 才走 after）；
//   - proxy:stream-chunk：流式逐块拼接，[DONE] 收尾。
//
// 与 route-log 的关联（方式 A）：route_requests 表加列 request_log_id（UUID），
// 本插件在 before-attempt 生成后 UPDATE 该列，route-log 列表/详情带出，
// 前端点击跳转 /api/request-logs/{id}。能力开关挂 capability_routes
// （capability="request_log"，models × channels 矩阵，参照 sensitive-filter）。
package requestlog

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"loadout/core/config"
	"loadout/core/db"
	"loadout/core/plugin"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// capabilityName 能力路由表中本能力的固定名称。
const capabilityName = "request_log"

// metadataKey UUID 在 pipe.Metadata 中的键——单一来源是 model-gateway 的
// MetadataRequestLogID 常量（proxyAttempt 实际请求位置生成，emit 时随管线传入）。
// 本插件在 HandleBeforeAttempt 里覆写为本次 attempt 的 UUID（per-attempt 独立日志），
// 收尾事件（after-upstream/stream-chunk/upstream-failed）经 pipeRequestLogID 读它命中本次行。
const metadataKey = modelgateway.MetadataRequestLogID

// attemptMetadataKey 本次 attempt 的 request-log 关联 UUID 键：model-gateway 写
// route_attempts 行时读取落 request_log_id 列（前端内层行渲染「日志」按钮）。
const attemptMetadataKey = modelgateway.MetadataRequestLogAttemptID

// Service 完整请求日志适配器：查能力路由、抓请求/响应快照落独立库。
// 所有写库 best-effort：handler 永不 return error（否则中断整个 /v1 转发），
// 出错仅记日志。
type Service struct {
	st      *store.Store
	lg      *slog.Logger
	reqDB   *sql.DB        // 独立库 request-log.db（request_logs / request_log_config）
	loadout *sql.DB        // loadout.db：UPDATE route_requests.request_log_id 关联列
	repo    *db.Repository // SQLite 能力路由数据源（装配后注入；nil 时回退 JSON）
}

// NewService 创建完整请求日志适配器。
// database 为 loadout.db（写关联列用，测试可传 nil 跳过 UPDATE）。
func NewService(st *store.Store, lg *slog.Logger, reqDB, database *sql.DB) *Service {
	return &Service{st: st, lg: lg, reqDB: reqDB, loadout: database}
}

// SetRepository 注入 SQLite 仓储（由装配层在 db 就绪后调用；测试可省略）。
// 同时用于能力路由查询与 route_requests.request_log_id 的 UPDATE（同一 loadout.db）。
func (s *Service) SetRepository(repo *db.Repository) { s.repo = repo }

// DecideRoute 查能力路由表：model + channelID 的 request_log 能力。
// 未命中返回 nil（视为 native 透传）。channelID 空 = 全渠道命中（与 sensitive-filter 一致）。
func (s *Service) DecideRoute(model, channelID string) (*types.CapabilityRoute, error) {
	scope := types.ChannelRequestScope{}
	if channelID != "" {
		scope.IDs = []string{channelID}
		if bu := s.requestChannelBaseURL(channelID); bu != "" {
			scope.BaseURLs = []string{bu}
		}
	}
	routes, err := s.DecideRoutesScope(model, scope)
	if err != nil || len(routes) == 0 {
		return nil, err
	}
	return routes[0], nil
}

// DecideRoutesScope 查能力路由表：model + 请求渠道上下文（含聚合模型的候选 Key 集合）。
// 返回所有匹配的路由；native/error 路由立即返回，proxy 路由收集全部匹配项。
// 读表/解析失败 fail-open：记录日志并返回 nil（按 native 透传），不拒绝请求。
func (s *Service) DecideRoutesScope(model string, scope types.ChannelRequestScope) ([]*types.CapabilityRoute, error) {
	if s.repo != nil {
		routes, err := s.repo.ListCapabilityRoutes(context.Background())
		if err == nil {
			var matched []*types.CapabilityRoute
			for i := range routes {
				if routes[i].Capability == capabilityName &&
					types.MatchModels(routes[i].Models, model) &&
					types.MatchChannelScopeEx(routes[i].ChannelIDs, routes[i].ChannelBaseURLs, scope) {
					if routes[i].Route != types.RouteProxy {
						return []*types.CapabilityRoute{&routes[i]}, nil
					}
					matched = append(matched, &routes[i])
				}
			}
			return matched, nil
		}
		s.lg.Error("request-log: 从 SQLite 读能力路由表失败，回退 JSON", "err", err)
	}
	var routes []types.CapabilityRoute
	if err := s.st.Read(types.FileCapabilityRoutes, &routes); err != nil {
		if errors.Is(err, store.ErrNotExist) {
			return nil, nil
		}
		s.lg.Error("request-log: 读取能力路由表失败，按透传处理", "err", err)
		return nil, nil
	}
	var matched []*types.CapabilityRoute
	for i := range routes {
		if routes[i].Capability == capabilityName &&
			types.MatchModels(routes[i].Models, model) &&
			types.MatchChannelScopeEx(routes[i].ChannelIDs, routes[i].ChannelBaseURLs, scope) {
			if routes[i].Route != types.RouteProxy {
				return []*types.CapabilityRoute{&routes[i]}, nil
			}
			matched = append(matched, &routes[i])
		}
	}
	return matched, nil
}

// requestChannelBaseURL 取请求渠道的 base_url（用于渠道级匹配），无渠道或查不到返回空串。
func (s *Service) requestChannelBaseURL(channelID string) string {
	if channelID == "" || s.repo == nil {
		return ""
	}
	channels, err := s.repo.ListChannels(context.Background())
	if err != nil {
		return ""
	}
	for _, ch := range channels {
		if ch.ID == channelID {
			return ch.BaseURL
		}
	}
	return ""
}

// redactEnabled 读脱敏开关（request_log_config.redact，默认 1=开）。
// 读失败按开启处理（安全默认）。
func (s *Service) redactEnabled() bool {
	if s.reqDB == nil {
		return true
	}
	var redact int
	if err := s.reqDB.QueryRow(`SELECT redact FROM request_log_config WHERE id = 1`).Scan(&redact); err != nil {
		return true
	}
	return redact != 0
}

// subscribe 订阅 model-gateway 事件。
func (s *Service) subscribe(ctx plugin.Context) {
	ctx.On(modelgateway.ProxyBeforeAttempt, s.HandleBeforeAttempt)
	ctx.On(modelgateway.ProxyAfterUpstream, s.HandleAfterUpstream)
	ctx.On(modelgateway.ProxyUpstreamFailed, s.HandleUpstreamFailed)
	// proxy:attempt-failed：每次渠道尝试失败即触发（普通模型 + 聚合中间失败 attempt），
	// payload 与 ProxyUpstreamFailed 同构（*ProxyFailurePayload），直接复用收尾逻辑。
	ctx.On(modelgateway.ProxyAttemptFailed, s.HandleUpstreamFailed)
	ctx.On(modelgateway.ProxyStreamChunk, s.HandleStreamChunk)
}

// ---- 请求方向：proxy:before-attempt（请求发出之前抓完整请求 + 生成 UUID） ----

// HandleBeforeAttempt 每次渠道尝试前触发（proxy.go:282，早于构建 :304 / 发出 :337）。
// 本 handler 只**消费** UUID——model-gateway 已在实际请求位置（proxyAttempt）生成并
// 写入 pipe.Metadata（MetadataRequestLogID），emit 事件时随管线传入；这里不自己造，
// 仅当 metadata 缺失（测试直构 pipe / 管道被重建）才兜底：反查 route_requests 复用，
// 再不行才自造。
// 命中 request_log 能力路由时：UPDATE route_requests.request_log_id → 写 request_logs
// 半条（running）→ 标记已记录（failover 同 pipe 重复触发时早退）。
// 【铁律】永不 return error、永不改 body/响应——只读快照，出错仅记日志（best-effort）。
func (s *Service) HandleBeforeAttempt(payload any) (any, error) {
	pipe, ok := payload.(*modelgateway.ProxyPipeline)
	if !ok || pipe == nil || pipe.Request == nil || s.reqDB == nil {
		return payload, nil
	}
	if pipe.Metadata == nil {
		pipe.Metadata = map[string]any{}
	}
	// 能力路由匹配用「当前 attempt 的真实模型」：聚合插件在 before-upstream 已把
	// pipe.Request.Model 改写为真实模型（aggregate/service.go:124），虚拟名只留在
	// __virtual_model 里供 route-log 展示。不能拿虚拟名覆盖——否则用户只配置物理模型
	// （如 hy3）时，聚合内部切换到的真实模型永远匹配不上（与 sensitive-filter 对齐）。
	model := pipe.Request.Model
	scope := types.ChannelScopeFromMetadata(pipe.Metadata, s.requestChannelBaseURL)
	routes, err := s.DecideRoutesScope(model, scope)
	if err != nil {
		s.lg.Warn("request-log: 能力路由决策失败，跳过记录", "request_id", pipe.RequestID, "err", err)
		return payload, nil
	}
	if len(routes) == 0 || routes[0].Route != types.RouteProxy {
		return payload, nil // 未命中 / native：不记录（列保持 NULL，前端不显示入口）
	}

	// 每次渠道尝试独立 UUID（per-attempt 语义）：不复用 model-gateway 的 pipe 级 UUID
	//（failover 同 pipe 所有 attempt 共享同一个，无法区分），每次生成新 UUID 写新行。
	uuid := newRequestLogID()
	// 覆写 metadata：收尾事件（after/stream-chunk/upstream-failed）经 pipeRequestLogID
	// 读 metadataKey 命中本次行；model-gateway 写 route_attempts 行读 attemptMetadataKey
	// 落 request_log_id 列（前端内层行渲染「日志」按钮）。
	pipe.Metadata[metadataKey] = uuid
	pipe.Metadata[attemptMetadataKey] = uuid

	// UPDATE 关联列（best-effort；loadout 连接为空或行不存在时忽略，不影响记录）。
	// COALESCE 条件保证仅首次命中写入——外层按钮指向第一次渠道尝试的日志。
	if s.loadout != nil {
		if _, err := s.loadout.Exec(`UPDATE route_requests SET request_log_id = ? WHERE request_id = ? AND COALESCE(request_log_id, '') = ''`, uuid, pipe.RequestID); err != nil {
			s.lg.Warn("request-log: 写 route_requests.request_log_id 失败", "request_id", pipe.RequestID, "err", err)
		}
	}

	channel, _ := pipe.Metadata["__current_channel"].(string)
	started := time.Now().UTC()
	snap := buildRequestSnapshot(pipe, model, s.redactEnabled())
	reqJSON, err := json.Marshal(snap)
	if err != nil {
		s.lg.Warn("request-log: 序列化请求快照失败", "request_id", pipe.RequestID, "err", err)
		return payload, nil
	}
	// 纯 INSERT：每次 attempt 独立 UUID 恒不冲突（无需 ON CONFLICT 分支）。
	if _, err := s.reqDB.Exec(`INSERT INTO request_logs(id, request_id, model, channel, stream, started_at, result, request_json, created_at) VALUES (?, ?, ?, ?, ?, ?, 'running', ?, ?)`,
		uuid, pipe.RequestID, model, channel, boolToInt(pipe.Request.Stream), started.Format(time.RFC3339Nano), string(reqJSON), started.Format(time.RFC3339Nano)); err != nil {
		s.lg.Warn("request-log: 写 request_logs 半条失败", "request_id", pipe.RequestID, "err", err)
		return payload, nil
	}
	return payload, nil
}

// ---- 输出方向：非流式收尾（2xx 走 after-upstream，失败走 upstream-failed） ----

// HandleAfterUpstream 非流式 2xx 响应返回后触发（proxy.go:413，仅 2xx 分支）。
// 收尾 request_logs：result=success、response_json/http_status/finished_at/duration_ms、
// channel 回填 __last_tried_channel。永不 return error。
func (s *Service) HandleAfterUpstream(payload any) (any, error) {
	ap, ok := payload.(*modelgateway.AfterUpstreamPayload)
	if !ok || ap == nil || ap.Pipe == nil || s.reqDB == nil {
		return payload, nil
	}
	uuid := s.pipeRequestLogID(ap.Pipe)
	if uuid == "" {
		return payload, nil // 未被记录（未命中能力路由）
	}
	channel, _ := ap.Pipe.Metadata["__last_tried_channel"].(string)
	snap := responseSnapshot{
		StatusCode: ap.Response.StatusCode,
		Headers:    redactHeaders(ap.Response.Header, s.redactEnabled()),
		Body:       redactBody(string(ap.Response.Body), s.redactEnabled()),
	}
	respJSON, err := json.Marshal(snap)
	if err != nil {
		s.lg.Warn("request-log: 序列化响应快照失败", "request_id", ap.Pipe.RequestID, "err", err)
		return payload, nil
	}
	s.finishRequestLog(uuid, ap.Response.StatusCode, string(respJSON), channel, "success")
	return payload, nil
}

// HandleUpstreamFailed 上游转发失败（4xx/5xx、无渠道、网络错误、安检拒绝）。
// ProxyAfterUpstream 仅 2xx 触发，失败必须靠本事件收尾（B2），否则行永远卡 running。
// 永不 return error。
func (s *Service) HandleUpstreamFailed(payload any) (any, error) {
	fp, ok := payload.(*modelgateway.ProxyFailurePayload)
	if !ok || fp == nil || fp.Pipe == nil || s.reqDB == nil {
		return payload, nil
	}
	uuid := s.pipeRequestLogID(fp.Pipe)
	if uuid == "" {
		return payload, nil
	}
	channel, _ := fp.Pipe.Metadata["__last_tried_channel"].(string)
	snap := responseSnapshot{
		StatusCode: fp.StatusCode,
		Body:       redactBody(fp.ErrorBody, s.redactEnabled()),
	}
	respJSON, err := json.Marshal(snap)
	if err != nil {
		s.lg.Warn("request-log: 序列化失败快照失败", "request_id", fp.Pipe.RequestID, "err", err)
		return payload, nil
	}
	s.finishRequestLog(uuid, fp.StatusCode, string(respJSON), channel, "failed")
	return payload, nil
}

// responseSnapshot request_logs.response_json 的结构。
type responseSnapshot struct {
	StatusCode int             `json:"status_code"`
	Headers    headerSnapshot  `json:"headers,omitempty"`
	Body       string          `json:"body,omitempty"`
	Truncated  bool            `json:"truncated,omitempty"` // 流式缓冲触顶截断标记
}

// pipeRequestLogID 取本次请求的 UUID：metadata 优先（同 pipe），丢失则按
// request_id 反查独立库（插件重建 pipe / 恢复场景）。
func (s *Service) pipeRequestLogID(pipe *modelgateway.ProxyPipeline) string {
	if pipe == nil {
		return ""
	}
	if pipe.Metadata != nil {
		if id, _ := pipe.Metadata[metadataKey].(string); id != "" {
			return id
		}
	}
	if s.reqDB == nil || pipe.RequestID == "" {
		return ""
	}
	var id string
	if err := s.reqDB.QueryRow(`SELECT id FROM request_logs WHERE request_id = ? ORDER BY created_at DESC LIMIT 1`, pipe.RequestID).Scan(&id); err != nil {
		return ""
	}
	return id
}

// finishRequestLog 收尾：写响应快照、状态码、结束时刻、时长、result；channel 非空时回填。
// best-effort，出错仅记日志。
func (s *Service) finishRequestLog(uuid string, status int, respJSON, channel, result string) {
	var startedStr string
	if err := s.reqDB.QueryRow(`SELECT started_at FROM request_logs WHERE id = ?`, uuid).Scan(&startedStr); err != nil {
		s.lg.Warn("request-log: 收尾时找不到记录", "id", uuid, "err", err)
		return
	}
	started, err := time.Parse(time.RFC3339Nano, startedStr)
	if err != nil {
		s.lg.Warn("request-log: 解析 started_at 失败", "id", uuid, "err", err)
		return
	}
	now := time.Now().UTC()
	duration := now.Sub(started).Milliseconds()
	if _, err := s.reqDB.Exec(`UPDATE request_logs SET response_json = ?, http_status = ?, finished_at = ?, duration_ms = ?, result = ?, channel = CASE WHEN ? = '' THEN channel ELSE ? END WHERE id = ?`,
		respJSON, status, now.Format(time.RFC3339Nano), duration, result, channel, channel, uuid); err != nil {
		s.lg.Warn("request-log: 收尾写库失败", "id", uuid, "err", err)
	}
}

// ---- 输出方向：流式逐块拼接 ----

// streamBufferKey 流式响应累积缓冲（SSE 原文逐块拼接）在 metadata 中的键。
const streamBufferKey = "__request_log_buffer"

// streamTruncatedKey 流式缓冲触顶截断标记（response_json 会带 "truncated": true）。
const streamTruncatedKey = "__request_log_truncated"

// maxStreamBuffer 流式响应缓冲上限（防大流式 OOM；超限后丢弃后续 chunk 并标记截断）。
var maxStreamBuffer = 32 << 20 // 32MB

// HandleStreamChunk 流式响应逐块触发（proxy.go:659，SSE 每行）。把 chunk 原文
// （脱敏后）追加到 metadata 缓冲；检测到 "data: [DONE]"（model-gateway 内部 isSSEDone
// 是私有函数，这里等价实现）时收尾：result=success、response_json=SSE 原文。
// 缓冲超 maxStreamBuffer 截断并标记 truncated。中断（断连/EOF 无 [DONE]）保持
// running，靠 self-heal 超时收尾。永不 return error。
func (s *Service) HandleStreamChunk(payload any) (any, error) {
	sp, ok := payload.(*modelgateway.StreamChunkPayload)
	if !ok || sp == nil || sp.Pipe == nil || s.reqDB == nil {
		return payload, nil
	}
	uuid := s.pipeRequestLogID(sp.Pipe)
	if uuid == "" {
		return payload, nil // 未被记录
	}
	if sp.Pipe.Metadata == nil {
		sp.Pipe.Metadata = map[string]any{}
	}
	buf, _ := sp.Pipe.Metadata[streamBufferKey].(*strings.Builder)
	if buf == nil {
		buf = &strings.Builder{}
		sp.Pipe.Metadata[streamBufferKey] = buf
	}
	// 触顶后丢弃后续 chunk（只保留截断标记），防止大流式无限累积
	if buf.Len() < maxStreamBuffer {
		buf.WriteString(redactBody(string(sp.Data), s.redactEnabled()))
	} else {
		sp.Pipe.Metadata[streamTruncatedKey] = true
	}

	if !isSSEDoneLine(string(sp.Data)) {
		return payload, nil
	}
	truncated, _ := sp.Pipe.Metadata[streamTruncatedKey].(bool)
	snap := responseSnapshot{StatusCode: http.StatusOK, Body: buf.String(), Truncated: truncated}
	respJSON, err := json.Marshal(snap)
	if err != nil {
		s.lg.Warn("request-log: 序列化流式快照失败", "request_id", sp.Pipe.RequestID, "err", err)
		return payload, nil
	}
	channel, _ := sp.Pipe.Metadata["__last_tried_channel"].(string)
	s.finishRequestLog(uuid, http.StatusOK, string(respJSON), channel, "success")
	return payload, nil
}

// isSSEDoneLine 判断一条 SSE 行是否为流结束标记 data: [DONE]
// （允许 data:[DONE] 无空格写法），与 model-gateway 的 isSSEDone 语义一致。
func isSSEDoneLine(line string) bool {
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "data:") {
		return false
	}
	return strings.TrimSpace(line[len("data:"):]) == "[DONE]"
}

// ---- 查询：列表 / 详情 / self-heal ----

// requestLogItem 列表行（不含 request_json/response_json，避免大 payload）。
type requestLogItem struct {
	ID         string  `json:"id"`
	RequestID  string  `json:"request_id"`
	Model      string  `json:"model"`
	Channel    string  `json:"channel"`
	HTTPStatus int     `json:"http_status,omitempty"`
	Stream     bool    `json:"stream"`
	StartedAt  string  `json:"started_at"`
	FinishedAt *string `json:"finished_at,omitempty"`
	DurationMS int64   `json:"duration_ms,omitempty"`
	Result     string  `json:"result"`
}

// requestLogPage 列表响应（与计划定义一致：{items, total}，非 admin-api 裸数组）。
type requestLogPage struct {
	Items []requestLogItem `json:"items"`
	Total int              `json:"total"`
}

// requestLogDetail 详情行：列表字段 + 完整 request/response JSON。
type requestLogDetail struct {
	ID           string          `json:"id"`
	RequestID    string          `json:"request_id"`
	Model        string          `json:"model"`
	Channel      string          `json:"channel"`
	HTTPStatus   int             `json:"http_status,omitempty"`
	Stream       bool            `json:"stream"`
	StartedAt    string          `json:"started_at"`
	FinishedAt   *string         `json:"finished_at,omitempty"`
	DurationMS   int64           `json:"duration_ms,omitempty"`
	Result       string          `json:"result"`
	RequestJSON  json.RawMessage `json:"request_json"`
	ResponseJSON json.RawMessage `json:"response_json,omitempty"`
}

// requestLogFilter 列表过滤条件（对应 GET /api/request-logs 的 query 参数）。
type requestLogFilter struct {
	Model      string
	Channel    string
	RequestID  string
	Result     string
	StatusCode int  // 0 = 未过滤
	Stream     *int // nil = 未过滤（0/1 才过滤；用指针避免零值歧义）
	From       *time.Time
	To         *time.Time
	Limit      int
	Offset     int
}

// listWhere 返回 List/Count 共用的 WHERE 子句与参数。
func listWhere(filter requestLogFilter) (string, []any) {
	query := ` WHERE 1=1`
	args := []any{}
	if filter.Model != "" {
		query += ` AND model = ?`
		args = append(args, filter.Model)
	}
	if filter.Channel != "" {
		query += ` AND channel = ?`
		args = append(args, filter.Channel)
	}
	if filter.RequestID != "" {
		query += ` AND request_id = ?`
		args = append(args, filter.RequestID)
	}
	if filter.Result != "" {
		query += ` AND result = ?`
		args = append(args, filter.Result)
	}
	if filter.StatusCode != 0 {
		query += ` AND http_status = ?`
		args = append(args, filter.StatusCode)
	}
	if filter.Stream != nil {
		query += ` AND stream = ?`
		args = append(args, *filter.Stream)
	}
	if filter.From != nil {
		query += ` AND started_at >= ?`
		args = append(args, filter.From.UTC().Format(time.RFC3339Nano))
	}
	if filter.To != nil {
		query += ` AND started_at <= ?`
		args = append(args, filter.To.UTC().Format(time.RFC3339Nano))
	}
	return query, args
}

// List 列表 + 搜索（分页：Limit 默认 100、上限 500；Offset 可选）。
// 先对超时的 running 行批量 self-heal（P0：ProxyUpstreamFailed 仅聚合模型触发，
// 普通模型失败无输出事件，只能靠这里兜底收尾），再查当前页。
func (s *Service) List(ctx context.Context, filter requestLogFilter) (requestLogPage, error) {
	if s.reqDB == nil {
		return requestLogPage{}, fmt.Errorf("request-log: 独立库未装配")
	}
	s.healStuckList(ctx)
	where, whereArgs := listWhere(filter)
	var total int
	if err := s.reqDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM request_logs`+where, whereArgs...).Scan(&total); err != nil {
		return requestLogPage{}, err
	}
	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	query := `SELECT id, request_id, model, channel, COALESCE(http_status, 0), stream, started_at, finished_at, COALESCE(duration_ms, 0), result FROM request_logs` + where + ` ORDER BY started_at DESC LIMIT ? OFFSET ?`
	args := append(whereArgs, limit, offset)
	rows, err := s.reqDB.QueryContext(ctx, query, args...)
	if err != nil {
		return requestLogPage{}, err
	}
	defer rows.Close()
	items := make([]requestLogItem, 0)
	for rows.Next() {
		var it requestLogItem
		var finished sql.NullString
		var status int
		var stream int
		if err := rows.Scan(&it.ID, &it.RequestID, &it.Model, &it.Channel, &status, &stream, &it.StartedAt, &finished, &it.DurationMS, &it.Result); err != nil {
			return requestLogPage{}, err
		}
		it.HTTPStatus = status
		it.Stream = stream != 0
		if finished.Valid {
			it.FinishedAt = &finished.String
		}
		items = append(items, it)
	}
	return requestLogPage{Items: items, Total: total}, rows.Err()
}

// Detail 按 UUID 查详情。命中且卡 running 超时 → 先 self-heal 再返回；
// 未命中返回 sql.ErrNoRows 语义的错误（由 handler 转 404）。
func (s *Service) Detail(ctx context.Context, id string) (requestLogDetail, error) {
	if s.reqDB == nil {
		return requestLogDetail{}, fmt.Errorf("request-log: 独立库未装配")
	}
	var d requestLogDetail
	var status int
	var stream int
	var finished sql.NullString
	var respJSON sql.NullString
	var reqJSON string
	var started string
	if err := s.reqDB.QueryRowContext(ctx, `SELECT id, request_id, model, channel, COALESCE(http_status, 0), stream, started_at, finished_at, COALESCE(duration_ms, 0), result, request_json, response_json FROM request_logs WHERE id = ?`, id).
		Scan(&d.ID, &d.RequestID, &d.Model, &d.Channel, &status, &stream, &started, &finished, &d.DurationMS, &d.Result, &reqJSON, &respJSON); err != nil {
		return requestLogDetail{}, err
	}
	d.HTTPStatus = status
	d.Stream = stream != 0
	d.StartedAt = started
	d.RequestJSON = json.RawMessage(reqJSON)
	if finished.Valid {
		d.FinishedAt = &finished.String
	}
	if respJSON.Valid && respJSON.String != "" {
		d.ResponseJSON = json.RawMessage(respJSON.String)
	}
	// self-heal：卡 running 超时（断连/EOF 无 [DONE]、failover 中断等）收尾
	if d.Result == "running" && !finished.Valid {
		if parsed, err := time.Parse(time.RFC3339Nano, started); err == nil {
			s.healStuck(d.ID, d.RequestID, parsed)
			// 重读收尾结果（含 response_json：heal 可能从 route_requests 还原 error_body）
			if err := s.reqDB.QueryRowContext(ctx, `SELECT result, finished_at, COALESCE(http_status, 0), COALESCE(duration_ms, 0), response_json FROM request_logs WHERE id = ?`, id).
				Scan(&d.Result, &finished, &status, &d.DurationMS, &respJSON); err == nil {
				if finished.Valid {
					d.FinishedAt = &finished.String
				}
				d.HTTPStatus = status
				if respJSON.Valid && respJSON.String != "" {
					d.ResponseJSON = json.RawMessage(respJSON.String)
				}
			}
		}
	}
	return d, nil
}

// healStuckList 批量收尾超时 running 行（List 每次调用前执行）。
// running 行数量少（正常秒级收尾），扫描开销可忽略。
// 注意：必须先收集并 Close rows 再写库——SQLite 单连接（SetMaxOpenConns(1)）下
// 在 rows 迭代期间执行写操作会让新查询阻塞等连接释放，死锁。
func (s *Service) healStuckList(ctx context.Context) {
	rows, err := s.reqDB.QueryContext(ctx, `SELECT id, request_id, started_at FROM request_logs WHERE result = 'running' AND finished_at IS NULL`)
	if err != nil {
		return
	}
	type stuckRow struct{ id, reqID, startedStr string }
	var stuck []stuckRow
	for rows.Next() {
		var r stuckRow
		if err := rows.Scan(&r.id, &r.reqID, &r.startedStr); err != nil {
			continue
		}
		stuck = append(stuck, r)
	}
	_ = rows.Close() // 关键：释放连接后再写库
	for _, r := range stuck {
		started, err := time.Parse(time.RFC3339Nano, r.startedStr)
		if err != nil {
			continue
		}
		s.healStuck(r.id, r.reqID, started)
	}
}

// healStuck 卡 running 超时收尾：route_requests 侧已 failed（可反查）则标 failed
// 并带上已捕获的 http_status/error_body（写入 response_json），否则标
// stream_interrupted。复用 route-log 的 SelfHeal 阈值（config.RouteLogSelfHealTimeout）。
func (s *Service) healStuck(id, requestID string, started time.Time) {
	if time.Since(started) < config.RouteLogSelfHealTimeout {
		return
	}
	result := "stream_interrupted"
	status := 0
	errBody := ""
	if s.loadout != nil {
		// per-attempt 优先：失败 attempt 的错误信息在 route_attempts 表（429 等真实上游
		// 错误体），按 request_log_id 精确反查本行对应的 attempt。route_requests 只存
		// 外层最终结果——per-attempt 语义下外层 success 时它为空，反查 attempt 才能
		// 拿到失败详情（原实现只查 route_requests 会把失败 attempt 误标 stream_interrupted）。
		var attemptErr string
		var attemptStatus int
		if err := s.loadout.QueryRow(`SELECT COALESCE(error_body, ''), COALESCE(status_code, 0) FROM route_attempts WHERE request_log_id = ? ORDER BY started_at DESC LIMIT 1`, id).Scan(&attemptErr, &attemptStatus); err == nil && (attemptErr != "" || attemptStatus != 0) {
			errBody = attemptErr
			status = attemptStatus
			result = "failed"
		} else {
			var rr string
			if err := s.loadout.QueryRow(`SELECT COALESCE(result, '') FROM route_requests WHERE request_id = ?`, requestID).Scan(&rr); err == nil && rr == "failed" {
				result = "failed"
				_ = s.loadout.QueryRow(`SELECT COALESCE(http_status, 0) FROM route_requests WHERE request_id = ?`, requestID).Scan(&status)
				_ = s.loadout.QueryRow(`SELECT COALESCE(error_body, '') FROM route_requests WHERE request_id = ?`, requestID).Scan(&errBody)
			}
		}
	}
	// error_body 还原进 response_json（普通模型失败无事件，这是唯一拿到错误详情的途径）
	respJSON := ""
	if errBody != "" {
		snap := responseSnapshot{StatusCode: status, Body: redactBody(errBody, s.redactEnabled())}
		if b, err := json.Marshal(snap); err == nil {
			respJSON = string(b)
		}
	}
	now := time.Now().UTC()
	_, _ = s.reqDB.Exec(`UPDATE request_logs SET finished_at = ?, duration_ms = ?, result = ?, http_status = CASE WHEN ? = 0 THEN http_status ELSE ? END, response_json = CASE WHEN ? = '' THEN response_json ELSE ? END WHERE id = ?`,
		now.Format(time.RFC3339Nano), now.Sub(started).Milliseconds(), result, status, status, respJSON, respJSON, id)
}

// ---- HTTP handlers（RegisterRoute 注册，Auth: AuthSession 由框架挂 session） ----

// handleList GET /api/request-logs
func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	filter := requestLogFilter{
		Model:     r.URL.Query().Get("model"),
		Channel:   r.URL.Query().Get("channel"),
		RequestID: r.URL.Query().Get("request_id"),
		Result:    r.URL.Query().Get("result"),
	}
	if v := r.URL.Query().Get("from"); v != "" {
		if parsed, err := time.Parse(time.RFC3339, v); err == nil {
			filter.From = &parsed
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if parsed, err := time.Parse(time.RFC3339, v); err == nil {
			filter.To = &parsed
		}
	}
	if v := r.URL.Query().Get("status_code"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			filter.StatusCode = n
		}
	}
	if v := r.URL.Query().Get("stream"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && (n == 0 || n == 1) {
			filter.Stream = &n
		}
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			filter.Limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			filter.Offset = n
		}
	}
	page, err := s.List(r.Context(), filter)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{"message": err.Error()}})
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// handleDetail GET /api/request-logs/{id}
func (s *Service) handleDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	detail, err := s.Detail(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"message": "该请求未记录完整日志"}})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{"message": err.Error()}})
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// writeJSON 统一 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// newRequestLogID 生成 UUID：crypto/rand 16 字节 → 32 位 hex（零依赖，够唯一）。
func newRequestLogID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// requestSnapshot request_logs.request_json 的结构（body 存原始字符串保真，前端再格式化）。
type requestSnapshot struct {
	Method  string         `json:"method"`
	Path    string         `json:"path"`
	Query   string         `json:"query"`
	Headers headerSnapshot `json:"headers"`
	Body    string         `json:"body"`
	Model   string         `json:"model"`
	Stream  bool           `json:"stream"`
}

// buildRequestSnapshot 序列化请求快照（脱敏 + base64 图片占位）。
func buildRequestSnapshot(pipe *modelgateway.ProxyPipeline, model string, redact bool) requestSnapshot {
	return requestSnapshot{
		Method:  pipe.Request.Method,
		Path:    pipe.Request.Path,
		Query:   pipe.Request.Query,
		Headers: redactHeaders(pipe.Request.Header, redact),
		Body:    redactBody(string(pipe.Request.Body), redact),
		Model:   model,
		Stream:  pipe.Request.Stream,
	}
}

// ---- 脱敏 ----

// sensitiveHeaderKeys 打码的敏感头（不区分大小写，子串匹配）。
var sensitiveHeaderKeys = []string{"authorization", "api-key", "x-api-key", "cookie", "proxy-authorization"}

// headerSnapshot HTTP headers 快照：底层仍是 map[string][]string，但 JSON
// 序列化时单值输出为字符串，多值保留数组，避免前端看到所有 header 都是数组。
type headerSnapshot map[string][]string

func (h headerSnapshot) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, len(h))
	for k, vv := range h {
		switch len(vv) {
		case 0:
			m[k] = ""
		case 1:
			m[k] = vv[0]
		default:
			m[k] = vv
		}
	}
	return json.Marshal(m)
}

// Get 模拟 http.Header.Get：返回 key 的第一个值（大小写不敏感）。
func (h headerSnapshot) Get(key string) string {
	return http.Header(h).Get(key)
}

// UnmarshalJSON 支持单值字符串或多值数组两种写法，兼容序列化后的 JSON。
func (h *headerSnapshot) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	out := make(headerSnapshot, len(raw))
	for k, v := range raw {
		var arr []string
		if err := json.Unmarshal(v, &arr); err == nil {
			out[k] = arr
			continue
		}
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			out[k] = []string{s}
			continue
		}
		return fmt.Errorf("header %q: cannot unmarshal %s", k, v)
	}
	*h = out
	return nil
}

// redactHeaders 复制 headers，敏感键的值替换为 ***。
func redactHeaders(h http.Header, enabled bool) headerSnapshot {
	out := make(headerSnapshot, len(h))
	for k, vv := range h {
		if enabled && isSensitiveHeader(k) {
			out[k] = []string{"***"}
			continue
		}
		out[k] = append([]string(nil), vv...)
	}
	return out
}

func isSensitiveHeader(k string) bool {
	lk := strings.ToLower(k)
	for _, sk := range sensitiveHeaderKeys {
		if strings.Contains(lk, sk) {
			return true
		}
	}
	return false
}

// redactBody 对 body 文本做脱敏：sk- 密钥打码、base64 data URI 转占位标记。
func redactBody(body string, enabled bool) string {
	if !enabled {
		return body
	}
	// sk- 后跟 4+ 位字母数字才算密钥（避免误伤普通文本，且已打码的 sk-*** 不重复打码）
	body = skSecretRegex.ReplaceAllString(body, "sk-***")
	return dataURIRegex.ReplaceAllStringFunc(body, func(m string) string {
		parts := dataURIRegex.FindStringSubmatch(m)
		if len(parts) != 3 {
			return m
		}
		// 只算字节大小（解码占位统计用），不存图片字节
		n := len(parts[2]) * 3 / 4
		if decoded, derr := base64.StdEncoding.DecodeString(parts[2]); derr == nil {
			n = len(decoded)
		}
		return fmt.Sprintf("[image: %s, %dB]", parts[1], n)
	})
}

// skSecretRegex 匹配 sk- 开头的 API 密钥（4+ 位字母数字；已打码的 sk-*** 不命中）。
var skSecretRegex = regexp.MustCompile(`sk-[A-Za-z0-9]{4,}`)

// dataURIRegex 匹配 data:<mime>;base64,<payload>。
var dataURIRegex = regexp.MustCompile(`data:([a-zA-Z0-9.+-]+/[a-zA-Z0-9.+-]+);base64,([A-Za-z0-9+/=]+)`)

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

