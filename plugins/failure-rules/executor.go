package failurerules

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Executor 把裁决写入 model_states / channel_states。
//
// 恢复语义（与 CheckNow 对齐）：
//   - recover=never  → status='disabled'（无自动恢复）
//   - recover=daily  → status='cooling' + disabled_until=次日 daily_reset_hour 点
//   - recover=fixed  → status='cooling' + disabled_until=now+cooldown_seconds
type Executor struct {
	db *sql.DB
	lg *slog.Logger
}

// NewExecutor 创建执行器。
func NewExecutor(database *sql.DB, lg *slog.Logger) *Executor {
	if lg == nil {
		lg = slog.Default()
	}
	return &Executor{db: database, lg: lg}
}

// ActionContext 执行动作需要的上下文信息。
type ActionContext struct {
	Evidence Evidence
	Decision Decision
	// Action 规则动作（AI 裁决时由 engine 从 reason 透传 recover/cooldown）。
	Action Action
}

// Execute 执行裁决。返回是否已写状态（false = ignore/retry_same/switch_next）。
func (x *Executor) Execute(ctx context.Context, ac ActionContext) (bool, error) {
	// 写库不受客户端取消影响（状态必须落库，路由决策依赖它）。
	ctx = context.WithoutCancel(ctx)
	switch ac.Decision.Verdict {
	case VerdictIgnore:
		return false, nil
	case VerdictRetrySame, VerdictSwitchNext:
		return false, nil
	case VerdictCooldown:
		return true, x.applyCooldown(ctx, ac)
	case VerdictDisableKey:
		return true, x.applyDisableKey(ctx, ac)
	case VerdictDisableModel:
		return true, x.applyDisableModel(ctx, ac)
	case VerdictDisableProvider:
		return true, x.applyDisableProvider(ctx, ac)
	default:
		return false, fmt.Errorf("failure-rules: unknown verdict %q", ac.Decision.Verdict)
	}
}

// nextRecovery 计算恢复时间点。never 返回 (zero, false)。
func nextRecovery(a Action, now time.Time) (time.Time, bool) {
	switch a.Recover {
	case "never":
		return time.Time{}, false
	case "daily":
		hour := a.DailyResetHour
		if hour == 0 {
			hour = 12
		}
		next := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, time.Local)
		if !next.After(now) {
			next = next.AddDate(0, 0, 1)
		}
		return next, true
	default: // fixed
		secs := a.CooldownSeconds
		if secs <= 0 {
			secs = 120
		}
		return now.Add(time.Duration(secs) * time.Second), true
	}
}

// stateClass 生成写入 last_failure_class 的语义化分类：rule_<verdict>_<recover>。
// 前端据此区分「永久禁用 / 次日恢复 / 定时冷却」，而不是把不同成因一律
// 显示成「冷却中」。recover 为空时按执行层实际默认值 fixed 处理。
func stateClass(verdict string, a Action) string {
	recover := a.Recover
	if recover == "" {
		recover = "fixed"
	}
	return "rule_" + verdict + "_" + recover
}

// applyCooldown / applyDisableKey：时间限定禁用统一写 cooling + until。
func (x *Executor) applyCooldown(ctx context.Context, ac ActionContext) error {
	a := ac.Action
	// 限速升级：连续失败达阈值升级恢复策略（fail_count 由 model_states 累加）。
	if a.FailUpgradeCount > 0 && ac.Evidence.FailCount+1 >= a.FailUpgradeCount && a.FailUpgradeRecover != "" {
		upgrade := a
		upgrade.Recover = a.FailUpgradeRecover
		if upgrade.Recover == "daily" && upgrade.DailyResetHour == 0 {
			upgrade.DailyResetHour = 12
		}
		x.lg.Info("failure-rules: 连续失败升级恢复策略",
			"model", ac.Evidence.Model, "channel", ac.Evidence.ChannelID,
			"fail_count", ac.Evidence.FailCount+1, "upgrade", upgrade.Recover)
		a = upgrade
	}
	return x.writeModelState(ctx, ac, a, stateClass("cooldown", a))
}

func (x *Executor) applyDisableKey(ctx context.Context, ac ActionContext) error {
	if err := x.writeModelState(ctx, ac, ac.Action, stateClass("disable_key", ac.Action)); err != nil {
		return err
	}
	// 连坐：recover=never + switch_account → 整个 key（channel_states）禁用。
	if ac.Action.SwitchAccount && ac.Action.Recover == "never" {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		_, err := x.db.ExecContext(ctx, `
			INSERT INTO channel_states(channel_id, status, fail_count, last_error, last_failure_class, updated_at)
			VALUES (?, 'disabled', 1, ?, 'rule_disable', ?)
			ON CONFLICT(channel_id) DO UPDATE SET status='disabled', disabled_until=NULL,
			       fail_count=channel_states.fail_count+1, last_error=excluded.last_error,
			       last_failure_class=excluded.last_failure_class, updated_at=excluded.updated_at`,
			ac.Evidence.ChannelID, truncate(ac.Evidence.Message, 500), now)
		if err != nil {
			return err
		}
		x.lg.Info("failure-rules: 渠道连坐禁用（auth 永久）", "channel", ac.Evidence.ChannelID)
	}
	return nil
}

