package failurerules

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"regexp"
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
	return nextRecoveryWithEvidence(a, Evidence{}, now)
}

// nextRecoveryWithEvidence 同 nextRecovery，但支持「从错误文案提取恢复时间」。
//
// 规则开启 ExtractRecoverAt 且文案里带可解析的时刻（如「将在 2026-09-23
// 15:48:27 UTC+8 重置」）时，恢复点 = 提取到的时刻；提取不到或时间在过去
// 则回退常规策略，规则照常工作。daily 的「明天刷新」语义保持不变。
func nextRecoveryWithEvidence(a Action, ev Evidence, now time.Time) (time.Time, bool) {
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
		// 开了「从文案提取恢复时间」就先试提取：上游明确说「几点重置」时，
		// 冷却到那个点比拍脑袋的 now+cooldown 准确得多（用户实测反馈）。
		if a.ExtractRecoverAt {
			if until, ok := extractRecoverAt(ev.Message, now); ok {
				return until, true
			}
		}
		secs := a.CooldownSeconds
		if secs <= 0 {
			secs = 120
		}
		return now.Add(time.Duration(secs) * time.Second), true
	}
}

// recoverAtPatterns 上游文案里的时间写法（按平台实测积累）：
//   - 2026-09-23 15:48:27 UTC+8（腾讯 copilot / newapi 系）
//   - 2026-09-23 15:48:27 GMT+8 / +08:00
//   - 2026/09/23 15:48:27、2026-09-23 15:48（秒可省）
var recoverAtPatterns = []*regexp.Regexp{
	regexp.MustCompile(`20\d{2}[-/]\d{2}[-/]\d{2}[ T]\d{1,2}:\d{2}:\d{2}`),
	regexp.MustCompile(`20\d{2}[-/]\d{2}[-/]\d{2}[ T]\d{1,2}:\d{2}`),
}

// recoverAtTZHints 时区写法 → 偏移。文案没写时区时按北京时间（+8）处理：
// 目标上游以中国平台为主，与其依赖机器时区不如固定 +8 可预期。
var recoverAtTZHints = []struct {
	hint   []string
	offset int
}{
	{[]string{"utc+8", "gmt+8", "+08:00", "cst", "北京时间"}, 8 * 3600},
	{[]string{"utc+7", "gmt+7", "+07:00"}, 7 * 3600},
}

// extractRecoverAt 从错误文案中提取「恢复/重置时刻」。
//
// 找不到、解析失败或时间在过去（上游时钟漂移、示例文本）都返回 ok=false，
// 调用方回退常规恢复策略。
func extractRecoverAt(message string, now time.Time) (time.Time, bool) {
	if message == "" {
		return time.Time{}, false
	}
	lower := strings.ToLower(message)
	offset := 8 * 3600
	for _, h := range recoverAtTZHints {
		for _, hint := range h.hint {
			if strings.Contains(lower, hint) {
				offset = h.offset
				break
			}
		}
	}
	for _, re := range recoverAtPatterns {
		m := re.FindString(message)
		if m == "" {
			continue
		}
		for _, layout := range []string{
			"2006-01-02 15:04:05", "2006/01/02 15:04:05",
			"2006-01-02T15:04:05", "2006-01-02 15:04", "2006/01/02 15:04",
		} {
			if t, err := time.ParseInLocation(layout, m, time.FixedZone("hint", offset)); err == nil {
				if !t.After(now) {
					return time.Time{}, false // 过去时间视为无效
				}
				return t, true
			}
		}
	}
	return time.Time{}, false
}

// actionParamsText 生成动作参数的人类可读摘要（回放/样本校验展示用）。
func actionParamsText(verdict, until string, cooldownSeconds int) string {
	switch verdict {
	case VerdictCooldown, VerdictDisableKey, VerdictDisableModel, VerdictDisableProvider:
		if until != "" {
			return "恢复时间 " + until
		}
		if cooldownSeconds > 0 {
			return "冷却 " + fmt.Sprint(cooldownSeconds) + " 秒"
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
	until, timed := nextRecoveryWithEvidence(a, ac.Evidence, now)
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
	until, timed := nextRecoveryWithEvidence(ac.Action, ac.Evidence, time.Now())
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
	until, timed := nextRecoveryWithEvidence(a, ac.Evidence, now)
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
