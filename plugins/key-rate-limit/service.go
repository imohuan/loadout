// Package keyratelimit 实现「单个上游渠道 Key 限流 + 账号轮换」能力（plugins/key-rate-limit）。
//
// 场景：同一个平台上配了多个账号（每个账号一条渠道 Key）。请求默认总是打到渠道表里排最前的
// 那个 Key，容易被上游风控/限速。本插件给每个 Key 加一层滑动窗口配额：窗口内用满 MaxRequests
// 次后，就把该 Key 从候选里剔除，请求自动落到下一个账号；所有账号都用满时按 OnExhausted
// 返回 429（默认）或忽略限流继续使用。
//
// 复用能力路由表（capability="key_rate_limit"）做作用域，与其他能力插件一致：
//
//	models            = ["*"]（全部模型）/ ["gpt-4o"]（单模型）
//	channel_base_urls = ["https://api.xxx.com/v1"]   ← 平台级
//	channel_ids       = ["ch-abc"]                   ← 精确到某个账号（可选）
//	rate_limit        = {max_requests, window_seconds, on_exhausted}
//
// 两个事件分工：
//   - proxy:channels-resolved（转发前，候选已解析完）：剔除已达上限的 Key；
//     全部达上限且 on_exhausted=reject 时返回 GatewayError（429），直接拒绝本次请求。
//   - proxy:before-attempt（每次渠道尝试前）：对「本次真实要用的那个 Key」记一次消费。
//     放在这里是因为此时 __current_channel 已是具体渠道，语义准确；且同一请求重试/聚合中间
//     attempt 都会各自记账，与实际访问上游账号的次数一致。
//
// 【铁律】读表失败、计数失败一律 fail-open（不限流），绝不因为限流把正常请求打死。
package keyratelimit

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"loadout/core/db"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// capabilityName 能力路由表中本能力的固定名称。
const capabilityName = "key_rate_limit"

// defaultWindowSeconds 未配置窗口秒数时的默认值（1 分钟）。
const defaultWindowSeconds = 60

// sweepInterval 内存计数桶的后台清扫间隔。
const sweepInterval = time.Minute

// sweepMaxIdle 清扫口径上限：没有任何请求超过这个时长的桶直接回收。
// 取一个明显大于常见窗口的值（1 小时），保证「窗口内仍有记录的桶」不会被误删。
const sweepMaxIdle = time.Hour

// Service 限流适配器：查能力路由、滑动窗口计数、候选筛选与消费。
//
// 计数器是一个带锁的 map，只活在进程内存里（重启清零，用户已确认）；规则配置仍随能力路由
// 表持久化在 loadout.db。
type Service struct {
	st   *store.Store
	lg   *slog.Logger
	repo *db.Repository // SQLite 能力路由/渠道数据源（装配后注入；nil 时回退 JSON）

	counter *windowCounter
	// conc 在途并发计数：与 counter 分开存（两个维度独立配置，也不该互相影响清扫）。
	conc *concurrencyCounter

	// sweepOnce 保证后台清扫 goroutine 只启动一次（测试可重复调用 Start）。
	sweepOnce sync.Once
}

// NewService 创建限流适配器。
func NewService(st *store.Store, lg *slog.Logger) *Service {
	return &Service{st: st, lg: lg, counter: newWindowCounter(), conc: newConcurrencyCounter()}
}

// SetRepository 注入 SQLite 仓储（由装配层在 db 就绪后调用；测试可省略）。
func (s *Service) SetRepository(repo *db.Repository) { s.repo = repo }

// StartSweeper 启动后台清扫：定期回收「早已没有请求」的计数桶，防止内存随渠道数量增长。
// 返回停止函数（装配层用 ctx.Effect 注册，卸载时调用）。
func (s *Service) StartSweeper() func() {
	stop := make(chan struct{})
	var once sync.Once
	s.sweepOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(sweepInterval)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					if n := s.counter.sweep(sweepMaxIdle, time.Now()); n > 0 {
						s.lg.Debug("key-rate-limit: 回收空闲计数桶", "removed", n, "remaining", s.counter.size())
					}
				}
			}
		}()
	})
	return func() { once.Do(func() { close(stop) }) }
}

