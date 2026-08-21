package modelhealth

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"loadout/plugins/contracts"
)

const (
	statusAvailable = "available"
	statusCooling   = "cooling"
	statusDisabled  = "disabled"
)

// Service implements the small cross-plugin model health contract.
type Service struct {
	db *sql.DB
	lg *slog.Logger
}

func NewService(database *sql.DB, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{db: database, lg: logger}
}

func pluginError(name string) error { return fmt.Errorf("model-health: missing %s service", name) }

// Start runs only state expiry. It never makes paid upstream requests.
func (s *Service) Start() func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(3 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.CheckNow(ctx, false); err != nil {
					s.lg.Warn("model health expiry check failed", "err", err)
				}
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { cancel(); <-done }) }
}

func (s *Service) Check(ctx context.Context, channelID, model string) (contracts.Availability, error) {
	var channelManual bool
	var channelStatus string
	var channelUntil sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT c.manual_enabled, COALESCE(cs.status, 'available'), cs.disabled_until FROM channels c LEFT JOIN channel_states cs ON cs.channel_id = c.id WHERE c.id = ?`, channelID).Scan(&channelManual, &channelStatus, &channelUntil); err != nil {
		return contracts.Availability{}, fmt.Errorf("model-health: find channel: %w", err)
	}
	if !channelManual {
		return unavailable(false, channelStatus, "channel manually disabled"), nil
	}
	if !usableStatus(channelStatus, channelUntil.String) {
		return unavailable(true, channelStatus, "channel "+channelStatus), nil
	}
	var modelManual bool
	var modelStatus string
	var modelUntil sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT manual_enabled, status, disabled_until FROM model_states WHERE channel_id = ? AND model = ?`, channelID, model).Scan(&modelManual, &modelStatus, &modelUntil)
	if err == sql.ErrNoRows {
		return contracts.Availability{ManualEnabled: true, HealthStatus: statusAvailable, EffectiveAvailable: true}, nil
	}
	if err != nil {
		return contracts.Availability{}, fmt.Errorf("model-health: find model: %w", err)
	}
	if !modelManual {
		return unavailable(false, modelStatus, "model manually disabled"), nil
	}
	if !usableStatus(modelStatus, modelUntil.String) {
		return unavailable(true, modelStatus, "model "+modelStatus), nil
	}
	return contracts.Availability{ManualEnabled: true, HealthStatus: statusAvailable, EffectiveAvailable: true}, nil
}

func unavailable(manual bool, status, reason string) contracts.Availability {
	return contracts.Availability{ManualEnabled: manual, HealthStatus: status, EffectiveAvailable: false, Reason: reason}
}

func usableStatus(status, disabledUntil string) bool {
	if status == statusAvailable {
		return true
	}
	if status != statusCooling || disabledUntil == "" {
		return false
	}
	until, err := time.Parse(time.RFC3339Nano, disabledUntil)
	if err != nil {
		return false
	}
	return !time.Now().Before(until)
}

// catalogAllows 判断渠道目录是否允许为该模型记录健康状态：
//   - 渠道从未探测过（channel_models 为空）：没有目录可依，放行（保持历史行为，避免误伤真实可用模型）；
//   - 目录明确不含该模型：拒绝写入。防止路由失败尝试（如聚合模型目标里拼错的模型名）
//     把"不存在的模型"写进 model_states，污染模型状态列表。
func (s *Service) catalogAllows(ctx context.Context, channelID, model string) (bool, error) {
	var catalogCount, modelCount int
	if err := s.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM channel_models WHERE channel_id = ?),
		(SELECT COUNT(*) FROM channel_models WHERE channel_id = ? AND model = ?)`, channelID, channelID, model).Scan(&catalogCount, &modelCount); err != nil {
		return false, fmt.Errorf("model-health: check catalog: %w", err)
	}
	if catalogCount == 0 {
		return true, nil
	}
	return modelCount > 0, nil
}

func (s *Service) RecordSuccess(ctx context.Context, channelID, model string) error {
	if ok, err := s.catalogAllows(ctx, channelID, model); err != nil {
		return err
	} else if !ok {
		s.lg.Debug("模型不在渠道目录，跳过健康状态记录", "channel_id", channelID, "model", model)
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `INSERT INTO model_states(channel_id, model, manual_enabled, status, fail_count, last_success_at, updated_at) VALUES (?, ?, 1, 'available', 0, ?, ?) ON CONFLICT(channel_id, model) DO UPDATE SET status='available', disabled_until=NULL, fail_count=0, last_error='', last_failure_class='', last_success_at=excluded.last_success_at, updated_at=excluded.updated_at`, channelID, model, now, now)
	if err != nil {
		return fmt.Errorf("model-health: record success: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO channel_states(channel_id, status, fail_count, last_success_at, updated_at) VALUES (?, 'available', 0, ?, ?) ON CONFLICT(channel_id) DO UPDATE SET status='available', disabled_until=NULL, fail_count=0, last_error='', last_failure_class='', last_success_at=excluded.last_success_at, updated_at=excluded.updated_at`, channelID, now, now)
	return err
}