func (x *Executor) applyDisableModel(ctx context.Context, ac ActionContext) error {
	// 该模型在当前渠道上禁用（disable_model 语义 = key 级模型禁用，
	// 与 legacy auth/model_quota 行为一致；跨渠道全禁由 disable_provider 承担）。
	now := time.Now().UTC().Format(time.RFC3339Nano)
	until, timed := nextRecovery(ac.Action, time.Now())
	status := "disabled"
	var untilAny any
	if timed {
		status = "cooling"
		untilAny = until.UTC().Format(time.RFC3339Nano)
	}
	_, err := x.db.ExecContext(ctx, `
		INSERT INTO model_states(channel_id, model, manual_enabled, status, disabled_until, fail_count,
		       last_error, last_failure_class, updated_at)
		VALUES (?, ?, 1, ?, ?, 1, ?, ?, ?)
		ON CONFLICT(channel_id, model) DO UPDATE SET
		       status=excluded.status, disabled_until=excluded.disabled_until,
		       fail_count=model_states.fail_count+1, last_error=excluded.last_error,
		       last_failure_class=excluded.last_failure_class, updated_at=excluded.updated_at`,
		ac.Evidence.ChannelID, ac.Evidence.Model, status, untilAny,
		truncate(ac.Evidence.Message, 500), stateClass("disable_model", ac.Action), now)
	return err
}

func (x *Executor) applyDisableProvider(ctx context.Context, ac ActionContext) error {
	// 整个渠道组（同 base_url）禁用。
	now := time.Now().UTC().Format(time.RFC3339Nano)
	ids, err := x.channelIDsByBaseURL(ctx, ac.Evidence.ProviderURL)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := x.db.ExecContext(ctx, `
			INSERT INTO channel_states(channel_id, status, fail_count, last_error, last_failure_class, updated_at)
			VALUES (?, 'disabled', 1, ?, 'rule_disable_provider', ?)
			ON CONFLICT(channel_id) DO UPDATE SET status='disabled', disabled_until=NULL,
			       fail_count=channel_states.fail_count+1, last_error=excluded.last_error,
			       last_failure_class=excluded.last_failure_class, updated_at=excluded.updated_at`,
			id, truncate(ac.Evidence.Message, 500), now); err != nil {
			return err
		}
	}
	x.lg.Info("failure-rules: 平台组禁用", "base_url", ac.Evidence.ProviderURL, "channels", len(ids))
	return nil
}

// writeModelState 写单个 key（channel+model）的状态。
func (x *Executor) writeModelState(ctx context.Context, ac ActionContext, a Action, class string) error {
	now := time.Now().UTC()
	until, timed := nextRecovery(a, now)
	status := "disabled"
	var untilAny any
	if timed {
		status = "cooling"
		untilAny = until.UTC().Format(time.RFC3339Nano)
	}
	_, err := x.db.ExecContext(ctx, `
		INSERT INTO model_states(channel_id, model, manual_enabled, status, disabled_until, fail_count,
		       last_error, last_failure_class, updated_at)
		VALUES (?, ?, 1, ?, ?, 1, ?, ?, ?)
		ON CONFLICT(channel_id, model) DO UPDATE SET
		       status=excluded.status, disabled_until=excluded.disabled_until,
		       fail_count=model_states.fail_count+1, last_error=excluded.last_error,
		       last_failure_class=excluded.last_failure_class, updated_at=excluded.updated_at`,
		ac.Evidence.ChannelID, ac.Evidence.Model, status, untilAny,
		truncate(ac.Evidence.Message, 500), class, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	x.lg.Info("failure-rules: 状态已写入",
		"model", ac.Evidence.Model, "channel", ac.Evidence.ChannelID,
		"verdict", ac.Decision.Verdict, "status", status, "until", untilAny,
		"rule", ac.Decision.MatchedRuleName)
	return nil
}

// channelIDsByBaseURL 渠道组身份映射：base_url → channel ids。
func (x *Executor) channelIDsByBaseURL(ctx context.Context, baseURL string) ([]string, error) {
	if baseURL == "" {
		return nil, nil
	}
	rows, err := x.db.QueryContext(ctx, `SELECT id FROM channels WHERE base_url = ?`, baseURL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ProviderURLFor 渠道 base_url 查询（engine 调用方组装 Evidence 用）。
func (x *Executor) ProviderURLFor(ctx context.Context, channelID string) string {
	if channelID == "" {
		return ""
	}
	var u string
	if err := x.db.QueryRowContext(ctx, `SELECT COALESCE(base_url,'') FROM channels WHERE id=?`, channelID).Scan(&u); err != nil {
		return ""
	}
	return strings.TrimRight(u, "/")
}