// decideRoutes 查能力路由：model + 请求渠道上下文的 key_rate_limit 路由。
// 未命中返回空；命中 native（豁免）立即返回该项；多个 proxy 路由返回全部（叠加）。
// 选择策略统一走 types.SelectCapabilityRoutesEx（与 sensitive-filter / field-filter 一致，
// 支持聚合模型的候选 Key 与渠道级 base_url 命中）。
// 读表/解析失败 fail-open：记日志并返回空（视为不限流）。
func (s *Service) decideRoutes(pipe *modelgateway.ProxyPipeline, virtualModel string) []*types.CapabilityRoute {
	if pipe == nil || pipe.Request == nil {
		return nil
	}
	scope := types.ChannelScopeFromMetadata(pipe.Metadata, s.requestChannelBaseURLs)
	model := pipe.Request.Model
	if s.repo != nil {
		routes, err := s.repo.ListCapabilityRoutes(context.Background())
		if err == nil {
			return types.SelectCapabilityRoutesEx(routes, capabilityName, model, virtualModel, scope)
		}
		s.lg.Warn("key-rate-limit: 从 SQLite 读能力路由表失败，回退 JSON", "err", err)
	}
	var routes []types.CapabilityRoute
	if err := s.st.Read(types.FileCapabilityRoutes, &routes); err != nil {
		if errors.Is(err, store.ErrNotExist) {
			return nil
		}
		s.lg.Warn("key-rate-limit: 读取能力路由表失败，按不限流处理", "err", err)
		return nil
	}
	return types.SelectCapabilityRoutesEx(routes, capabilityName, model, virtualModel, scope)
}

// requestChannelBaseURLs 反查渠道 base_url 列表：term 可为渠道 key id（精确匹配，返回该 key
// 所在渠道组的 base_url）或渠道名 ChannelName（返回组内全部启用 Key 共享的 base_url，去重）。
// 实现与 force-stream / field-filter 一致（渠道级约束 channel_base_urls 需要）。
func (s *Service) requestChannelBaseURLs(term string) []string {
	if term == "" {
		return nil
	}
	find := func(channels []types.Channel) []string {
		var byID string
		var byName []string
		for _, ch := range channels {
			if ch.ID == term {
				byID = ch.BaseURL
			}
			if ch.Enabled && ch.ChannelName == term && ch.BaseURL != "" {
				byName = append(byName, ch.BaseURL)
			}
		}
		if byID != "" {
			return []string{byID}
		}
		return byName
	}
	if s.repo != nil {
		channels, err := s.repo.ListChannels(context.Background())
		if err == nil {
			if out := find(dbChannelsToTypes(channels)); len(out) > 0 {
				return out
			}
		}
	}
	var channels []types.Channel
	if err := s.st.Read(types.FileChannels, &channels); err == nil {
		return find(channels)
	}
	return nil
}

// dbChannelsToTypes 把 db.Channel 转为 types.Channel（store 兜底查询复用 find）。
func dbChannelsToTypes(channels []db.Channel) []types.Channel {
	out := make([]types.Channel, 0, len(channels))
	for _, ch := range channels {
		out = append(out, types.Channel{
			ID:          ch.ID,
			Name:        ch.Name,
			ChannelName: ch.ChannelName,
			BaseURL:     ch.BaseURL,
			Enabled:     ch.ManualEnabled,
		})
	}
	return out
}

// scopeKey 一条限流规则的作用域签名：决定「哪几个模型共用一个配额桶」。
//   - models 含 "*" → "*"（该 Key 全部模型共用一个桶）；
//   - 否则按排序后的模型名拼接（同一规则覆盖的模型共用一个桶）。
// 规则身份也参与签名（用规则下标）：同一 Key 被多条规则覆盖时各自独立计数，互不干扰。
func scopeKey(models []string, ruleIndex int) string {
	sig := "*"
	if !containsString(models, "*") {
		sorted := append([]string(nil), models...)
		sort.Strings(sorted)
		sig = strings.Join(sorted, "|")
	}
	// 规则下标参与签名：多条规则并存时，各自一份配额（更严的那条先触发）。
	return sig + "#" + strconv.Itoa(ruleIndex)
}

