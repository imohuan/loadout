package failurerules

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// compiledCond 编译后的单条条件：需要正则的条件在这里把正则预编译好，
// 避免每次求值重复编译。
type compiledCond struct {
	cond  Condition
	regex *regexp.Regexp // 该条件声明了正则时非 nil；非法正则保持 nil（判不命中）
}

// compiled 缓存编译后的规则。
type compiled struct {
	rule Rule
	any  []compiledCond
	all  []compiledCond
}

// Engine 规则引擎：加载启用且已确认的规则，按优先级匹配失败证据。
type Engine struct {
	db *sql.DB
	lg *slog.Logger

	mu    sync.RWMutex
	cache []compiled

	// AI 兜底：nil = 未配置（无规则命中时走默认动作）。
	ai *AIResolver

	// 同一错误指纹的 AI 判定在飞去重（singleflight 语义）。
	inflight sync.Map // fingerprint -> *sync.WaitGroup 风格的 channel

	// onAIDecided 异步 AI 判定完成后的回调（调用方用它落草稿规则 / 记日志）。
	// 不设则只写缓存，不影响正确性。
	onAIDecided func(Evidence, Decision)
}

// NewEngine 创建引擎。database 为 loadout.db（failure_rules 所在库）。
func NewEngine(database *sql.DB, lg *slog.Logger) *Engine {
	if lg == nil {
		lg = slog.Default()
	}
	e := &Engine{db: database, lg: lg}
	e.Reload(context.Background())
	return e
}

// SetAIResolver 注入 AI 兜底解析器（可后置配置）。
func (e *Engine) SetAIResolver(ai *AIResolver) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ai = ai
}

// SetOnAIDecided 注册「异步 AI 判定完成」回调（只注册一次，装配期调用）。
func (e *Engine) SetOnAIDecided(fn func(Evidence, Decision)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.onAIDecided = fn
}

// Reload 重新加载启用且已确认的规则（按 priority 升序）。
func (e *Engine) Reload(ctx context.Context) {
	rows, err := e.db.QueryContext(ctx, `
		SELECT id, name, enabled, source, confirmed, priority, provider_base_url, model,
		       match_json, action_json, hit_count, COALESCE(last_hit_at,''), created_at, updated_at
		FROM failure_rules WHERE enabled = 1 AND confirmed = 1
		ORDER BY priority ASC, rowid ASC`)
	if err != nil {
		e.lg.Warn("failure-rules: 加载规则失败", "err", err)
		return
	}
	defer rows.Close()

	var out []compiled
	for rows.Next() {
		var r Rule
		var enabled, confirmed int
		var matchJSON, actionJSON string
		if err := rows.Scan(&r.ID, &r.Name, &enabled, &r.Source, &confirmed, &r.Priority,
			&r.ProviderBaseURL, &r.Model, &matchJSON, &actionJSON, &r.HitCount, &r.LastHitAt,
			&r.CreatedAt, &r.UpdatedAt); err != nil {
			e.lg.Warn("failure-rules: 读取规则行失败", "err", err)
			continue
		}
		r.Enabled = enabled == 1
		r.Confirmed = confirmed == 1
		if err := unmarshalStrictish(matchJSON, &r.Match); err != nil {
			e.lg.Warn("failure-rules: 规则 match_json 解析失败", "id", r.ID, "err", err)
			continue
		}
		if err := unmarshalStrictish(actionJSON, &r.Action); err != nil {
			e.lg.Warn("failure-rules: 规则 action_json 解析失败", "id", r.ID, "err", err)
			continue
		}
		c := compiled{rule: r, any: compileConditions(r.Match.Any), all: compileConditions(r.Match.All)}
		out = append(out, c)
	}
	e.mu.Lock()
	e.cache = out
	e.mu.Unlock()
	e.lg.Info("failure-rules: 规则已加载", "count", len(out))
}

