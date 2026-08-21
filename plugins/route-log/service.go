package routelog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"loadout/core/config"
	"loadout/plugins/contracts"
)

// Service is a best-effort synchronous route-log store. Callers should log and
// continue when any method returns an error.
type Service struct {
	db *sql.DB
	lg *slog.Logger
	// 活跃转发登记表：request_id → 登记时刻。Start 时写入、Finish 时删除，
	// 覆盖「转发正在进行中」的请求（含视觉识别等 hook 阶段）。进程崩溃时
	// 表随进程消失——残留的 running 日志因此天然判死。并发访问用 mu 保护。
	mu       sync.Mutex
	activeAt map[string]time.Time
}

func NewService(database *sql.DB, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{db: database, lg: logger, activeAt: make(map[string]time.Time)}
}

func (s *Service) Start(ctx context.Context, request contracts.RouteRequest) error {
	// 活跃登记：Start 表示转发流程已开始（UPSERT 幂等，重复 Start 刷新登记时刻）。
	if request.RequestID != "" {
		s.mu.Lock()
		s.activeAt[request.RequestID] = time.Now()
		s.mu.Unlock()
	}
	// UPSERT：客户端重试时若复用同一 X-Request-Id，合并到同一条日志（保留首次 started_at），
	// 避免一次业务请求被拆成多条记录。
	// 冲突时同步更新 requested_model/virtual_model：model-gateway 在 before-upstream hook
	// 之前先写占位（running，虚拟模型未知），hook 确定 __virtual_model 后再调一次补全。
	_, err := s.db.ExecContext(ctx, `INSERT INTO route_requests(request_id, requested_model, virtual_model, started_at, result) VALUES (?, ?, NULLIF(?, ''), ?, 'running') ON CONFLICT(request_id) DO UPDATE SET result='running', requested_model=excluded.requested_model, virtual_model=excluded.virtual_model`, request.RequestID, request.RequestedModel, request.VirtualModel, request.StartedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Service) Attempt(ctx context.Context, attempt contracts.RouteAttempt) (int64, error) {
	metadata, err := safeMetadata(attempt.Metadata)
	if err != nil {
		return 0, err
	}
	result := attempt.Result
	if result == "" {
		result = "running"
	}
	action := attempt.Action
	if action == "" {
		action = "首次尝试"
	}
	var finished any
	if attempt.FinishedAt != nil {
		finished = attempt.FinishedAt.UTC().Format(time.RFC3339Nano)
	}
	resultRow, err := s.db.ExecContext(ctx, `INSERT INTO route_attempts(request_id, previous_attempt_id, step_no, action, model, channel_id, started_at, finished_at, result, failure_class, status_code, error_message, duration_ms, stream, prompt_tokens, completion_tokens, cached_tokens, metadata_json) VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, NULLIF(?, 0), ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(request_id, step_no) DO UPDATE SET action=excluded.action, model=excluded.model, channel_id=excluded.channel_id, started_at=excluded.started_at, finished_at=excluded.finished_at, result=excluded.result, failure_class=excluded.failure_class, status_code=excluded.status_code, error_message=excluded.error_message, duration_ms=excluded.duration_ms, stream=excluded.stream, prompt_tokens=excluded.prompt_tokens, completion_tokens=excluded.completion_tokens, cached_tokens=excluded.cached_tokens, metadata_json=excluded.metadata_json`, attempt.RequestID, attempt.PreviousAttemptID, attempt.StepNo, action, attempt.Model, attempt.ChannelID, attempt.StartedAt.UTC().Format(time.RFC3339Nano), finished, result, attempt.FailureClass, attempt.StatusCode, redact(attempt.ErrorMessage), attempt.Duration.Milliseconds(), boolToInt(attempt.Stream), attempt.PromptTokens, attempt.CompletionTokens, attempt.CachedTokens, metadata)
	if err != nil {
		return 0, err
	}
	return resultRow.LastInsertId()
}

func (s *Service) Finish(ctx context.Context, finish contracts.RouteFinish) error {
	// 活跃登记移除：转发流程结束（无论成败），不再视为活跃请求。
	if finish.RequestID != "" {
		s.mu.Lock()
		delete(s.activeAt, finish.RequestID)
		s.mu.Unlock()
	}
	_, err := s.db.ExecContext(ctx, `UPDATE route_requests SET finished_at=?, result=?, final_model=NULLIF(?, ''), final_channel_id=NULLIF(?, ''), http_status=NULLIF(?, 0), duration_ms=?, error_message=?, stream=?, prompt_tokens=?, completion_tokens=?, cached_tokens=? WHERE request_id=?`, finish.FinishedAt.UTC().Format(time.RFC3339Nano), finish.Result, finish.FinalModel, finish.FinalChannelID, finish.HTTPStatus, finish.Duration.Milliseconds(), redact(finish.ErrorMessage), boolToInt(finish.Stream), finish.PromptTokens, finish.CompletionTokens, finish.CachedTokens, finish.RequestID)
	return err
}

// IsActive 判断 request_id 对应的转发是否仍在进行中（登记表视角）：
//   - 表里有该 id 且未超过 maxAge → true（转发流程还活着，可能只是慢）；
//   - 表里没有 → false（转发已结束，或进程已崩溃导致表随进程消失）；
//   - 表里有但超过 maxAge → false（超时兜底：正常超时早该掐断，视为死锁/泄漏）。
func (s *Service) IsActive(requestID string, maxAge time.Duration) bool {
	if requestID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.activeAt[requestID]
	if !ok {
		return false
	}
	return time.Since(at) < maxAge
}

// SelfHeal 兜底收尾：Start 写 running 但 Finish 异常中断时，记录会永久卡 running。
// 判定分两层，任何一层命中即收尾（标 stream_interrupted）：
//  1. 活跃登记表：IsActive 为 false（转发已结束/进程已崩溃/超时兜底）——事实判死；
//  2. 时间兜底：result='running' 且 finished_at 为空 且距 started_at 超过 threshold。
// 两层都认为「还活着」时才 no-op，最大限度避免误杀真正在跑的请求。
// 修复动作幂等：UPDATE 带 WHERE result='running' AND finished_at IS NULL 防并发。
func (s *Service) SelfHeal(ctx context.Context, requestID string, threshold time.Duration) error {
	if threshold <= 0 {
		return nil
	}
	if s.IsActive(requestID, config.RouteLogSelfHealMaxAlive) {
		return nil
	}
	row := s.db.QueryRowContext(ctx, `SELECT started_at, result, finished_at FROM route_requests WHERE request_id = ?`, requestID)
	var startedStr string
	var result string
	var finished sql.NullString
	if err := row.Scan(&startedStr, &result, &finished); err != nil {
		// 记录不存在：no-op，不算错（detail 路径会返回 404）
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if result != "running" || finished.Valid {
		return nil
	}
	started, err := time.Parse(time.RFC3339Nano, startedStr)
	if err != nil {
		return err
	}
	// 仍在 threshold 内：保护真正在跑的请求，不误杀
	if time.Since(started) < threshold {
		return nil
	}
	now := time.Now().UTC()
	inferred := now.Sub(started)
	_, err = s.db.ExecContext(ctx,
		`UPDATE route_requests
		 SET finished_at = ?, result = 'stream_interrupted', duration_ms = ?,
		     error_message = COALESCE(NULLIF(error_message, ''), ?)
		 WHERE request_id = ? AND result = 'running' AND finished_at IS NULL`,
		now.Format(time.RFC3339Nano), inferred.Milliseconds(),
		"后端自我修复：写入流程异常中断，已基于 started_at 自动收尾（前端 detail 命中触发）",
		requestID)
	if err != nil {
		s.lg.Warn("route-log self-heal failed", "request_id", requestID, "err", err)
	}
	return err
}

func (s *Service) List(ctx context.Context, filter contracts.RouteLogFilter) ([]contracts.RouteRequestView, error) {
	query := `SELECT request_id, requested_model, COALESCE(virtual_model, ''), started_at, finished_at, result, COALESCE(final_model, ''), COALESCE(final_channel_id, ''), COALESCE(http_status, 0), COALESCE(duration_ms, 0), error_message, COALESCE(stream, 0), COALESCE(prompt_tokens, 0), COALESCE(completion_tokens, 0), COALESCE(cached_tokens, 0) FROM route_requests r WHERE 1=1`
	args := []any{}
	if filter.Model != "" {
		query += ` AND (r.requested_model = ? OR r.final_model = ? OR EXISTS (SELECT 1 FROM route_attempts a WHERE a.request_id = r.request_id AND a.model = ?))`
		args = append(args, filter.Model, filter.Model, filter.Model)
	}
	if filter.ChannelID != "" {
		query += ` AND (r.final_channel_id = ? OR EXISTS (SELECT 1 FROM route_attempts a WHERE a.request_id = r.request_id AND a.channel_id = ?))`
		args = append(args, filter.ChannelID, filter.ChannelID)
	}
	if filter.Result != "" {
		query += ` AND r.result = ?`
		args = append(args, filter.Result)
	}
	if filter.StartedAfter != nil {
		query += ` AND r.started_at >= ?`
		args = append(args, filter.StartedAfter.UTC().Format(time.RFC3339Nano))
	}
	if filter.StartedBefore != nil {
		query += ` AND r.started_at <= ?`
		args = append(args, filter.StartedBefore.UTC().Format(time.RFC3339Nano))
	}
	query += ` ORDER BY r.started_at DESC LIMIT ?`
	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// 初始化为空切片而非 nil，保证空列表 JSON 序列化为 [] 而不是 null
	result := make([]contracts.RouteRequestView, 0)
	for rows.Next() {
		view, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, view)
	}
	return result, rows.Err()
}

func (s *Service) Detail(ctx context.Context, requestID string) (contracts.RouteRequestView, error) {
	row := s.db.QueryRowContext(ctx, `SELECT request_id, requested_model, COALESCE(virtual_model, ''), started_at, finished_at, result, COALESCE(final_model, ''), COALESCE(final_channel_id, ''), COALESCE(http_status, 0), COALESCE(duration_ms, 0), error_message, COALESCE(stream, 0), COALESCE(prompt_tokens, 0), COALESCE(completion_tokens, 0), COALESCE(cached_tokens, 0) FROM route_requests WHERE request_id=?`, requestID)
	view, err := scanRequest(row)
	if err != nil {
		return contracts.RouteRequestView{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, request_id, previous_attempt_id, step_no, action, model, COALESCE(channel_id, ''), started_at, finished_at, result, failure_class, COALESCE(status_code, 0), error_message, COALESCE(duration_ms, 0), COALESCE(stream, 0), COALESCE(prompt_tokens, 0), COALESCE(completion_tokens, 0), COALESCE(cached_tokens, 0), metadata_json FROM route_attempts WHERE request_id=? ORDER BY step_no`, requestID)
	if err != nil {
		return contracts.RouteRequestView{}, err
	}
	defer rows.Close()
	// 初始化为空切片，避免空 attempts JSON 序列化为 null
	view.Attempts = make([]contracts.RouteAttempt, 0)
	for rows.Next() {
		var id int64
		var attempt contracts.RouteAttempt
		var previous sql.NullInt64
		var started string
		var finished sql.NullString
		var duration int64
		var stream int
		var promptTokens, completionTokens, cachedTokens int
		var metadata string
		if err := rows.Scan(&id, &attempt.RequestID, &previous, &attempt.StepNo, &attempt.Action, &attempt.Model, &attempt.ChannelID, &started, &finished, &attempt.Result, &attempt.FailureClass, &attempt.StatusCode, &attempt.ErrorMessage, &duration, &stream, &promptTokens, &completionTokens, &cachedTokens, &metadata); err != nil {
			return contracts.RouteRequestView{}, err
		}
		attempt.PreviousAttemptID = nullInt64(previous)
		attempt.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
		if finished.Valid {
			if parsed, err := time.Parse(time.RFC3339Nano, finished.String); err == nil {
				attempt.FinishedAt = &parsed
			}
		}
		attempt.Duration = contracts.DurationMS(time.Duration(duration) * time.Millisecond)
		attempt.Stream = stream != 0
		attempt.PromptTokens = promptTokens
		attempt.CompletionTokens = completionTokens
		attempt.CachedTokens = cachedTokens
		_ = json.Unmarshal([]byte(metadata), &attempt.Metadata)
		view.Attempts = append(view.Attempts, attempt)
	}
	return view, rows.Err()
}

// Clear 清空全部转发日志（route_attempts 由外键 ON DELETE CASCADE 级联删除）。
// before 参数保留仅为兼容 contracts.RouteLog 接口，当前实现为全量清空。
func (s *Service) Clear(ctx context.Context, _ time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM route_requests`)
	return err
}

type scanner interface{ Scan(...any) error }

func scanRequest(scanner scanner) (contracts.RouteRequestView, error) {
	var view contracts.RouteRequestView
	var started string
	var finished sql.NullString
	var duration int64
	var stream int
	var promptTokens, completionTokens, cachedTokens int
	if err := scanner.Scan(&view.RequestID, &view.RequestedModel, &view.VirtualModel, &started, &finished, &view.Result, &view.FinalModel, &view.FinalChannelID, &view.HTTPStatus, &duration, &view.ErrorMessage, &stream, &promptTokens, &completionTokens, &cachedTokens); err != nil {
		return view, err
	}
	view.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
	if finished.Valid {
		if parsed, err := time.Parse(time.RFC3339Nano, finished.String); err == nil {
			view.FinishedAt = &parsed
		}
	}
	view.Duration = contracts.DurationMS(time.Duration(duration) * time.Millisecond)
	view.Stream = stream != 0
	view.PromptTokens = promptTokens
	view.CompletionTokens = completionTokens
	view.CachedTokens = cachedTokens
	return view, nil
}

func nullInt64(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	out := value.Int64
	return &out
}

func safeMetadata(metadata map[string]any) (string, error) {
	if metadata == nil {
		return "{}", nil
	}
	if sensitive(metadata) {
		return "", fmt.Errorf("route-log: sensitive metadata is forbidden")
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	if len(encoded) > 4096 {
		return "", fmt.Errorf("route-log: metadata exceeds 4 KiB")
	}
	return string(encoded), nil
}

func sensitive(value any) bool {
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "authorization") || strings.Contains(lower, "api_key") || strings.Contains(lower, "request_body") || strings.Contains(lower, "response_body") {
				return true
			}
			if sensitive(child) {
				return true
			}
		}
	case []any:
		for _, child := range current {
			if sensitive(child) {
				return true
			}
		}
	}
	return false
}

func redact(message string) string {
	if len(message) > 1024 {
		return message[:1024]
	}
	return strings.ReplaceAll(message, "sk-", "sk-***")
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

var _ contracts.RouteLog = (*Service)(nil)