// containsString 判断切片是否含指定元素。
func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// activeRule 命中且真正生效的一条限流规则（配好参数、归一化后的形态）。
type activeRule struct {
	index   int
	rule    types.RateLimitRule
	scope   string // 作用域签名（决定共用桶的模型集合）
	window  time.Duration
	onExh   string
	// holds 该规则占用/释放并发名额用的计数器（与次数计数分开，避免互相影响）。
	holds *concurrencyCounter
}

// activeRules 把命中路由归一化为生效规则列表。
//   - 未配置 rate_limit（或两个维度都没配）的条目直接忽略（未配置 = 不限流）；
//   - 命中 native 路由返回「豁免」信号：该模型/渠道不限流（精确豁免通配规则）。
//
// 返回 (rules, exempt)：exempt=true 时调用方应立即按「不限流」处理。
func (s *Service) activeRules(routes []*types.CapabilityRoute) ([]activeRule, bool) {
	var out []activeRule
	for i, r := range routes {
		if r == nil {
			continue
		}
		if r.Route != types.RouteProxy {
			// native（及历史 error 降级）：豁免，不限流。
			return nil, true
		}
		rl := r.RateLimit
		if !rl.Configured() {
			continue // 两个维度都没配视为不限流
		}
		window := time.Duration(rl.WindowSeconds) * time.Second
		if rl.WindowSeconds <= 0 {
			window = defaultWindowSeconds * time.Second
		}
		onExh := rl.OnExhausted
		if onExh != types.RateLimitReuse {
			onExh = types.RateLimitReject // 默认拒绝（429）
		}
		out = append(out, activeRule{
			index:  i,
			rule:   *rl,
			scope:  scopeKey(r.Models, i),
			window: window,
			onExh:  onExh,
			holds:  s.conc,
		})
	}
	return out, false
}

// HandleChannelsResolved proxy:channels-resolved 事件：候选解析完成后剔除已达上限的 Key。
//
// 行为：
//   - 未命中规则 / 命中 native 豁免 / 规则未配参数 → 原样返回（不限流）；
//   - 先按「滑动窗口内次数还有余量」筛出候选（次数维度，纯查询不掉名额）；
//   - 再为**将要使用的那个 Key** 抢占一个并发名额（次数够 + 并发有位的第一个候选），
//     并把它排到候选最前，保证 proxyForward 用的正是占到位的那一个。
//     抢占必须在候选阶段完成：若等每次尝试前才占，突发并发会先各自挑中同一个 Key、随后
//     集体让步，并发限制形同虚设。抢占是原子的（先查后占在同一把锁内），不会被竞态绕过。
//   - 一个候选都抢不到（次数用满或并发都占满）：
//   - on_exhausted=reject（默认）→ 返回 GatewayError(429)；
//   - on_exhausted=reuse → 原样返回全量候选（忽略限制，继续使用）。
//   - **测试锁定请求完全绕过限流**（见 isPinned）：不筛候选、不占名额、不返回 429。
//
// 占到的名额记在管线的 metadata 上，由 proxy:attempt-finished 释放（核心用 defer 保证触发）。
func (s *Service) HandleChannelsResolved(payload any) (any, error) {
	p, ok := payload.(*modelgateway.ChannelCandidatesPayload)
	if !ok || p == nil || p.Pipe == nil || len(p.Candidates) == 0 {
		return payload, nil
	}
	virtualModel := types.VirtualModelFromMetadata(p.Pipe.Metadata)
	routes := s.decideRoutes(p.Pipe, virtualModel)
	if len(routes) == 0 {
		return payload, nil
	}
	rules, exempt := s.activeRules(routes)
	if exempt || len(rules) == 0 {
		return payload, nil
	}
	// 同一管线可能多次走到这里（聚合 failover 会递归回到 proxyForward 重新解析候选）。
	// 测试锁定请求（模型状态页逐 Key「测试」按钮）：直接放行，不做任何筛选与占位。
	if isPinned(p.Pipe) {
		return payload, nil
	}
	// 上一轮占的并发名额先按当时记下的桶键精确归还，避免重复占位导致名额泄漏。
	s.releaseHold(p.Pipe.Metadata)

	now := time.Now()
	// 1) 次数维度筛选：窗口内还有余额的候选保留（排序不变，保持原有偏好顺序）。
	var byWindow []modelgateway.ResolvedChannel
	for _, ch := range p.Candidates {
		if s.windowHasRoom(ch.ID, rules, now) {
			byWindow = append(byWindow, ch)
		}
	}
	if len(byWindow) == 0 {
		// 次数维度已全部用满。
		return s.rejectOrReuse(payload, p, rules, "window")
	}
	// 2) 并发维度：为「次数够 + 并发有位」的第一个候选原子抢占名额。
	//    不限并发（规则里没配 max_concurrency）时直接取第一个，不做占位。
	grantedIdx := -1
	for i := range byWindow {
		if s.acquireConc(byWindow[i].ID, rules, p.Pipe.Metadata) {
			grantedIdx = i
			break
		}
	}
	if grantedIdx < 0 {
		// 候选的并发名额全被占满。
		return s.rejectOrReuse(payload, p, rules, "concurrency")
	}
	// 3) 把占到位的那一个排到最前，保证 proxyForward 首选的正是已占住名额的 Key。
	if grantedIdx > 0 {
		byWindow[0], byWindow[grantedIdx] = byWindow[grantedIdx], byWindow[0]
	}
	if len(byWindow) != len(p.Candidates) || grantedIdx > 0 {
		p.Candidates = byWindow
	}
	return p, nil
}