func (s *Service) RecordFailure(ctx context.Context, failure contracts.RouteFailure) (string, error) {
	if ok, err := s.catalogAllows(ctx, failure.ChannelID, failure.Model); err != nil {
		return "", err
	} else if !ok {
		s.lg.Debug("模型不在渠道目录，跳过健康状态记录", "channel_id", failure.ChannelID, "model", failure.Model)
		return "", nil
	}
	if shouldIgnoreFailure(failure) {
		s.lg.Debug("忽略非模型服务错误，不记录健康状态", "channel_id", failure.ChannelID, "model", failure.Model, "status_code", failure.StatusCode)
		return "", nil
	}
	class := classify(failure)
	now := time.Now().UTC()
	status, until := failureState(class, now)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO model_states(channel_id, model, manual_enabled, status, disabled_until, fail_count, last_error, last_failure_class, last_checked_at, updated_at) VALUES (?, ?, 1, ?, ?, 1, ?, ?, ?, ?) ON CONFLICT(channel_id, model) DO UPDATE SET status=excluded.status, disabled_until=excluded.disabled_until, fail_count=model_states.fail_count+1, last_error=excluded.last_error, last_failure_class=excluded.last_failure_class, last_checked_at=excluded.last_checked_at, updated_at=excluded.updated_at`, failure.ChannelID, failure.Model, status, until, redact(failure.Error), class, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return class, fmt.Errorf("model-health: record model failure: %w", err)
	}
	var syncBilling bool
	if err := s.db.QueryRowContext(ctx, `SELECT sync_billing FROM channels WHERE id = ?`, failure.ChannelID).Scan(&syncBilling); err != nil {
		return class, fmt.Errorf("model-health: read channel billing policy: %w", err)
	}
	// 多 key 语义：auth(401 或明确 invalid api key) = 该 key 无效（过期/被删），除禁模型外
	// 必须把整条 key 记录（channel_states）置 disabled，否则路由仍会把它当作候选。
	// 纯 403（权限/封禁等）不连坐渠道，只按原逻辑禁模型，避免误伤。
	if class == "auth" && (failure.StatusCode == 401 || strings.Contains(strings.ToLower(strings.Join([]string{failure.Error, failure.ErrorBody}, " ")), "invalid api key")) {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO channel_states(channel_id, status, disabled_until, fail_count, last_error, last_failure_class, last_checked_at, updated_at) VALUES (?, 'disabled', NULL, 1, ?, ?, ?, ?) ON CONFLICT(channel_id) DO UPDATE SET status='disabled', disabled_until=NULL, fail_count=channel_states.fail_count+1, last_error=excluded.last_error, last_failure_class=excluded.last_failure_class, last_checked_at=excluded.last_checked_at, updated_at=excluded.updated_at`, failure.ChannelID, redact(failure.Error), class, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
			return class, fmt.Errorf("model-health: record channel failure: %w", err)
		}
	}
	if class == "channel_billing" && syncBilling {
		_, err := s.db.ExecContext(ctx, `INSERT INTO channel_states(channel_id, status, disabled_until, fail_count, last_error, last_failure_class, last_checked_at, updated_at) VALUES (?, 'disabled', NULL, 1, ?, ?, ?, ?) ON CONFLICT(channel_id) DO UPDATE SET status='disabled', disabled_until=NULL, fail_count=channel_states.fail_count+1, last_error=excluded.last_error, last_failure_class=excluded.last_failure_class, last_checked_at=excluded.last_checked_at, updated_at=excluded.updated_at`, failure.ChannelID, redact(failure.Error), class, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
		if err != nil {
			return class, fmt.Errorf("model-health: record channel failure: %w", err)
		}
	}
	return class, nil
}

