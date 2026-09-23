package failurerules

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// beijingTZ 北京时间固定 +8 时区（中国无夏令时；FixedZone 零依赖，
// Windows 上不依赖系统 tzdata）。与 volc-free-quota 的同名时区口径一致。
var beijingTZ = time.FixedZone("Asia/Shanghai", 8*3600)

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
	return nextRecoveryWithEvidence(a, Evidence{}, nil, now)
}

// nextRecoveryWithEvidence 同 nextRecovery，但支持捕获模板。
//
// captures 是正则条件命中的捕获组；动作的 CooldownSecondsTemplate /
// RecoverAtTemplate 里可以用 $1/$2… 引用（通用机制，不限于时间）。
//   - RecoverAtTemplate：展开结果解析成时间成功 → 恢复点 = 该时刻；
//   - CooldownSecondsTemplate：展开结果是数字 → 冷却秒数 = 该值；
//   - 都没配 / 展开失败 → 回退静态字段（cooldown_seconds，缺省 120s）。
//
// daily 的「明天刷新」语义保持不变。
func nextRecoveryWithEvidence(a Action, ev Evidence, captures []string, now time.Time) (time.Time, bool) {
	switch a.Recover {
	case "never":
		return time.Time{}, false
	case "daily":
		hour := a.DailyResetHour
		if hour == 0 {
			hour = 12
		}
		// 「每日额度」的语义是：今天用尽后今天不再可用，要等明天刷新。
		// 恢复点必须是「明天」的刷新时刻（理由见历史注释）；用固定北京时间，
		// 不依赖机器时区（中国无夏令时，FixedZone 零依赖）。
		local := now.In(beijingTZ)
		return time.Date(local.Year(), local.Month(), local.Day()+1, hour, 0, 0, 0, beijingTZ), true
	default: // fixed
		// 1) 恢复时刻模板：展开结果解析成时间即用。
		if a.RecoverAtTemplate != "" {
			if until, ok := parseTimeFlex(expandTemplate(a.RecoverAtTemplate, captures), now); ok {
				return until, true
			}
		}
		// 2) 冷却秒数模板：展开结果必须是正数。
		if a.CooldownSecondsTemplate != "" {
			if n, ok := toInt(expandTemplate(a.CooldownSecondsTemplate, captures)); ok && n > 0 {
				return now.Add(time.Duration(n) * time.Second), true
			}
		}
		// 3) 回退静态冷却。
		secs := a.CooldownSeconds
		if secs <= 0 {
			secs = 120
		}
		return now.Add(time.Duration(secs) * time.Second), true
	}
}

// actionParamsText 生成动作参数的人类可读摘要（回放/样本校验展示用）。
func actionParamsText(verdict, until string, cooldownSeconds int) string {
	return actionParamsTextR(verdict, until, cooldownSeconds, "")
}