// rejectOrReuse 全部候选都达上限时的统一出口：reject（默认）返回 429，reuse 原样放行。
// dimension 只用于日志（window = 次数用满，concurrency = 并发占满）。
func (s *Service) rejectOrReuse(payload any, p *modelgateway.ChannelCandidatesPayload, rules []activeRule, dimension string) (any, error) {
	if rules[0].onExh == types.RateLimitReuse {
		s.lg.Warn("key-rate-limit: 所有候选 Key 均达上限，按 reuse 策略继续使用",
			"model", p.Model, "candidates", len(p.Candidates), "dimension", dimension)
		return payload, nil
	}
	s.lg.Info("key-rate-limit: 所有候选 Key 均达上限，拒绝本次请求",
		"model", p.Model, "candidates", len(p.Candidates), "dimension", dimension,
		"max_requests", rules[0].rule.MaxRequests, "window_seconds", int(rules[0].window.Seconds()),
		"max_concurrency", rules[0].rule.MaxConcurrency)
	return payload, &modelgateway.GatewayError{
		Status: http.StatusTooManyRequests,
		Type:   "key_rate_limited",
		Msg:    "当前模型的全部渠道 Key 均已达到限流上限，请稍后重试",
	}
}

// windowHasRoom 判断某渠道 Key 在全部生效规则的滑动窗口内是否还有次数余量
// （任一规则达上限即不可用）。计数查询失败按「有余额」处理（fail-open）。
func (s *Service) windowHasRoom(channelID string, rules []activeRule, now time.Time) bool {
	for _, r := range rules {
		if r.rule.MaxRequests <= 0 {
			continue // 该规则不限次数
		}
		key := bucketKey(channelID, r.scope)
		if s.counter.used(key, r.rule.MaxRequests, r.window, now) >= r.rule.MaxRequests {
			return false
		}
	}
	return true
}

// concPlan 某渠道 Key 在全部生效规则下的并发占用方案：待占的桶键 + 各自的并发上限。
// 不限并发的规则不参与。每个桶按各自规则的上限判定（不用统一最小值，避免宽松规则被误伤）。
type concPlan struct {
	keys []string
	max  []int
}

// concPlanFor 计算某渠道 Key 的并发占用方案。none=true 表示该规则集不限制并发。
func concPlanFor(channelID string, rules []activeRule) (plan concPlan, none bool) {
	for _, r := range rules {
		if r.rule.MaxConcurrency <= 0 {
			continue
		}
		plan.keys = append(plan.keys, bucketKey(channelID, r.scope))
		plan.max = append(plan.max, r.rule.MaxConcurrency)
	}
	if len(plan.keys) == 0 {
		return concPlan{}, true
	}
	return plan, false
}