func classify(failure contracts.RouteFailure) string {
	message := strings.ToLower(strings.Join([]string{failure.Error, failure.ErrorBody}, " "))
	switch {
	case failure.StatusCode == 401 || failure.StatusCode == 403 || strings.Contains(message, "invalid api key"):
		return "auth"
	case failure.StatusCode == 429 || strings.Contains(message, "rate limit"):
		return "rate_limit"
	case strings.Contains(message, "not support") || strings.Contains(message, "unsupported"):
		return "capability"
	case failure.StatusCode == 402:
		if strings.Contains(message, "account balance") || strings.Contains(message, "账户余额") || strings.Contains(message, "channel billing") {
			return "channel_billing"
		}
		return "model_quota"
	case strings.Contains(message, "timeout") || strings.Contains(message, "connection") || strings.Contains(message, "network"):
		return "network"
	case failure.StatusCode >= 500:
		return "temporary"
	default:
		return "unknown"
	}
}

func failureState(class string, now time.Time) (string, any) {
	if class == "auth" || class == "model_quota" {
		return statusDisabled, nil
	}
	cooldown := time.Minute
	if class == "rate_limit" {
		cooldown = 5 * time.Minute
	}
	return statusCooling, now.Add(cooldown).Format(time.RFC3339Nano)
}

func redact(message string) string {
	message = strings.ReplaceAll(message, "Authorization", "[redacted]")
	if len(message) > 1024 {
		return message[:1024]
	}
	return message
}

func shouldIgnoreFailure(failure contracts.RouteFailure) bool {
	switch failure.StatusCode {
	case 404, 405, 400:
		return true
	}
	if failure.StatusCode == 0 {
		message := strings.ToLower(strings.Join([]string{failure.Error, failure.ErrorBody}, " "))
		for _, kw := range []string{
			"no such host", "connection refused", "no route to host",
			"eof", "dial tcp", "lookup",
		} {
			if strings.Contains(message, kw) {
				return true
			}
		}
	}
	return false
}

func (s *Service) SetChannelEnabled(ctx context.Context, channelID string, enabled bool) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, `UPDATE channels SET manual_enabled = ?, updated_at = ? WHERE id = ?`, enabled, now, channelID); err != nil {
		return err
	}
	// 手动启用 = 开关 + 清自动熔断：auth 等自动禁用的渠道（channel_states disabled）
	// 必须一并恢复，否则路由仍会跳过该 key（Check 先查渠道状态）。
	// 同时恢复该 key 名下所有自动熔断的模型（manual_enabled=1 且非 available）；
	// 用户手动禁用的模型（manual_enabled=0）保持禁用，不强制打开。
	if enabled {
		if err := s.RecoverChannel(ctx, channelID); err != nil {
			return err
		}
		_, err := s.db.ExecContext(ctx, `UPDATE model_states SET status='available', disabled_until=NULL, fail_count=0, last_error='', last_failure_class='', updated_at=? WHERE channel_id=? AND manual_enabled=1 AND status != 'available'`, now, channelID)
		return err
	}
	return nil
}

func (s *Service) SetModelEnabled(ctx context.Context, channelID, model string, enabled bool) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `INSERT INTO model_states(channel_id, model, manual_enabled, status, updated_at) VALUES (?, ?, ?, 'available', ?) ON CONFLICT(channel_id, model) DO UPDATE SET manual_enabled=excluded.manual_enabled, updated_at=excluded.updated_at`, channelID, model, enabled, now)
	return err
}

