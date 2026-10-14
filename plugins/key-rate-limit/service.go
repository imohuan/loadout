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

	// sweepOnce 保证后台清扫 goroutine 只启动一次（测试可重复调用 Start）。
	sweepOnce sync.Once
}

// NewService 创建限流适配器。
func NewService(st *store.Store, lg *slog.Logger) *Service {
	return &Service{st: st, lg: lg, counter: newWindowCounter()}
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
	exempt  bool // native 路由 = 豁免（不限流），用于短路
}

// activeRules 把命中路由归一化为生效规则列表。
//   - 未配置 rate_limit / MaxRequests<=0 的条目直接忽略（未配置 = 不限流）；
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
		if rl == nil || rl.MaxRequests <= 0 {
			continue // 未配参数视为不限流
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
		})
	}
	return out, false
}

// HandleChannelsResolved proxy:channels-resolved 事件：候选解析完成后剔除已达上限的 Key。
//
// 行为：
//   - 未命中规则 / 命中 native 豁免 / 规则未配参数 → 原样返回（不限流）；
//   - 逐个候选 Key 判断「在每条生效规则的窗口内是否还有余量」；任一规则已达上限即视为该 Key
//     本轮不可用，从候选里剔除；
//   - 剔除后仍有关键字可选 → 用精简后的候选覆盖 Candidates（请求自动落到下一个账号）；
//   - 全部候选都达上限：
//   - on_exhausted=reject（默认）→ 返回 GatewayError(429)，交由核心拒绝本次请求；
//   - on_exhausted=reuse → 原样返回全量候选（忽略限流，继续使用）。
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

	now := time.Now()
	var available []modelgateway.ResolvedChannel
	for _, ch := range p.Candidates {
		if s.channelAvailable(ch.ID, rules, now) {
			available = append(available, ch)
		}
	}
	if len(available) == 0 {
		// 全部达上限：按第一条生效规则的 on_exhausted 决定拒绝还是放行。
		if rules[0].onExh == types.RateLimitReuse {
			s.lg.Warn("key-rate-limit: 所有候选 Key 均达上限，按 reuse 策略继续使用",
				"model", p.Model, "candidates", len(p.Candidates))
			return payload, nil
		}
		s.lg.Info("key-rate-limit: 所有候选 Key 均达上限，拒绝本次请求",
			"model", p.Model, "candidates", len(p.Candidates),
			"max_requests", rules[0].rule.MaxRequests, "window_seconds", int(rules[0].window.Seconds()))
		return payload, &modelgateway.GatewayError{
			Status: http.StatusTooManyRequests,
			Type:   "key_rate_limited",
			Msg:    "当前模型的全部渠道 Key 均已达到限流上限，请稍后重试",
		}
	}
	if len(available) == len(p.Candidates) {
		return payload, nil // 没有剔除任何 Key：不改动，避免无谓的候选重写
	}
	p.Candidates = available
	return p, nil
}

// channelAvailable 判断某渠道 Key 在全部生效规则下是否还有余量（任一规则达上限即不可用）。
// 计数查询失败按「可用」处理（fail-open）。
func (s *Service) channelAvailable(channelID string, rules []activeRule, now time.Time) bool {
	for _, r := range rules {
		key := bucketKey(channelID, r.scope)
		if s.counter.used(key, r.rule.MaxRequests, r.window, now) >= r.rule.MaxRequests {
			return false
		}
	}
	return true
}

// HandleBeforeAttempt proxy:before-attempt 事件：对本次真实要用的渠道 Key 记一次消费。
//
// 只做计数，永不改 body/响应，也永不返回 error（返回 error 会让核心视为安检拒绝而中断请求）。
func (s *Service) HandleBeforeAttempt(payload any) (any, error) {
	pipe, ok := payload.(*modelgateway.ProxyPipeline)
	if !ok || pipe == nil || pipe.Request == nil || pipe.Metadata == nil {
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
		key := bucketKey(channelID, r.scope)
		used := s.counter.record(key, r.rule.MaxRequests, r.window, now)
		if used > r.rule.MaxRequests {
			s.lg.Debug("key-rate-limit: 渠道 Key 已超窗口配额（本次仍已放行）",
				"channel_id", channelID, "model", pipe.Request.Model,
				"used", used, "max_requests", r.rule.MaxRequests)
		}
	}
	return payload, nil
}

// bucketKey 计数桶键：(渠道 Key, 作用域签名)。
// 分隔符用 "\x1f"（ASCII 单元分隔符）——渠道 ID 是 hex、签名是模型名/下标，都不会包含它。
func bucketKey(channelID, scope string) string {
	return channelID + "\x1f" + scope
}