// acquireConc 原子占用某渠道 Key 的并发名额。任一桶占不到即回滚全部并返回 false；
// 全部占到返回 true 并记下**实际占用的桶键**——调用方据此精确释放，不必反推规则。
func (s *Service) acquireConc(channelID string, rules []activeRule, md map[string]any) bool {
	plan, none := concPlanFor(channelID, rules)
	if none {
		return true // 该规则集不限并发：无需占位
	}
	var acquired []string
	for i, key := range plan.keys {
		if ok, _ := s.conc.acquire(key, plan.max[i]); !ok {
			for _, back := range acquired {
				s.conc.release(back)
			}
			return false
		}
		acquired = append(acquired, key)
	}
	md[concMarkKey] = &concHold{channelID: channelID, keys: acquired}
	return true
}

// releaseHold 精确归还一批已占的并发桶键（幂等：归还后清空标记）。
func (s *Service) releaseHold(md map[string]any) {
	if md == nil {
		return
	}
	hold, _ := md[concMarkKey].(*concHold)
	if hold != nil {
		for _, key := range hold.keys {
			s.conc.release(key)
		}
	}
	delete(md, concMarkKey)
}

// heldChannel 当前并发名额归属的渠道 Key（无占位返回空串）。
func heldChannel(md map[string]any) string {
	if md == nil {
		return ""
	}
	hold, _ := md[concMarkKey].(*concHold)
	if hold == nil {
		return ""
	}
	return hold.channelID
}

// 并发占位标记的 metadata 键：值为本次请求已抢占的**并发计数桶键**列表（每个生效规则一个）。
//
// 记桶键而不是渠道 ID：释放时不必再反推规则与作用域（failover 换模型后规则集可能已变，
// 按渠道 ID 反推会释放到错的桶、造成名额泄漏）。记下当时占的确切键，释放就永远精确。
// 由 HandleChannelsResolved 写入、HandleAttemptFinished 读取并释放；一个请求同一时刻只占
// 一组键，保证「占」与「放」严格配对。
const concMarkKey = "__key_rate_limit_conc"

// concHold 一次并发占位的记录：占的是哪个渠道 Key、占到了哪些并发计数桶。
// 记下来是为了精确释放——failover 换模型后规则集可能已变，靠渠道 ID 反推会释放到错的桶。
type concHold struct {
	channelID string
	keys      []string
}