// SetModelsEnabled 批量开启/关闭一个渠道下的多个模型。
func (s *Service) SetModelsEnabled(ctx context.Context, channelID string, models []string, enabled bool) error {
	for _, model := range models {
		if err := s.SetModelEnabled(ctx, channelID, model, enabled); err != nil {
			return err
		}
	}
	return nil
}

// DeleteModel 删除"手动添加"的模型：先校验 channel_models 中该模型 source='manual'，
// 仅删除手动添加的目录行与其健康状态记录（model_states）。自动探测（probe）的模型拒绝删除。
func (s *Service) DeleteModel(ctx context.Context, channelID, model string) error {
	var source string
	err := s.db.QueryRowContext(ctx, `SELECT source FROM channel_models WHERE channel_id = ? AND model = ?`, channelID, model).Scan(&source)
	if err == sql.ErrNoRows {
		return fmt.Errorf("model-health: model %q not in channel %q catalog", model, channelID)
	}
	if err != nil {
		return fmt.Errorf("model-health: find model for delete: %w", err)
	}
	if source != "manual" {
		return fmt.Errorf("model-health: model %q is %s, only manual models can be deleted", model, source)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM channel_models WHERE channel_id = ? AND model = ? AND source = 'manual'`, channelID, model); err != nil {
		return fmt.Errorf("model-health: delete model catalog: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM model_states WHERE channel_id = ? AND model = ?`, channelID, model); err != nil {
		return fmt.Errorf("model-health: delete model state: %w", err)
	}
	return nil
}

// DeleteModels 批量删除"手动添加"的模型（单事务，全部成功才提交）。
// 任一模型不是 manual 来源、或不在目录，则整体回滚并返回错误。
// 删除部分用单条 IN 批量执行，避免逐条 DELETE 的事务/执行开销。
func (s *Service) DeleteModels(ctx context.Context, channelID string, models []string) error {
	if len(models) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("model-health: begin delete models: %w", err)
	}
	defer tx.Rollback()
	for _, model := range models {
		var source string
		err := tx.QueryRowContext(ctx, `SELECT source FROM channel_models WHERE channel_id = ? AND model = ?`, channelID, model).Scan(&source)
		if err == sql.ErrNoRows {
			return fmt.Errorf("model-health: model %q not in channel %q catalog", model, channelID)
		}
		if err != nil {
			return fmt.Errorf("model-health: find model for delete: %w", err)
		}
		if source != "manual" {
			return fmt.Errorf("model-health: model %q is %s, only manual models can be deleted", model, source)
		}
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(models)), ",")
	args := make([]any, 0, len(models)+1)
	args = append(args, channelID)
	for _, m := range models {
		args = append(args, m)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM channel_models WHERE channel_id = ? AND model IN (%s) AND source = 'manual'`, placeholders), args...); err != nil {
		return fmt.Errorf("model-health: delete model catalog: %w", err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM model_states WHERE channel_id = ? AND model IN (%s)`, placeholders), args...); err != nil {
		return fmt.Errorf("model-health: delete model state: %w", err)
	}
	return tx.Commit()
}

func (s *Service) RecoverChannel(ctx context.Context, channelID string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `INSERT INTO channel_states(channel_id, status, fail_count, updated_at) VALUES (?, 'available', 0, ?) ON CONFLICT(channel_id) DO UPDATE SET status='available', disabled_until=NULL, fail_count=0, last_error='', last_failure_class='', updated_at=excluded.updated_at`, channelID, now)
	return err
}

func (s *Service) RecoverModel(ctx context.Context, channelID, model string) error {
	return s.RecordSuccess(ctx, channelID, model)
}

// RecoverModels 批量恢复（清自动熔断 + 强制打开手动开关）一个渠道下的多个模型。
func (s *Service) RecoverModels(ctx context.Context, channelID string, models []string) error {
	for _, model := range models {
		if err := s.RecoverModel(ctx, channelID, model); err != nil {
			return err
		}
	}
	return nil
}