// Evaluate 对一次失败证据求值：按优先级匹配规则，未命中走 AI 兜底（若配置），
// 再未命中返回默认动作（cooldown 120s fixed）。
func (e *Engine) Evaluate(ctx context.Context, ev Evidence) Decision {
	e.mu.RLock()
	cache := e.cache
	ai := e.ai
	e.mu.RUnlock()

	for _, c := range cache {
		if !c.rule.scopeMatches(ev) {
			continue
		}
		if matchConditions(c, ev) {
			e.recordHit(c.rule.ID)
			return Decision{MatchedRuleID: c.rule.ID, MatchedRuleName: c.rule.Name, Verdict: c.rule.Action.Verdict, Action: c.rule.Action}
		}
	}

	// AI 兜底（配置了模型才启用）。
	//
	// 防递归：AI 请求带 X-Loadout-Rule-AI 标记，model-gateway 见到该标记就
	// **不**把这次失败写回规则引擎（见 model-gateway.isRuleAIRequest）。
	// 早期这里注释写「由调用方保证」但没有任何地方真的读那个 header，属于
	// 注释与实现不符；现在标记真正生效。
	if ai != nil {
		d, ok := e.evaluateAIOnce(ctx, ai, ev)
		if ok {
			return d
		}
	}
	return Decision{Verdict: VerdictCooldown, Reason: "no_rule_matched_default"}
}

// evaluateAIOnce 同指纹去重后的 AI 判定。
func (e *Engine) evaluateAIOnce(ctx context.Context, ai *AIResolver, ev Evidence) (Decision, bool) {
	fp := fingerprint(ev)
	// 已有同指纹的 AI 判定 → 立即复用（这是「第二次遇到同样问题就走对」的关键）。
	if d, ok := ai.cached(fp); ok {
		return d, true
	}
	// 未命中缓存：**异步**发起判定，本次请求不等它。
	//
	// 为什么不等：实测推理型模型返回完整裁决要 9~40s，而这条路径在用户请求的
	// 失败链路上同步执行——等下去就是让用户的请求白挂十几秒。这里立即返回 false
	// （调用方走默认动作，保证本次路由正确），AI 结果写进缓存与草稿规则，
	// 同指纹的下一次失败即可秒用。
	if _, loaded := e.inflight.LoadOrStore(fp, struct{}{}); loaded {
		// 同指纹已有判定在飞：不重复调用，也不等待。
		return Decision{}, false
	}
	go func() {
		defer e.inflight.Delete(fp)
		// 请求上下文可能在用户断连后取消，但判定结果仍然有价值 → 剥掉取消。
		bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), defaultAITimeout+10*time.Second)
		defer cancel()
		d, ok := ai.Resolve(bg, ev, fp)
		if !ok {
			e.lg.Warn("failure-rules: AI 兜底判定失败（超时/鉴权/解析），下次同指纹仍走默认动作",
				"model", ev.Model, "status", ev.StatusCode, "body_code", ev.BodyCode)
			return
		}
		e.mu.RLock()
		cb := e.onAIDecided
		e.mu.RUnlock()
		if cb != nil {
			// 回调里落草稿规则（异步，不阻塞任何请求）。
			cb(ev, d)
		}
		e.lg.Info("failure-rules: AI 兜底判定完成（已缓存，下次同指纹直接复用）",
			"model", ev.Model, "verdict", d.Verdict, "ai_model", d.AIModel)
	}()
	return Decision{}, false
}

func (e *Engine) recordHit(ruleID string) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := e.db.ExecContext(context.WithoutCancel(context.Background()),
		`UPDATE failure_rules SET hit_count = hit_count + 1, last_hit_at = ?, updated_at = ? WHERE id = ?`,
		now, now, ruleID)
	if err != nil {
		e.lg.Warn("failure-rules: 更新命中统计失败", "id", ruleID, "err", err)
	}
}

// matchConditions 求值一条编译规则的 any/all 条件。
func matchConditions(c compiled, ev Evidence) bool {
	if len(c.any) > 0 {
		for _, cc := range c.any {
			if evalCondition(cc, ev) {
				return true
			}
		}
		return false
	}
	if len(c.all) > 0 {
		for _, cc := range c.all {
			if !evalCondition(cc, ev) {
				return false
			}
		}
		return true
	}
	return false
}

// compileRule 把一条规则的两组条件预编译成求值用的形态。
func compileRule(rule Rule) compiled {
	return compiled{rule: rule, any: compileConditions(rule.Match.Any), all: compileConditions(rule.Match.All)}
}

// compileConditions 逐条编译条件。
//
// 关键：正则必须**按条件各自编译**，不能整条规则共用一份。
// 早期版本 compileRegex 只返回规则里遇到的第一个正则，再拿它去判所有正则条件，
// 于是「any 里第二条正则才命中」被算成不命中、「all 里第二条本该不命中」被算成
// 命中——一条规则里写两个正则结果就全乱（用户实测反馈过）。
func compileConditions(conds []Condition) []compiledCond {
	if len(conds) == 0 {
		return nil
	}
	out := make([]compiledCond, 0, len(conds))
	for _, cond := range conds {
		cc := compiledCond{cond: cond}
		if pattern, ok := regexPattern(cond); ok {
			// 非法正则留 nil：evalCondition 见到 nil 直接判不命中，不 panic。
			if re, err := compileMessageRegex(pattern); err == nil {
				cc.regex = re
			}
		}
		out = append(out, cc)
	}
	return out
}