// HandleBeforeAttempt proxy:before-attempt 事件：对本次真实要用的渠道 Key 记一次次数消费。
//
// 并发维度不在这里占用——已在 proxy:channels-resolved 候选筛选阶段占好（否则突发请求会
// 各自挑中同一个 Key 再集体失败，并发限制失效）。这里只负责次数记账。
// 测试锁定请求既不记账也不补占并发名额（与候选阶段一样完全绕过限流）。
//
// 只做计数，永不改 body/响应，也永不返回 error（返回 error 会让核心视为安检拒绝而中断请求）。
func (s *Service) HandleBeforeAttempt(payload any) (any, error) {
	pipe, ok := payload.(*modelgateway.ProxyPipeline)
	if !ok || pipe == nil || pipe.Request == nil || pipe.Metadata == nil {
		return payload, nil
	}
	// 测试锁定请求：不计数（否则探测流量会白占配额），也不补占并发名额。
	if isPinned(pipe) {
		return payload, nil
	}
	channelID, _ := pipe.Metadata["__current_channel"].(string)
	if channelID == "" {
		// 聚合渠道级/Key 多选目标在尝试前会把 __current_channel 写成具体值（见 proxyAttempt），
		// 这里为空说明尚未确定渠道，本事件不是消费点，跳过（consumer 在 proxyAttempt 内触发）。
		return payload, nil
	}
	routes := s.decideRoutes(pipe, types.VirtualModelFromMetadata(pipe.Metadata))
	if len(routes) == 0 {
		return payload, nil
	}
	rules, exempt := s.activeRules(routes)
	if exempt || len(rules) == 0 {
		return payload, nil
	}
	now := time.Now()
	for _, r := range rules {
		if r.rule.MaxRequests <= 0 {
			continue // 该规则不限次数
		}
		key := bucketKey(channelID, r.scope)
		used := s.counter.record(key, r.rule.MaxRequests, r.window, now)
		if used > r.rule.MaxRequests {
			s.lg.Debug("key-rate-limit: 渠道 Key 已超窗口配额（本次仍已放行）",
				"channel_id", channelID, "model", pipe.Request.Model,
				"used", used, "max_requests", r.rule.MaxRequests)
		}
	}
	// 不变量：正在使用某个渠道 Key 的请求，必须持有该 Key 的一个并发名额。
	// 常规路径下候选阶段占的就是本次要用的 Key（筛选时已把它排到候选首位），这里无需动作。
	// 不一致只会出现在 failover 切到别的候选时（上一 Key 的名额已由 attempt-finished 释放）：
	// 此时补占新 Key；补不到就按「软超额」放行并告警——宁可偶尔多放一个，也不能让名额永久
	// 泄漏（那会把该渠道彻底卡死）。
	if heldChannel(pipe.Metadata) != channelID {
		s.releaseHold(pipe.Metadata)
		if !s.acquireConc(channelID, rules, pipe.Metadata) {
			s.lg.Warn("key-rate-limit: failover 到新渠道时并发名额已满，本次按软超额放行",
				"channel_id", channelID, "model", pipe.Request.Model)
		}
	}
	return payload, nil
}

// HandleAttemptFinished proxy:attempt-finished 事件：释放本次尝试占住的并发名额。
//
// 纯通知、无返回值语义，只做释放。标记里记录的是候选筛选阶段抢占到的那个 Key，本次尝试
// 若是该 Key 就释放（乐观标记由更晚的那个事件清理；同一时刻只有一个事件在跑，不会重复释放
// ——标记一经释放即删除，后续竞速事件读到的 slug 为空自然跳过）。
func (s *Service) HandleAttemptFinished(payload any) (any, error) {
	p, ok := payload.(*modelgateway.ProxyAttemptFinishedPayload)
	if !ok || p == nil || p.Pipe == nil || p.Pipe.Metadata == nil {
		return payload, nil
	}
	// 只释放「本次尝试实际用到的那个 Key」上的名额：同一管线上其它 Key 的占位不该被本事件
	// 顺手清掉（failover 场景下名额归属会随 __current_channel 迁移，归属一致才释放）。
	if heldChannel(p.Pipe.Metadata) == "" || heldChannel(p.Pipe.Metadata) != p.ChannelID {
		return payload, nil
	}
	s.releaseHold(p.Pipe.Metadata)
	return payload, nil
}

// isPinned 判断本次请求是否是「测试锁定」请求（模型状态页逐 Key 的「测试」按钮）。
//
// 该按钮的语义是「探测这个 Key 本身现在还能不能用」：用户点它，往往正是想知道一个
// 已被熔断/已被限流跳过的 Key 到底还行不行。因此这类请求必须**完全绕过限流**——
// 不筛候选（否则目标 Key 被剔除，测的根本不是它）、不占并发、不计数、也不返回 429。
//
// 判据只看管线 metadata 上的 pin 标记：该标记由 model-gateway 在入口按
// X-Loadout-Pin-Channel 头写入（且只认回环来源），本插件不必也不该重复校验来源。
func isPinned(pipe *modelgateway.ProxyPipeline) bool {
	if pipe == nil || pipe.Metadata == nil {
		return false
	}
	s, _ := pipe.Metadata[modelgateway.MetadataPinChannel].(string)
	return s != ""
}

// bucketKey 计数桶键：(渠道 Key, 作用域签名)。
// 分隔符用 "\x1f"（ASCII 单元分隔符）——渠道 ID 是 hex、签名是模型名/下标，都不会包含它。
func bucketKey(channelID, scope string) string {
	return channelID + "\x1f" + scope
}