// RecoverAllModels 把所有处于"非正常"状态的模型一次性归零：
//   - 自动健康熔断（status != 'available'）：清 fail_count / last_error / disabled_until
//   - 手动开关：把 manual_enabled 强制置 1
//
// 同时清掉所有渠道的自动熔断。影响行数按"模型 + 渠道"汇总返回。
// 注：此操作会覆盖用户主动关闭的模型开关，是破坏性操作，调用方需做二次确认。
func (s *Service) RecoverAllModels(ctx context.Context) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	modelsRes, err := s.db.ExecContext(ctx, `UPDATE model_states
		SET status='available',
		    disabled_until=NULL,
		    fail_count=0,
		    last_error='',
		    last_failure_class='',
		    manual_enabled=1,
		    updated_at=?
		WHERE manual_enabled=0 OR status != 'available'`, now)
	if err != nil {
		return 0, fmt.Errorf("model-health: recover all models: %w", err)
	}
	modelsAffected, _ := modelsRes.RowsAffected()
	channelsRes, err := s.db.ExecContext(ctx, `UPDATE channel_states
		SET status='available',
		    disabled_until=NULL,
		    fail_count=0,
		    last_error='',
		    last_failure_class='',
		    updated_at=?
		WHERE status != 'available'`, now)
	if err != nil {
		return modelsAffected, fmt.Errorf("model-health: recover all channels: %w", err)
	}
	channelsAffected, _ := channelsRes.RowsAffected()
	return modelsAffected + channelsAffected, nil
}

// RecoverAllChannels 把所有处于"非正常"状态的渠道一次性归零（全平台）：
//   - 自动健康熔断（status != 'available'）：清 fail_count / last_error / disabled_until
//
// 注意：只恢复渠道自身的自动状态，不碰模型开关。
func (s *Service) RecoverAllChannels(ctx context.Context) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	channelsRes, err := s.db.ExecContext(ctx, `UPDATE channel_states
		SET status='available',
		    disabled_until=NULL,
		    fail_count=0,
		    last_error='',
		    last_failure_class='',
		    updated_at=?
		WHERE status != 'available'`, now)
	if err != nil {
		return 0, fmt.Errorf("model-health: recover all channels: %w", err)
	}
	channelsAffected, _ := channelsRes.RowsAffected()
	return channelsAffected, nil
}

// RecoverAllModelsByChannel 把指定渠道内所有处于"非正常"状态的模型一次性归零：
//   - 自动健康熔断（status != 'available'）：清 fail_count / last_error / disabled_until
//   - 手动开关：把 manual_enabled 强制置 1
//
// 同时清掉该渠道的自动熔断。影响行数按"模型 + 渠道"汇总返回。
// 注：此操作会覆盖用户主动关闭的模型开关，是破坏性操作，调用方需做二次确认。
func (s *Service) RecoverAllModelsByChannel(ctx context.Context, channelID string) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	modelsRes, err := s.db.ExecContext(ctx, `UPDATE model_states
		SET status='available',
		    disabled_until=NULL,
		    fail_count=0,
		    last_error='',
		    last_failure_class='',
		    manual_enabled=1,
		    updated_at=?
		WHERE channel_id=? AND (manual_enabled=0 OR status != 'available')`, now, channelID)
	if err != nil {
		return 0, fmt.Errorf("model-health: recover all models of channel %s: %w", channelID, err)
	}
	modelsAffected, _ := modelsRes.RowsAffected()
	channelsRes, err := s.db.ExecContext(ctx, `UPDATE channel_states
		SET status='available',
		    disabled_until=NULL,
		    fail_count=0,
		    last_error='',
		    last_failure_class='',
		    updated_at=?
		WHERE channel_id=? AND status != 'available'`, now, channelID)
	if err != nil {
		return modelsAffected, fmt.Errorf("model-health: recover channel %s: %w", channelID, err)
	}
	channelsAffected, _ := channelsRes.RowsAffected()
	return modelsAffected + channelsAffected, nil
}