// actionParamsTextR 带 recover 的版本：never 要说清「不会自动恢复」。
//
// 早期只按 verdict 判断，disable_key + recover=never（永久禁用）也会显示成
// 「定时恢复」，误导用户以为会自动恢复（实测反馈）。
func actionParamsTextR(verdict, until string, cooldownSeconds int, recover string) string {
	switch verdict {
	case VerdictCooldown, VerdictDisableKey, VerdictDisableModel, VerdictDisableProvider:
		switch recover {
		case "never":
			return "永久禁用（需手动恢复）"
		case "daily":
			if until != "" {
				return "次日恢复 " + until
			}
			return "次日恢复"
		}
		if until != "" {
			return "恢复时间 " + until
		}
		if cooldownSeconds > 0 {
			return fmt.Sprintf("冷却 %d 秒", cooldownSeconds)
		}
		return "定时恢复"
	default:
		return ""
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

// ruleAttribution 从裁决里取出「是哪条规则把它判掉的」，写入状态表供 UI 溯源：
// 模型状态页据此展示命中规则，并可一键跳到规则页改那条规则。
// AI 兜底没有具体规则（草稿是异步补生成的），只给一个说明性名称。
func ruleAttribution(d Decision) (string, string) {
	if d.MatchedRuleID == "" {
		if d.AIModel != "" {
			return "", "AI 兜底判定"
		}
		return "", ""
	}
	name := d.MatchedRuleName
	if name == "" {
		name = d.MatchedRuleID
	}
	return d.MatchedRuleID, name
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
	// disable_key 语义 = 这条 Key（账号）整体不可用，不只是当前这一次请求的模型。
	// 「额度用尽/余额不足/密钥失效」都是账号级的：只禁单个模型会导致同一个额度
	// 耗尽的账号在下一个模型上继续被选中，反复失败（用户反馈的「死掉的模型反复鞭尸」）。
	// 因此这里必须同时写 channel_states，让路由整体跳过这条 Key。
	class := stateClass("disable_key", ac.Action)
	if err := x.writeModelState(ctx, ac, ac.Action, class); err != nil {
		return err
	}
	return x.writeChannelState(ctx, ac, ac.Action, class)
}

// writeChannelState 写整条 Key（channel_states）的状态。
//
// 恢复语义与 model_states 对齐（CheckNow 同时清理两者）：
//   - recover=never  → status='disabled'（需手动恢复）
//   - recover=daily  → status='cooling' + disabled_until=次日刷新点
//   - recover=fixed  → status='cooling' + disabled_until=now+cooldown
//
// 历史实现只在 switch_account（auth 永久）时才写渠道状态，且固定写 'disabled'，
// 于是「每日额度用尽」既没禁 Key，也没法到期自动恢复。
func (x *Executor) writeChannelState(ctx context.Context, ac ActionContext, a Action, class string) error {
	now := time.Now().UTC()
	until, timed := nextRecoveryWithEvidence(a, ac.Evidence, ac.Decision.Captures, now)
	status := "disabled"
	var untilAny any
	if timed {
		status = "cooling"
		untilAny = until.UTC().Format(time.RFC3339Nano)
	}
	ruleID, ruleName := ruleAttribution(ac.Decision)
	_, err := x.db.ExecContext(ctx, `
		INSERT INTO channel_states(channel_id, status, disabled_until, fail_count, last_error,
		       last_failure_class, last_rule_id, last_rule_name, updated_at)
		VALUES (?, ?, ?, 1, ?, ?, ?, ?, ?)
		ON CONFLICT(channel_id) DO UPDATE SET
		       status=excluded.status, disabled_until=excluded.disabled_until,
		       fail_count=channel_states.fail_count+1, last_error=excluded.last_error,
		       last_failure_class=excluded.last_failure_class,
		       last_rule_id=excluded.last_rule_id, last_rule_name=excluded.last_rule_name,
		       updated_at=excluded.updated_at`,
		ac.Evidence.ChannelID, status, untilAny, truncate(ac.Evidence.Message, 500), class,
		ruleID, ruleName, now.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	x.lg.Info("failure-rules: Key 状态已写入",
		"channel", ac.Evidence.ChannelID, "verdict", ac.Decision.Verdict,
		"status", status, "until", untilAny, "rule", ac.Decision.MatchedRuleName)
	return nil
}

func (x *Executor) applyDisableModel(ctx context.Context, ac ActionContext) error {
	// 该模型在当前渠道上禁用（disable_model 语义 = key 级模型禁用，
	// 与 legacy auth/model_quota 行为一致；跨渠道全禁由 disable_provider 承担）。
	now := time.Now().UTC().Format(time.RFC3339Nano)
	until, timed := nextRecoveryWithEvidence(ac.Action, ac.Evidence, ac.Decision.Captures, time.Now())
	status := "disabled"
	var untilAny any
	if timed {
		status = "cooling"
		untilAny = until.UTC().Format(time.RFC3339Nano)
	}
	ruleID, ruleName := ruleAttribution(ac.Decision)
	_, err := x.db.ExecContext(ctx, `
		INSERT INTO model_states(channel_id, model, manual_enabled, status, disabled_until, fail_count,
		       last_error, last_failure_class, last_rule_id, last_rule_name, updated_at)
		VALUES (?, ?, 1, ?, ?, 1, ?, ?, ?, ?, ?)
		ON CONFLICT(channel_id, model) DO UPDATE SET
		       status=excluded.status, disabled_until=excluded.disabled_until,
		       fail_count=model_states.fail_count+1, last_error=excluded.last_error,
		       last_failure_class=excluded.last_failure_class,
		       last_rule_id=excluded.last_rule_id, last_rule_name=excluded.last_rule_name,
		       updated_at=excluded.updated_at`,
		ac.Evidence.ChannelID, ac.Evidence.Model, status, untilAny,
		truncate(ac.Evidence.Message, 500), stateClass("disable_model", ac.Action), ruleID, ruleName, now)
	return err
}

func (x *Executor) applyDisableProvider(ctx context.Context, ac ActionContext) error {
	// 整个渠道组（同 base_url）禁用。
	now := time.Now().UTC().Format(time.RFC3339Nano)
	ids, err := x.channelIDsByBaseURL(ctx, ac.Evidence.ProviderURL)
	if err != nil {
		return err
	}
	ruleID, ruleName := ruleAttribution(ac.Decision)
	for _, id := range ids {
		if _, err := x.db.ExecContext(ctx, `
			INSERT INTO channel_states(channel_id, status, fail_count, last_error, last_failure_class,
			       last_rule_id, last_rule_name, updated_at)
			VALUES (?, 'disabled', 1, ?, 'rule_disable_provider', ?, ?, ?)
			ON CONFLICT(channel_id) DO UPDATE SET status='disabled', disabled_until=NULL,
			       fail_count=channel_states.fail_count+1, last_error=excluded.last_error,
			       last_failure_class=excluded.last_failure_class,
			       last_rule_id=excluded.last_rule_id, last_rule_name=excluded.last_rule_name,
			       updated_at=excluded.updated_at`,
			id, truncate(ac.Evidence.Message, 500), ruleID, ruleName, now); err != nil {
			return err
		}
	}
	x.lg.Info("failure-rules: 平台组禁用", "base_url", ac.Evidence.ProviderURL, "channels", len(ids))
	return nil
}

// writeModelState 写单个 key（channel+model）的状态。
func (x *Executor) writeModelState(ctx context.Context, ac ActionContext, a Action, class string) error {
	now := time.Now().UTC()
	until, timed := nextRecoveryWithEvidence(a, ac.Evidence, ac.Decision.Captures, now)
	status := "disabled"
	var untilAny any
	if timed {
		status = "cooling"
		untilAny = until.UTC().Format(time.RFC3339Nano)
	}
	ruleID, ruleName := ruleAttribution(ac.Decision)
	_, err := x.db.ExecContext(ctx, `
		INSERT INTO model_states(channel_id, model, manual_enabled, status, disabled_until, fail_count,
		       last_error, last_failure_class, last_rule_id, last_rule_name, updated_at)
		VALUES (?, ?, 1, ?, ?, 1, ?, ?, ?, ?, ?)
		ON CONFLICT(channel_id, model) DO UPDATE SET
		       status=excluded.status, disabled_until=excluded.disabled_until,
		       fail_count=model_states.fail_count+1, last_error=excluded.last_error,
		       last_failure_class=excluded.last_failure_class,
		       last_rule_id=excluded.last_rule_id, last_rule_name=excluded.last_rule_name,
		       updated_at=excluded.updated_at`,
		ac.Evidence.ChannelID, ac.Evidence.Model, status, untilAny,
		truncate(ac.Evidence.Message, 500), class, ruleID, ruleName, now.UTC().Format(time.RFC3339Nano))
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

// parseTimeFlex 宽松解析时间文本（捕获模板的产物）。
//
// 支持常见格式与时区提示（UTC+8 / GMT+8 / +08:00 / 北京时间 / CST）；
// 无时区提示时按北京时间（+8）。解析失败返回 ok=false。
func parseTimeFlex(text string, now time.Time) (time.Time, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return time.Time{}, false
	}
	lower := strings.ToLower(text)
	offset := 8 * 3600
	for _, h := range []struct {
		hint   []string
		offset int
	}{
		{[]string{"utc+8", "gmt+8", "+08:00", "cst", "北京时间"}, 8 * 3600},
		{[]string{"utc+7", "gmt+7", "+07:00"}, 7 * 3600},
	} {
		for _, hint := range h.hint {
			if strings.Contains(lower, hint) {
				offset = h.offset
				break
			}
		}
	}
	// 纯数字：Unix 时间戳（秒/毫秒）。
	if n, ok := toInt(text); ok && n > 10^12 {
		return time.UnixMilli(int64(n)), true
	}
	if n, ok := toInt(text); ok && n > 10^9 {
		return time.Unix(int64(n), 0), true
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05", "2006/01/02 15:04:05",
		"2006-01-02T15:04:05", "2006-01-02 15:04", "2006/01/02 15:04",
		"2006-01-02", "2006/01/02",
		"15:04:05", "15:04",
	} {
		if t, err := time.ParseInLocation(layout, text, time.FixedZone("hint", offset)); err == nil {
			// 只给时间（15:04:05）→ 拼到今天；若已过去则拼到明天。
			if layout == "15:04:05" || layout == "15:04" {
				local := now.In(beijingTZ)
				t = time.Date(local.Year(), local.Month(), local.Day(), t.Hour(), t.Minute(), t.Second(), 0, beijingTZ)
				if !t.After(now) {
					t = t.AddDate(0, 0, 1)
				}
			}
			if layout == "2006-01-02" || layout == "2006/01/02" {
				t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, beijingTZ)
			}
			return t, true
		}
	}
	return time.Time{}, false
}