// compileMessageRegex 编译「错误文案」用的正则。
//
// 统一在这里加 (?i)：忽略大小写，与「包含」的口径保持一致——
// 上游错误文案大小写很随意，两种操作对同样字面量必须给出同结论。
// 校验（validateCondition）和运行期都必须走这个函数，否则会出现
// 「保存校验通过、运行时却说非法」的错位。
func compileMessageRegex(pattern string) (*regexp.Regexp, error) {
	return regexp.Compile("(?i)" + pattern)
}

// regexPattern 判断一条条件是否要按正则求值，并取出模式串。
//
// 两个入口都认：
//   - field=message_regex（前端「正则」字段）；
//   - op=regex（前端「正则」操作）。
//
// 早前后者被忽略，用户在界面上选了「正则」操作却完全不生效。
//
// 正则只作用于错误文案：状态码/业务码是数字，配「正则」没有意义，
// 直接判不匹配（而不是拿数字去撞文案正则）。
func regexPattern(cond Condition) (string, bool) {
	if cond.Field != "message_regex" && cond.Op != "regex" {
		return "", false
	}
	if cond.Field == "status_code" || cond.Field == "body_code" {
		return "", false
	}
	pattern := toString(cond.Value)
	if pattern == "" {
		return "", false
	}
	return pattern, true
}

func evalCondition(cc compiledCond, ev Evidence) bool {
	cond := cc.cond
	// 需要正则的条件统一走 cc.regex（按条件各自编译好的那一份）。
	if _, ok := regexPattern(cond); ok {
		if cc.regex == nil {
			return false // 非法/空正则
		}
		return cc.regex.MatchString(ev.Message)
	}
	switch cond.Field {
	case "status_code":
		n, ok := toInt(cond.Value)
		return ok && n == ev.StatusCode
	case "body_code":
		return toString(cond.Value) != "" && toString(cond.Value) == ev.BodyCode
	case "message_text":
		needle := strings.ToLower(toString(cond.Value))
		hay := strings.ToLower(ev.Message)
		switch cond.Op {
		case "eq":
			return needle != "" && hay == needle
		case "contains":
			return needle != "" && strings.Contains(hay, needle)
		case "not_contains":
			return needle == "" || !strings.Contains(hay, needle)
		default:
			return false
		}
	default:
		return false
	}
}

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return fmt.Sprintf("%v", int64(t))
	case int:
		return fmt.Sprintf("%v", t)
	default:
		return ""
	}
}

func toInt(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case int:
		return t, true
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		if err != nil {
			return 0, false
		}
		return int(n), true
	default:
		return 0, false
	}
}

func unmarshalStrictish(data string, v any) error {
	return jsonUnmarshal(data, v)
}

// fingerprint 错误指纹：同指纹的 AI 判定去重/缓存。
func fingerprint(ev Evidence) string {
	return fmt.Sprintf("%d|%s|%s", ev.StatusCode, ev.BodyCode, hashString(ev.Message))
}

// VerifyRule 单条规则对样本证据做 dry-run（UI 校验用，不写命中统计）。
// Count 当前加载的规则数（调试用）。
func (e *Engine) Count() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.cache)
}

func (e *Engine) VerifyRule(rule Rule, ev Evidence) bool {
	if !rule.scopeMatches(ev) {
		return false
	}
	c := compileRule(rule)
	return matchConditions(c, ev)
}

// VerifyRuleMatchOnly 只校验「匹配条件」本身，跳过作用域（scope/model）判断。
//
// 专给编辑弹窗的 dry-run 用：用户在那里填的是「状态码 + 业务码 + 错误文案」，
// 本来就没有（也不该要求填）provider / model —— 规则的作用域是保存时才确定的
// 平台范围。若在这里也跑 scopeMatches，锁了平台的规则会因样本 ProviderURL 为空
// 而永远返回 false，用户填了完全正确的预测也显示「未命中」（实测踩过）。
func (e *Engine) VerifyRuleMatchOnly(rule Rule, ev Evidence) bool {
	c := compileRule(rule)
	return matchConditions(c, ev)
}