func (s *Service) CheckNow(ctx context.Context, _ bool) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `UPDATE model_states SET status='available', disabled_until=NULL, updated_at=? WHERE status='cooling' AND disabled_until <= ?`, now, now)
	if err != nil {
		return err
	}
	// 净化：清掉"已探测渠道"目录外的模型状态记录（幽灵模型，如聚合目标里拼错的模型名）。
	// 渠道未探测过（channel_models 为空）时保留，避免误删真实可用模型的记录。
	_, err = s.db.ExecContext(ctx, `DELETE FROM model_states WHERE NOT EXISTS (SELECT 1 FROM channel_models cm WHERE cm.channel_id = model_states.channel_id AND cm.model = model_states.model) AND EXISTS (SELECT 1 FROM channel_models cm2 WHERE cm2.channel_id = model_states.channel_id)`)
	return err
}

// channelStatusRow 渠道及其自动状态的扁平行。
type channelStatusRow struct {
	ID          string
	Name        string
	ChannelName string
	BaseURL     string
	Manual      bool
	SyncBilling bool
	Status      string
	Until       sql.NullString
}

// modelStateRow 模型自动状态的扁平行。
type modelStateRow struct {
	Manual      bool
	Status      string
	Until       sql.NullString
	FailCount   int
	LastError   string
	LastSuccess sql.NullString
}

// List 批量读取渠道、渠道状态、模型目录与模型状态后组装，避免逐渠道/逐模型的
// N+1 查询（原实现每个模型要额外 3 次 SQL）。
// 注意：SQLite 连接池为单连接（MaxOpenConns=1），这里按序读取、读完即关，
// 不能同时持有多个未关闭的 Rows，否则会互相等待同一连接而死锁。
func (s *Service) List(ctx context.Context) ([]contracts.ChannelStatus, error) {
	channels, err := s.listChannelStatus(ctx)
	if err != nil {
		return nil, err
	}
	catalog, err := s.listModelCatalog(ctx)
	if err != nil {
		return nil, err
	}
	states, err := s.listModelStates(ctx)
	if err != nil {
		return nil, err
	}

	result := make([]contracts.ChannelStatus, 0, len(channels))
	for _, channel := range channels {
		item := contracts.ChannelStatus{
			ID:            channel.ID,
			Name:          channel.Name,
			ChannelName:   channel.ChannelName,
			BaseURL:       channel.BaseURL,
			ManualEnabled: channel.Manual,
			SyncBilling:   channel.SyncBilling,
			Health:        availabilityOf(channel.Manual, channel.Status, channel.Until.String, "channel"),
		}
		for _, model := range catalog[channel.ID] {
			row, ok := states[channel.ID][model.Model]
			manual := true
			status := statusAvailable
			until := ""
			var failCount int
			var lastError string
			var lastSuccess, disabledUntil sql.NullString
			if ok {
				manual, status, until = row.Manual, row.Status, row.Until.String
				failCount, lastError = row.FailCount, row.LastError
				lastSuccess, disabledUntil = row.LastSuccess, row.Until
			}
			availability := availabilityOf(manual, status, until, "model")
			modelStatus := contracts.ModelStatus{
				Model:         model.Model,
				ManualEnabled: availability.ManualEnabled,
				Health:        availability,
				LastError:     lastError,
				FailCount:     failCount,
				Source:        model.Source,
			}
			if lastSuccess.Valid {
				if parsed, err := time.Parse(time.RFC3339Nano, lastSuccess.String); err == nil {
					modelStatus.LastSuccessAt = &parsed
				}
			}
			if disabledUntil.Valid {
				if parsed, err := time.Parse(time.RFC3339Nano, disabledUntil.String); err == nil {
					modelStatus.DisabledUntil = &parsed
				}
			}
			item.Models = append(item.Models, modelStatus)
		}
		result = append(result, item)
	}
	return result, nil
}

func (s *Service) listChannelStatus(ctx context.Context) ([]channelStatusRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.id, c.name, c.channel_name, c.base_url, c.manual_enabled, c.sync_billing, COALESCE(cs.status, 'available'), cs.disabled_until FROM channels c LEFT JOIN channel_states cs ON cs.channel_id = c.id ORDER BY c.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []channelStatusRow
	for rows.Next() {
		var row channelStatusRow
		if err := rows.Scan(&row.ID, &row.Name, &row.ChannelName, &row.BaseURL, &row.Manual, &row.SyncBilling, &row.Status, &row.Until); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// catalogEntry 渠道模型目录项，带来源（probe=自动探测，manual=手动添加）。
type catalogEntry struct {
	Model  string
	Source string
}

func (s *Service) listModelCatalog(ctx context.Context) (map[string][]catalogEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT channel_id, model, MAX(source) AS source FROM (
			-- 渠道目录里启用（对外暴露）的模型：模型状态以「模型渠道」为准。
			SELECT channel_id, model, source FROM channel_models WHERE enabled = 1
			UNION ALL
			-- 仅当渠道从未探测过（channel_models 整个为空）时，历史状态才有展示意义；
			-- 已探测渠道的目录外状态由编辑/CheckNow 清理，不得混进模型状态。
			SELECT ms.channel_id, ms.model, '' AS source
			FROM model_states ms
			WHERE NOT EXISTS (
				SELECT 1 FROM channel_models cm WHERE cm.channel_id = ms.channel_id
			)
		) GROUP BY channel_id, model ORDER BY channel_id, model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	catalog := map[string][]catalogEntry{}
	for rows.Next() {
		var channelID, model, source string
		if err := rows.Scan(&channelID, &model, &source); err != nil {
			return nil, err
		}
		catalog[channelID] = append(catalog[channelID], catalogEntry{Model: model, Source: source})
	}
	return catalog, rows.Err()
}

// PurgeChannelStates 删除某渠道下不在 keep 清单内的模型状态记录。
// 用于编辑渠道（全量替换模型清单）后清理「幽灵状态」，保证模型状态
// 与模型渠道严格一致；keep 为空 = 清空该渠道全部模型状态。
// 实现：单条批量 DELETE（NOT IN），避免逐条删除时的多次事务提交开销。
func (s *Service) PurgeChannelStates(ctx context.Context, channelID string, keep []string) error {
	if len(keep) == 0 {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM model_states WHERE channel_id = ?`, channelID); err != nil {
			return fmt.Errorf("model-health: purge all states of channel %s: %w", channelID, err)
		}
		return nil
	}
	// 单条 NOT IN 批量删；keep 规模远超 SQLite 参数上限（默认 32766）时
	// 才需要分片，渠道模型数通常远小于此，直接一次执行。
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(keep)), ",")
	args := make([]any, 0, len(keep)+1)
	args = append(args, channelID)
	for _, m := range keep {
		args = append(args, m)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM model_states WHERE channel_id = ? AND model NOT IN (%s)`, placeholders), args...); err != nil {
		return fmt.Errorf("model-health: purge states of channel %s: %w", channelID, err)
	}
	return nil
}

func (s *Service) listModelStates(ctx context.Context) (map[string]map[string]modelStateRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT channel_id, model, manual_enabled, status, disabled_until, fail_count, last_error, last_success_at FROM model_states`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := map[string]map[string]modelStateRow{}
	for rows.Next() {
		var channelID, model string
		var row modelStateRow
		if err := rows.Scan(&channelID, &model, &row.Manual, &row.Status, &row.Until, &row.FailCount, &row.LastError, &row.LastSuccess); err != nil {
			return nil, err
		}
		if states[channelID] == nil {
			states[channelID] = map[string]modelStateRow{}
		}
		states[channelID][model] = row
	}
	return states, rows.Err()
}

// availabilityOf 计算渠道/模型的可用性视图，分支语义与 Check 完全一致：
// 手动关闭、自动 disabled/cooling 分别给出不同 reason；cooling 过期视为已恢复。
func availabilityOf(manual bool, status, until, subject string) contracts.Availability {
	if !manual {
		return unavailable(false, status, subject+" manually disabled")
	}
	if !usableStatus(status, until) {
		return unavailable(true, status, subject+" "+status)
	}
	return contracts.Availability{ManualEnabled: true, HealthStatus: statusAvailable, EffectiveAvailable: true}
}

var _ contracts.ModelHealth = (*Service)(nil)
