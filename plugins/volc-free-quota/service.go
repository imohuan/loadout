package volcfreequota

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"loadout/core/config"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
)

// Service 火山引擎免费额度监控服务。
//
// 线程模型：bgRefresh 启动一个 goroutine 定时刷新；其他方法都是数据库读写，
// SQLite 单连接池 + 时间短不会撞锁。HandleProxyUpstreamSucceeded 与 HandleProxyBeforeUpstream
// 都在转发热路径上，必须保持 zero allocation 下完成（只有 SQL exec）。
type Service struct {
	db     *sql.DB
	st     *store.Store
	lg     *slog.Logger
	client billingFetcher

	// inMemoryChannels 是 doRefresh 期间缓存的渠道 ID→渠道记录，避免刷新热路径再查 channels。
	// 缓存有效到下次显式失效（channel 增删）——插件自身不实现失效，靠定期重建。
	mu                sync.Mutex
	lastChannelCache  map[string]channelSnapshot
	lastChannelCacheT time.Time

	// 后台定时刷新（rev2）：纯 ticker 每 interval 无条件刷一次（去 debounce——
	// 请求活动/无请求时 debounce 都可能导致远程永不刷新，额度恢复检测不到）。
	// refreshing 是 in-flight 标记（Refresh 入口互斥，手动/定时共用）。
	bgMu       sync.Mutex
	refreshing bool
	bgCancel   context.CancelFunc
}

// channelSnapshot 渠道关键字段（model_states 写入需要的）。
type channelSnapshot struct {
	ID   string
	Name string
}

// NewService 创建火山引擎免费额度监控服务。
func NewService(database *sql.DB, st *store.Store, lg *slog.Logger) *Service {
	if lg == nil {
		lg = slog.Default()
	}
	return &Service{
		db:     database,
		st:     st,
		lg:     lg,
		client: newVolcBillingClient(lg),
	}
}

// accountID 计算账号指纹：SHA256(access_key) 前 16 位十六进制。
//
// 免费额度按火山账号（AK/SK）归属：同一账号的多个渠道 Key 共享同一份免费额度，
// 因此所有额度相关记录（volc_quota_packages / volc_quota_usage）以 account_id 对齐，
// 而不是 channel_id。AK 是明文存储的稳定标识，SHA256 指纹无碰撞风险、不可逆。
func accountID(accessKey string) string {
	if accessKey == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(accessKey))
	return hex.EncodeToString(sum[:])[:16]
}

// errRefreshInFlight 并发刷新冲突的哨兵错误（HandleRefresh 用 errors.Is 判断返回 409）。
var errRefreshInFlight = errors.New("刷新进行中，请稍后再试")

// StartBackgroundRefresh 启动后台定时刷新：纯 ticker，每 interval 无条件刷一次远程。
//
//   - interval <= 0 表示不启动（仍可手动调用 Refresh）。
//   - runNow=true 时异步立即触发一次刷新（不阻塞启动；拿到首份数据建底数）。
//   - ticker 每 interval 触发 safeRefresh；Refresh 入口的 refreshing 锁保证并发不重复拉
//     billing（手动 HTTP 刷新与定时刷新互斥）。
//
// 与旧 debounce（v16）的区别（rev2 审计 E）：debounce 只在请求成功后重置计时器，
// 请求活动期间 timer 被持续重置、无请求时永不触发——两种情况下远程都永不刷新，
// 额度恢复（8:00~11:30）检测不到。纯 ticker 无条件刷，滞后数据由 computeLocalRemaining
// 的"今天扣完不记 + 下降校准"保护，不会误判。
//
// 返回 Disposer（unload 时调用，停 ticker goroutine）。
func (s *Service) StartBackgroundRefresh(interval time.Duration, runNow bool) func() {
	if runNow {
		go s.safeRefresh() // 异步，不阻塞启动
	}
	if interval <= 0 {
		return func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.bgMu.Lock()
	s.bgCancel = cancel
	s.bgMu.Unlock()
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.safeRefresh()
			}
		}
	}()
	return func() {
		s.bgMu.Lock()
		if s.bgCancel != nil {
			s.bgCancel()
			s.bgCancel = nil
		}
		s.bgMu.Unlock()
	}
}

// safeRefresh 后台刷新统一入口：失败记 Warn（ticker 下周期自动重试），
// 并发由 Refresh 入口的 refreshing 锁保证。
func (s *Service) safeRefresh() {
	if _, err := s.Refresh(context.Background(), "", false); err != nil {
		s.lg.Warn("volc-free-quota: 后台刷新失败", "err", err)
	}
}

// ===== 事件钩子 =====

// HandleProxyUpstreamSucceeded 在每次成功转发后：
//  1. 记录 (account_id, model) 使用次数（volc_quota_usage）
//  2. 从本地余额 local_remaining 扣减 usage.total_tokens（不依赖 billing API）
//  3. local_remaining 扣到 <= 0 → status='exhausted'（后续请求被 before-upstream 拦截）
//
// 这是"本地递减余额"的核心：每次 API 响应拿到 total_tokens 就从本地余额里减，
// 用完归零、提前拦截，不等 billing API 报错（它经常 429）。
func (s *Service) HandleProxyUpstreamSucceeded(payload any) (any, error) {
	p, ok := payload.(*modelgateway.ProxySuccessPayload)
	if !ok || p == nil {
		return payload, nil
	}
	if p.ChannelID == "" || p.Model == "" {
		return payload, nil
	}
	aid := s.accountIDForChannel(p.ChannelID)
	if aid == "" {
		// 该渠道未配置 AK/SK → 不是免费额度追踪的账号，跳过（也不报错）。
		return payload, nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.Exec(`INSERT INTO volc_quota_usage(account_id, model, use_count, last_used_at) VALUES (?, ?, 1, ?)
		ON CONFLICT(account_id, model) DO UPDATE SET use_count = volc_quota_usage.use_count + 1, last_used_at = excluded.last_used_at`,
		aid, p.Model, now)
	if err != nil {
		// 路由热路径上的日志不能打 ERROR（容易刷屏且不致命），用 Warn 即可。
		s.lg.Warn("volc-free-quota: 记录使用次数失败", "channel_id", p.ChannelID, "model", p.Model, "err", err)
	}
	s.decrementLocalRemaining(aid, p.Model, p.Usage.TotalTokens)
	// 定时刷新由 StartBackgroundRefresh 的纯 ticker 负责（rev2 去 debounce），
	// 热路径不再做 timer 重置。
	return payload, nil
}

// decrementLocalRemaining 从该账号所有匹配该 API 模型的资源包行扣减 tokens。
//
// 匹配锚点是 volc_quota_packages.model（configuration_code 提取名，如
// "deepseek-v4-flash-0731"），与请求的 API 模型名（如 "deepseek-v4-flash-ga-260731"）
// 用 matchOne 模糊匹配（含日期归一化）。扣减到 <= 0 时置 status='UsedUp'；
// local_remaining 下限 0（不出现负数）。
//
// v17 起 volc_quota_models 聚合表已删除，扣减只更新 volc_quota_packages
//（UI 资源包明细表是唯一数据源）。
//
// 日志：每次成功扣减记 Info（含扣前/扣后余额），未匹配到任何资源包记 Warn
// （说明该 API 模型没有对应的免费额度记录，本地扣减没生效，需要排查）。
func (s *Service) decrementLocalRemaining(accountID, apiModel string, tokens int) {
	if accountID == "" || apiModel == "" || tokens <= 0 {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := s.db.Query(`SELECT instance_no, model, configuration_name, local_remaining FROM volc_quota_packages WHERE account_id = ? AND initial_total > 0`+activePackageCond+` ORDER BY local_remaining DESC`, accountID, now)
	if err != nil {
		s.lg.Warn("volc-free-quota: 查询资源包余额失败", "account_id", accountID, "model", apiModel, "err", err)
		return
	}
	defer rows.Close()
	// matched: 按余额从大到小排序的 (instance_no, model, configuration_name, 扣前余额) 列表。
	// 同 model 提取名可能对应多个资源包（如 DeepSeek_V4_flash 有多个包），
	// 一次请求的 tokens 要按顺序分摊：先扣余额大的，扣到 0 再扣下一个，
	// 总扣减恰好等于 tokens，不能每个包都全额扣（P1 修复）。
	type match struct {
		instanceNo string
		model      string
		confName   string
		before     int64
	}
	var matched []match
	for rows.Next() {
		var m match
		if err := rows.Scan(&m.instanceNo, &m.model, &m.confName, &m.before); err == nil && m.model != "" && matchOne(m.model, apiModel) {
			matched = append(matched, m)
		}
	}
	if len(matched) == 0 {
		s.lg.Warn("volc-free-quota: 未匹配到免费额度资源包，本地不扣减",
			"account_id", accountID, "api_model", apiModel, "tokens", tokens,
			"hint", "该 API 模型没有对应的 volc_quota_packages 行（model 为 configuration_code 提取名），请检查渠道是否已刷新远程额度")
		return
	}
	remaining := int64(tokens)
	for i := range matched {
		if remaining <= 0 {
			break // tokens 已分摊完，剩余包不动
		}
		m := &matched[i]
		// 本次实际扣这个包的量 = min(剩余待扣, 该包余额)，下限 0。
		deduct := remaining
		if deduct > m.before {
			deduct = m.before
		}
		if deduct <= 0 {
			continue // 该包已耗尽，跳过
		}
		_, err := s.db.Exec(`
			UPDATE volc_quota_packages
			SET local_remaining = MAX(local_remaining - ?, 0),
			    status = CASE WHEN local_remaining - ? <= 0 THEN 'UsedUp' ELSE status END,
			    synced_at = ?  -- 复用 synced_at 记录最近一次本地扣减时间（展示用）
			WHERE account_id = ? AND instance_no = ?`,
			deduct, deduct, now, accountID, m.instanceNo)
		if err != nil {
			s.lg.Warn("volc-free-quota: 扣减资源包余额失败", "account_id", accountID, "instance_no", m.instanceNo, "model", m.model, "configuration_name", m.confName, "deduct", deduct, "err", err)
			continue
		}
		remaining -= deduct
		var after int64
		if err := s.db.QueryRow(`SELECT local_remaining FROM volc_quota_packages WHERE account_id = ? AND instance_no = ?`, accountID, m.instanceNo).Scan(&after); err != nil {
			after = -1
		}
		s.lg.Info("volc-free-quota: 本地余额扣减",
			"account_id", accountID, "api_model", apiModel, "matched_model", m.model,
			"configuration_name", m.confName, "instance_no", m.instanceNo,
			"tokens", deduct, "before", m.before, "after", after)
	}
	if remaining > 0 {
		// 所有匹配包都扣到 0 仍未扣完：说明免费额度已彻底耗尽，剩余部分不记账（日志提醒）。
		s.lg.Warn("volc-free-quota: 免费额度已扣完，剩余 tokens 未入账",
			"account_id", accountID, "api_model", apiModel, "unaccounted", remaining)
	}
	// 聚合双向同步（唯一判定入口，rev2）：单包扣到 0 不再直接禁用模型（可能其他包还有余额），
	// 改为按 API 模型聚合 SUM(local_remaining) 三态判定（用完→冷却 / 恢复→解除 / 中间态不动）。
	// 幂等：状态无变化时 UPDATE 影响 0 行，每次请求成功后调用可接受。
	s.syncModelStatesByAggregate(context.Background(), accountID)
}

// syncModelStatesByAggregate 按账号聚合余额三态同步 model_states（唯一判定入口）。
//
//   - 某 API 模型所有匹配 quota model 的聚合 SUM(local_remaining) <= minRemaining（默认 0）
//     → 写冷却（用完）
//   - SUM > config.VolcQuotaReviveThreshold（默认 45 万）→ 解除冷却（恢复）
//   - 中间态（0 < SUM <= 45 万）→ 不动（小额残留不算恢复，也不重复禁用，避免状态摇摆）
//
// 关键（rev2 审计 C）：多 quota model（如 deepseek-v4-flash / deepseek-v4-flash-0731）
// 可能映射到同一 API 模型（deepseek-v4-flash-ga-260731），必须按 API 模型合并 SUM 后
// 统一判定，否则同循环内先 re-enable 后 disable 随机抖动（map 无序迭代）。
//
// 调用方：refreshOne（刷新后）、decrementLocalRemaining（请求扣减后）。幂等。
// 返回本次判为"用完"并写冷却的 API 模型名（排序后，供 RefreshResult.DisabledModels）。
func (s *Service) syncModelStatesByAggregate(ctx context.Context, accountID string) []string {
	channelIDs := s.channelsForAccount(accountID)
	if len(channelIDs) == 0 {
		return nil
	}
	apiModels, err := s.channelsAPIModels(ctx, channelIDs)
	if err != nil || len(apiModels) == 0 {
		return nil
	}
	agg := s.aggregateLocalRemaining(accountID)
	// 按 API 模型合并：apiModel -> 所有匹配 quota model 的聚合和
	apiAgg := make(map[string]int64)
	for quotaModel, sum := range agg {
		for _, m := range s.matchQuotaToAPIModels(quotaModel, apiModels) {
			apiAgg[m] += sum
		}
	}
	minRemaining := int64(config.VolcQuotaMinRemaining)
	reviveThreshold := int64(config.VolcQuotaReviveThreshold)
	disabledSet := make(map[string]struct{})
	for m, sum := range apiAgg {
		switch {
		case sum <= minRemaining: // 用完（聚合判据）
			until := untilNextDayRecovery()
			for _, chID := range channelIDs {
				if err := s.disableModelForFreeQuota(ctx, chID, m, until); err != nil {
					s.lg.Warn("volc-free-quota: 聚合同步禁用失败", "channel_id", chID, "model", m, "err", err)
				}
			}
			disabledSet[m] = struct{}{}
		case sum > reviveThreshold: // 恢复（聚合判据，>45 万才解除禁用）
			for _, chID := range channelIDs {
				if err := s.reEnableModelForFreeQuota(ctx, chID, m); err != nil {
					s.lg.Warn("volc-free-quota: 聚合同步恢复失败", "channel_id", chID, "model", m, "err", err)
				}
			}
			// 中间态（0 < sum <= 45 万）：不动
		}
	}
	return disabledSorted(disabledSet)
}

// accountIDForChannel 按渠道 ID 查出该渠道配置的 AK，返回账号指纹；未配置返回 ""。
// 热路径单条查询，命中缓存场景少，直接查库即可（SQLite 内存库快）。
func (s *Service) accountIDForChannel(channelID string) string {
	var ak string
	if err := s.db.QueryRow(`SELECT access_key FROM volc_quota_config WHERE channel_id = ?`, channelID).Scan(&ak); err != nil {
		return ""
	}
	return accountID(ak)
}

// HandleProxyBeforeUpstream 在转发上游前检测：
//
// 只有当候选渠道中存在至少一个配置了「强制关停」（force_block=1）的账号时，才会执行拦截：
//  1. 若请求的 model 对应的本地余额（聚合 SUM(local_remaining)）在所有候选账号上
//     都已低于最低保留阈值（core/config.VolcQuotaMinRemaining，默认 0）
//     → 返回 "模型免费额度用完" GatewayError
//  2. 否则放行
//
// 判据唯一来源是本地 SQLite 余额（local_remaining）：远程 billing API 数据不准确
// （经常 429），只作初始底数，不参与拦截判定。
//
// 未开启强制关停时，拦截逻辑不生效，由 model_states 冷却机制控制禁用。
//
// 返回的 GatewayError 会被 model-gateway 直接写出为
// {"error":{"message":"模型免费额度用完","type":"free_quota_exhausted"}}。
func (s *Service) HandleProxyBeforeUpstream(payload any) (any, error) {
	p, ok := payload.(*modelgateway.ProxyPipeline)
	if !ok || p == nil || p.Request == nil {
		return payload, nil
	}
	model := p.Request.Model
	if model == "" {
		return payload, nil
	}
	// 取候选渠道集合：先看 metadata（aggregate 注入），空则视为所有渠道。
	candidateIDs := s.candidateChannelIDs(p)
	if len(candidateIDs) == 0 {
		// 没有任何渠道能处理该 model 时本来就会落到 "no_available_channel"，
		// 在此不必再叠加 quota 错误。
		return payload, nil
	}
	// 优先检查强制关停开关：任一候选渠道开启强制关停 → 执行拦截
	if !s.hasForceBlockEnabled(candidateIDs) {
		return payload, nil
	}
	// 看该 model 在所有候选账号里是否本地余额都已耗尽。
	exhausted, allExhausted, err := s.checkAllCandidatesExhausted(candidateIDs, model)
	if err != nil {
		s.lg.Warn("volc-free-quota: 检查本地余额状态失败", "model", model, "err", err)
		return payload, nil
	}
	if !exhausted {
		return payload, nil
	}
	if !allExhausted {
		// 至少一条候选渠道还有余量 → 放行，让路由选它。
		return payload, nil
	}
	// 所有候选渠道本地余额都耗尽 → 阻断并报明确错误。
	s.lg.Warn("volc-free-quota: 模型免费额度用完，已拦截请求",
		"model", model, "candidate_channels", len(candidateIDs))
	// 把拦截原因写入 pipe.Metadata，让 model-gateway.proxyRejectedLog 在写每个
	// target 的 skipped attempt 时直接带上"模型免费额度用完"，不再依赖
	// model_states（可能被"恢复全部异常"重置成 available + 空 last_error）。
	if p.Metadata != nil {
		p.Metadata["__unavailable_reason"] = "模型免费额度用完"
		p.Metadata["__unavailable_failure_class"] = "free_quota_exhausted"
	}
	// 本地拦截发生 → 同步把候选渠道的 model_states 写冷却（status='cooling' +
	// last_error='模型免费额度用完'，冷却到次日 0:00）。用户已通过请求触达本地余额
	// 判定，此时写状态是事实来源，不等 15min debounce / runNow。
	s.disableCandidatesForQuota(context.Background(), candidateIDs, model)
	return nil, &modelgateway.GatewayError{
		Status: http.StatusTooManyRequests,
		Type:   "free_quota_exhausted",
		Msg:    "模型免费额度用完",
	}
}

// disableCandidatesForQuota 把一批候选渠道的某 API 模型写 model_states 冷却。
// 拦截路径（HandleProxyBeforeUpstream）调用：本地余额判定已耗尽时，状态立即落库。
// 与 refreshOne 的 disableModelForFreeQuota 幂等（UPSERT，重复写同状态无副作用）。
func (s *Service) disableCandidatesForQuota(ctx context.Context, channelIDs []string, model string) {
	if len(channelIDs) == 0 || model == "" {
		return
	}
	until := untilNextDayRecovery() // 次日 12:00 北京时间兜底（rev2 审计 G，与刷新路径一致）
	for _, chID := range channelIDs {
		if err := s.disableModelForFreeQuota(ctx, chID, model, until); err != nil {
			s.lg.Warn("volc-free-quota: 拦截后同步 model_states 失败",
				"channel_id", chID, "model", model, "err", err)
		}
	}
}

// candidateChannelIDs 从 pipe.Metadata 解析候选渠道 id 集合：
//
//   - aggregate 注入的 __channel_candidates（多 Key / 渠道级目标）。
//   - aggregate 注入的 __current_channel（单 Key 目标）。
//   - 都为空时回退到「所有火山引擎渠道」（即 base_url 含 ark.cn-beijing.volces.com）。
func (s *Service) candidateChannelIDs(p *modelgateway.ProxyPipeline) []string {
	if p.Metadata == nil {
		return s.allVolcChannelIDs()
	}
	if idsAny, ok := p.Metadata["__channel_candidates"]; ok {
		if ids, ok := idsAny.([]string); ok && len(ids) > 0 {
			return ids
		}
	}
	if cur, ok := p.Metadata["__current_channel"].(string); ok && cur != "" {
		return []string{cur}
	}
	return s.allVolcChannelIDs()
}

// hasForceBlockEnabled 检查候选渠道中是否存在已启用且开启强制关停的配额配置。
// 只有存在这样的配置时，才会执行请求拦截（否则走 model_states 冷却逻辑）。
func (s *Service) hasForceBlockEnabled(candidateIDs []string) bool {
	if len(candidateIDs) == 0 {
		return false
	}
	placeholders := strings.Repeat("?,", len(candidateIDs))
	placeholders = strings.TrimSuffix(placeholders, ",")
	args := make([]any, len(candidateIDs))
	for i, id := range candidateIDs {
		args[i] = id
	}
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM volc_quota_config WHERE channel_id IN (`+placeholders+`) AND enabled = 1 AND force_block = 1`, args...).Scan(&count)
	return err == nil && count > 0
}

// allVolcChannelIDs 列举所有 base_url 命中 ark.cn-beijing.volces.com 的渠道 id。
//
// 用作"无候选"时的兜底：当 metadata 未设置（如纯转发未走 aggregate），凡是该平台的
// 渠道都视为候选，确保免费额度提示对所有可能命中的渠道生效。
func (s *Service) allVolcChannelIDs() []string {
	rows, err := s.db.Query(`SELECT id FROM channels WHERE base_url LIKE '%ark.cn-beijing.volces.com%'`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// checkAllCandidatesExhausted 检查候选渠道中此 model 的本地余额是否耗尽（按账号对齐）：
//
//   - 免费额度按火山账号（account_id）归属，因此先把候选渠道映射到它们各自的账号，
//     再去重为「候选账号集合」。
//   - 耗尽判定唯一来源是本地余额：匹配该 API 模型的**所有资源包聚合剩余**
//     SUM(local_remaining) <= core/config.VolcQuotaMinRemaining（默认 0 = 扣到 0；
//     可配 10000 提前停）。按聚合判断而不是任一包，避免"一个包剩 5000 其余 200 万"
//     时误拦整个模型。billing API 的 status='exhausted' 不参与——远程数据不准确。
//   - exhausted=true 表示至少一个候选账号耗尽；allExhausted=true 表示所有候选账号都耗尽。
//   - 采用与刷新时完全一致的 matchOne 模糊匹配逻辑，避免归一化名与 API 模型名脱节。
func (s *Service) checkAllCandidatesExhausted(candidateIDs []string, requestModel string) (exhausted, allExhausted bool, err error) {
	if len(candidateIDs) == 0 {
		return false, false, nil
	}
	// 候选渠道 → 账号集合（一个渠道可能未配置 AK/SK，跳过；同账号多渠道去重）。
	accountIDs := s.accountsForChannels(candidateIDs)
	if len(accountIDs) == 0 {
		return false, false, nil
	}
	// 全局最低保留阈值（core/config 程序级配置，非 per-渠道）。
	minRemaining := int64(config.VolcQuotaMinRemaining)
	// 取出候选账号中所有匹配该 model 的资源包本地余额记录（仅活跃包：非过期状态 + 未到期）。
	placeholders := strings.Repeat("?,", len(accountIDs))
	placeholders = strings.TrimSuffix(placeholders, ",")
	args := make([]any, 0, len(accountIDs)+1)
	for _, id := range accountIDs {
		args = append(args, id)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	args = append(args, now)
	rows, err := s.db.Query(`SELECT account_id, model, initial_total, local_remaining FROM volc_quota_packages WHERE account_id IN (`+placeholders+`) AND initial_total > 0`+activePackageCond, args...)
	if err != nil {
		return false, false, err
	}
	defer rows.Close()
	// map[account_id] -> 该账号匹配该 model 的聚合剩余（多个包求和）
	accountRemaining := make(map[string]int64)
	for rows.Next() {
		var aid, quotaModel string
		var initialTotal, localRemaining int64
		if err := rows.Scan(&aid, &quotaModel, &initialTotal, &localRemaining); err != nil {
			continue
		}
		if !matchOne(quotaModel, requestModel) {
			continue
		}
		accountRemaining[aid] += localRemaining
	}
	if len(accountRemaining) == 0 {
		// 没有任何账号有匹配的资源包
		return false, false, nil
	}
	// map[account_id] -> 该账号聚合剩余是否 ≤ 阈值
	accountExhausted := make(map[string]bool)
	for aid, sum := range accountRemaining {
		if sum <= minRemaining {
			accountExhausted[aid] = true
		}
	}
	if len(accountExhausted) == 0 {
		// 没有任何账号的聚合剩余低于阈值
		return false, false, nil
	}
	exhausted = true
	// allExhausted = 所有候选账号都有匹配的耗尽 model
	allExhausted = len(accountExhausted) == len(accountIDs)
	return exhausted, allExhausted, nil
}

// accountsForChannels 把一批渠道 ID 映射为去重后的账号集合（account_id）。
// 未配置 AK/SK 的渠道（不在 volc_quota_config）自动跳过。
func (s *Service) accountsForChannels(channelIDs []string) []string {
	if len(channelIDs) == 0 {
		return nil
	}
	placeholders := strings.Repeat("?,", len(channelIDs))
	placeholders = strings.TrimSuffix(placeholders, ",")
	args := make([]any, len(channelIDs))
	for i, id := range channelIDs {
		args[i] = id
	}
	rows, err := s.db.Query(`SELECT DISTINCT account_id FROM volc_quota_config WHERE channel_id IN (`+placeholders+`) AND account_id != ''`, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var aid string
		if err := rows.Scan(&aid); err == nil && aid != "" {
			out = append(out, aid)
		}
	}
	return out
}

// aggregatePackagesForAccount 按 model 聚合某账号的资源包（卡片视图数据源，v19）。
//
// 只统计 active 包（initial_total > 0 且非过期/未到期），口径与 aggregateLocalRemaining /
// 扣减 / 拦截一致。本地口径 UsedAmount = SUM(initial_total - local_remaining)；
// Percentage = LocalRemaining / InitialTotal * 100。
func (s *Service) aggregatePackagesForAccount(accountID string) []PackageAggregate {
	if accountID == "" {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := s.db.Query(`SELECT
			model,
			-- 展示名 = 模型名（model）；仅当 model 为空时才退回组内资源包名兜底。
			CASE WHEN model != '' THEN model ELSE MAX(configuration_name) END AS name,
			MAX(unit) AS unit,
			SUM(initial_total) AS initial_total,
			SUM(local_remaining) AS local_remaining,
			SUM(used_amount) AS used_amount,
			SUM(total_amount) AS total_amount,
			SUM(initial_total - local_remaining) AS used_local
		FROM volc_quota_packages
		WHERE account_id = ? AND initial_total > 0`+activePackageCond+`
		GROUP BY model`, accountID, now)
	if err != nil {
		s.lg.Warn("volc-free-quota: 聚合资源包失败", "account_id", accountID, "err", err)
		return nil
	}
	defer rows.Close()
	var out []PackageAggregate
	for rows.Next() {
		var a PackageAggregate
		var usedLocal int64
		if err := rows.Scan(&a.Model, &a.Name, &a.Unit,
			&a.InitialTotal, &a.LocalRemaining, &a.UsedAmount, &a.TotalAmount,
			&usedLocal); err != nil {
			continue
		}
		// 本地口径已用（initial_total - local_remaining）优先，billing used_amount 兜底。
		if a.InitialTotal > 0 {
			a.UsedAmount = usedLocal
		}
		a.Exhausted = a.InitialTotal > 0 && a.LocalRemaining <= 0
		if a.InitialTotal > 0 {
			pct := int((a.LocalRemaining * 100) / a.InitialTotal)
			if pct > 100 {
				pct = 100
			}
			if pct < 0 {
				pct = 0
			}
			a.Percentage = pct
		}
		if a.Unit == "" {
			a.Unit = "token"
		}
		out = append(out, a)
	}
	// 排序：未耗尽在前、剩余多的在前。
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Exhausted != out[j].Exhausted {
			return !out[i].Exhausted
		}
		return out[i].LocalRemaining > out[j].LocalRemaining
	})
	return out
}

// ListAggregates 一次性返回所有配置 + 每配置下按 model 聚合的资源包（卡片视图，v19）。
//
// 与 ListStatus 同构：volc_quota_packages 按 account_id 归属，同一账号下多渠道 Key
// 共享快照，前端按 channel 行展示，同账号各行回填同一份聚合结果。
func (s *Service) ListAggregates(ctx context.Context) (ListAggregateResponse, error) {
	configs, err := s.ListConfigs(ctx)
	if err != nil {
		return ListAggregateResponse{}, err
	}
	resp := ListAggregateResponse{Configs: make([]ConfigWithAggregates, 0, len(configs))}
	cache := make(map[string][]PackageAggregate)
	for _, cfg := range configs {
		aid := cfg.AccountID
		if aid == "" {
			aid = accountID(cfg.AccessKey)
		}
		aggs, ok := cache[aid]
		if !ok && aid != "" {
			aggs = s.aggregatePackagesForAccount(aid)
			cache[aid] = aggs
		}
		if aggs == nil {
			aggs = []PackageAggregate{}
		}
		resp.Configs = append(resp.Configs, ConfigWithAggregates{Config: cfg, Aggregates: aggs})
	}
	return resp, nil
}

// HandleListAggregates GET /api/volc-quota/aggregate
// 返回所有配置 + 每配置下按 model 聚合的资源包，卡片视图渲染用。
func (s *Service) HandleListAggregates(w http.ResponseWriter, r *http.Request) {
	resp, err := s.ListAggregates(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "查询聚合额度失败: "+err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, resp)
}

// ===== 配置读写 =====

// ListConfigs 返回所有配置（每条带渠道名 / key 名等展示字段，secret_key 永不出网）。
func (s *Service) ListConfigs(ctx context.Context) ([]Config, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT v.channel_id, v.account_id, v.access_key, v.secret_key_cipher, v.enabled, v.force_block,
		       v.last_synced_at, v.last_error, v.updated_at,
		       COALESCE(c.channel_name, ''), COALESCE(c.name, ''), COALESCE(c.base_url, '')
		FROM volc_quota_config v
		LEFT JOIN channels c ON c.id = v.channel_id
		ORDER BY v.channel_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Config
	for rows.Next() {
		var c Config
		var secretCipher string
		if err := rows.Scan(&c.ChannelID, &c.AccountID, &c.AccessKey, &secretCipher, &c.Enabled, &c.ForceBlock,
			&c.LastSyncedAt, &c.LastError, &c.UpdatedAt,
			&c.ChannelName, &c.KeyName, &c.BaseURL); err != nil {
			return nil, err
		}
		// SecretKey 不外带（前端编辑时不回显）。
		c.SecretKey = ""
		_ = secretCipher
		out = append(out, c)
	}
	return out, rows.Err()
}

// SaveConfigs 整体覆盖配置（用事务保证原子性）：
//
//   - SecretKey 非空 → 用 store.Encrypt 加密落库；空 → 保留库内既有密文。
//   - 不存在的渠道（channel_id 不在 channels 表）会被拦截，避免脏数据。
//   - 删除不再出现的 channel_id 行（PUT 整体替换语义）。
func (s *Service) SaveConfigs(ctx context.Context, configs []Config) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 校验：所有 channel_id 必须存在。
	if len(configs) > 0 {
		ids := make([]string, 0, len(configs))
		for _, c := range configs {
			if c.ChannelID == "" {
				return errors.New("channel_id 不能为空")
			}
			ids = append(ids, c.ChannelID)
		}
		placeholders := strings.Repeat("?,", len(ids))
		placeholders = strings.TrimSuffix(placeholders, ",")
		countArgs := make([]any, len(ids))
		for i, id := range ids {
			countArgs[i] = id
		}
		var known int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM channels WHERE id IN (`+placeholders+`)`, countArgs...).Scan(&known); err != nil {
			return fmt.Errorf("校验渠道存在失败: %w", err)
		}
		if known != len(ids) {
			return errors.New("配置中存在不存在的渠道 ID，请先在渠道列表中添加")
		}
	}

	for _, c := range configs {
		var cipher string
		if c.SecretKey != "" {
			enc, err := s.st.Encrypt(c.SecretKey)
			if err != nil {
				return fmt.Errorf("加密 secret_key 失败: %w", err)
			}
			cipher = enc
		} else {
			// 读现有密文保留。
			if err := tx.QueryRow(`SELECT secret_key_cipher FROM volc_quota_config WHERE channel_id = ?`, c.ChannelID).Scan(&cipher); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("读取既有密文失败: %w", err)
			}
		}
		// account_id = AK 指纹，免费额度按账号对齐。AK 每次保存都会重算（AK 改了账号即变）。
		aid := accountID(c.AccessKey)
		_, err := tx.ExecContext(ctx, `
			INSERT INTO volc_quota_config(channel_id, access_key, account_id, secret_key_cipher, enabled, force_block, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(channel_id) DO UPDATE SET
				access_key = excluded.access_key,
				account_id = excluded.account_id,
				secret_key_cipher = CASE WHEN excluded.secret_key_cipher != '' THEN excluded.secret_key_cipher ELSE volc_quota_config.secret_key_cipher END,
				enabled = excluded.enabled,
				force_block = excluded.force_block,
				updated_at = excluded.updated_at`,
			c.ChannelID, c.AccessKey, aid, cipher, c.Enabled, c.ForceBlock, now)
		if err != nil {
			return fmt.Errorf("upsert volc_quota_config: %w", err)
		}
	}

	// 删除未保留的 channel_id 行（PUT 整体替换语义）。
	keepIDs := make([]string, 0, len(configs))
	for _, c := range configs {
		keepIDs = append(keepIDs, c.ChannelID)
	}
	if len(keepIDs) == 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM volc_quota_config`); err != nil {
			return err
		}
		// 账号维度快照：全部配置删除后，不再有任何账号被追踪，清空快照/统计。
		if _, err := tx.ExecContext(ctx, `DELETE FROM volc_quota_usage`); err != nil {
			return err
		}
	} else {
		placeholders := strings.Repeat("?,", len(keepIDs))
		placeholders = strings.TrimSuffix(placeholders, ",")
		args := make([]any, len(keepIDs))
		for i, id := range keepIDs {
			args[i] = id
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM volc_quota_config WHERE channel_id NOT IN (`+placeholders+`)`, args...); err != nil {
			return err
		}
		// 清理不再被任何剩余配置引用的账号的孤儿快照/统计。
		if _, err := tx.ExecContext(ctx, `DELETE FROM volc_quota_usage WHERE account_id NOT IN (SELECT DISTINCT account_id FROM volc_quota_config WHERE account_id != '')`); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListUsage 返回某账号的请求使用统计（account_id 维度）。
func (s *Service) ListUsage(ctx context.Context, accountID string) ([]Usage, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT account_id, model, use_count, last_used_at
		FROM volc_quota_usage WHERE account_id = ? ORDER BY use_count DESC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Usage
	for rows.Next() {
		var u Usage
		if err := rows.Scan(&u.AccountID, &u.Model, &u.UseCount, &u.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ListPackages 返回某账号的资源包逐条明细（account_id 维度，v14）。
// 按 available_amount 降序，耗尽/过期排后面，前端一眼看到"哪个模型还有额度"。
func (s *Service) ListPackages(ctx context.Context, accountID string) ([]Package, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT account_id, instance_no, product, product_name, configuration_code, configuration_name,
		       model, total_amount, available_amount, used_amount, initial_total, local_remaining,
		       unit, status, effective_time, expiry_time, synced_at
		FROM volc_quota_packages WHERE account_id = ?
		ORDER BY (status = 'Effective') DESC, available_amount DESC, configuration_name`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Package
	for rows.Next() {
		var p Package
		if err := rows.Scan(&p.AccountID, &p.InstanceNo, &p.Product, &p.ProductName,
			&p.ConfigurationCode, &p.ConfigurationName, &p.Model, &p.TotalAmount, &p.AvailableAmount,
			&p.UsedAmount, &p.InitialTotal, &p.LocalRemaining, &p.Unit, &p.Status,
			&p.EffectiveTime, &p.ExpiryTime, &p.SyncedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ===== 手动 + 定时刷新 =====

// Refresh 刷新额度：channelID 非空只刷该渠道；否则全量遍历所有 enabled 配置。
// force 为 true 时强制刷新 local_remaining 到远程值（覆盖"只降不升"防回弹逻辑）。
//
// 关键：免费额度按火山账号（AK）对齐，因此刷新前先把 enabled 配置按 account_id 去重，
// 每个账号只调一次火山接口（同账号多 Key 共享额度，重复查询浪费 QPS 且结果一致）。
// 返回 RefreshResult 给前端展示：失败/禁用条数。
func (s *Service) Refresh(ctx context.Context, channelID string, force bool) (RefreshResult, error) {
	// in-flight 互斥（rev2 审计 D）：手动 HTTP 刷新与后台 ticker 并发时只放行一个，
	// 避免 billing 双倍 QPS（接口上限 10，会 429）与 local_remaining 读-算-写互相覆盖。
	s.bgMu.Lock()
	if s.refreshing {
		s.bgMu.Unlock()
		return RefreshResult{}, errRefreshInFlight
	}
	s.refreshing = true
	s.bgMu.Unlock()
	defer func() {
		s.bgMu.Lock()
		s.refreshing = false
		s.bgMu.Unlock()
	}()
	result := RefreshResult{RefreshedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	configs, err := s.ListConfigs(ctx)
	if err != nil {
		return result, err
	}
	// 按账号去重：同一 account_id 只刷一次（取第一条 enabled 配置的 AK/SK 拉额度）。
	accountSeen := make(map[string]bool)
	for _, cfg := range configs {
		if channelID != "" && cfg.ChannelID != channelID {
			continue
		}
		if !cfg.Enabled {
			continue
		}
		if cfg.AccountID == "" {
			// 旧数据（v11 迁移前的行）或 AK 为空：按 AK 现算指纹，并写回 DB 修复，
			// 否则后面 `WHERE account_id = ?` 更新 last_error/last_synced_at 永远匹配不到行
			// （UI 上会残留僵尸 last_error、last_synced_at 不更新）。
			cfg.AccountID = accountID(cfg.AccessKey)
			if cfg.AccountID != "" {
				_, _ = s.db.ExecContext(ctx,
					`UPDATE volc_quota_config SET account_id = ? WHERE channel_id = ? AND (account_id = '' OR account_id IS NULL)`,
					cfg.AccountID, cfg.ChannelID)
			}
		}
		if cfg.AccountID == "" {
			continue // 连 AK 都没有，无法查询
		}
		if accountSeen[cfg.AccountID] {
			// 同账号的第二个 Key：额度已经刷过，跳过（但结果仍归属该账号）。
			continue
		}
		accountSeen[cfg.AccountID] = true
		result.ConfigsChecked++
		disabled, rerr := s.refreshOne(ctx, cfg, force)
		if rerr != nil {
			s.lg.Warn("volc-free-quota: 刷新失败", "account_id", cfg.AccountID, "channel_id", cfg.ChannelID, "err", rerr)
			// 记录到 last_error 让 UI 看到；失败的账号不会触发自动 disable（避免歧义）。
			now := time.Now().UTC().Format(time.RFC3339Nano)
			_, _ = s.db.ExecContext(ctx, `UPDATE volc_quota_config SET last_error = ?, updated_at = ? WHERE account_id = ?`,
				rerr.Error(), now, cfg.AccountID)
			result.FailedChannels = append(result.FailedChannels, cfg.ChannelID)
			// 单渠道刷新失败直接抛错：手动刷新时 HTTP 返回非 200 + 明确错误，
			// 前端 toast 直接可见（而不只是藏在响应体/数据库里）；后台定时刷新靠
			// StartBackgroundRefresh 捕获 error 记日志，不会中断后续渠道。
			return result, fmt.Errorf("刷新账号 %s 失败: %w", cfg.AccountID, rerr)
		}
		// 成功 → 清该账号所有配置的 last_error。
		now := time.Now().UTC().Format(time.RFC3339Nano)
		_, _ = s.db.ExecContext(ctx, `UPDATE volc_quota_config SET last_synced_at = ?, last_error = '', updated_at = ? WHERE account_id = ?`,
			now, now, cfg.AccountID)
		for _, m := range disabled {
			result.DisabledModels = append(result.DisabledModels, m)
		}
	}
	return result, nil
}

// refreshOne 刷新一个账号的额度（v17 起只写 volc_quota_packages 逐条明细，删除
// 了 volc_quota_models 聚合表）。
//
// force 为 true 时，local_remaining 直接取远程 available_amount（覆盖防回弹），
// 用于手动"强制刷新"把本地余额拉回远程权威值。
//
// 流程：解密 secret_key → 调 SDK → 过滤方舟免费资源包 → 逐条 UPSERT volc_quota_packages
// → 检测 Available<=0 资源包对应的 API 模型，对该账号关联的所有渠道 Key 写
// model_states（冷却到次日 0:00）。
func (s *Service) refreshOne(ctx context.Context, cfg Config, force bool) ([]string, error) {
	cipher := ""
	if err := s.db.QueryRow(`SELECT secret_key_cipher FROM volc_quota_config WHERE channel_id = ?`, cfg.ChannelID).Scan(&cipher); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("配置不存在")
		}
		return nil, err
	}
	plain, err := s.st.Decrypt(cipher)
	if err != nil || plain == "" {
		return nil, errors.New("secret_key 缺失或解密失败")
	}
	// 拉取资源包。
	pkgs, err := s.client.FetchPackages(ctx, cfg.AccessKey, plain)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339Nano)
	// 账号指纹：该账号的额度快照归属。
	aid := cfg.AccountID
	if aid == "" {
		aid = accountID(cfg.AccessKey)
	}

	// v17：扣减/拦截/UI 都从 packages 行直接读，不再维护聚合表。
	// 禁用判据（rev2）：不再"任何资源包 available=0 即禁用"（单包判据会误禁用多包模型），
	// UPSERT 完成后按聚合余额统一判定（syncModelStatesByAggregate）。

	// 预取旧值：instance_no -> (initial_total, local_remaining, synced_at)。
	// computeLocalRemaining 需要旧值（跨天判定用 synced_at），不能只靠 UPSERT 内部 CASE。
	oldRows, err := s.db.QueryContext(ctx,
		`SELECT instance_no, initial_total, local_remaining, synced_at FROM volc_quota_packages WHERE account_id = ?`, aid)
	if err != nil {
		return nil, err
	}
	type oldPkg struct {
		init   int64
		local  int64
		synced time.Time
	}
	oldByInst := make(map[string]oldPkg)
	for oldRows.Next() {
		var inst string
		var op oldPkg
		var syncedStr string
		if err := oldRows.Scan(&inst, &op.init, &op.local, &syncedStr); err == nil {
			// synced_at 解析失败（脏数据/NULL）→ 视为"今天"（保守：不触发跨天复活，
			// 避免 billing 滞后把今天扣完的包拉回）。
			if t, perr := time.Parse(time.RFC3339Nano, syncedStr); perr == nil {
				op.synced = t
			} else {
				op.synced = now
			}
			oldByInst[inst] = op
		}
	}
	if err := oldRows.Err(); err != nil {
		oldRows.Close()
		return nil, err
	}
	oldRows.Close()

	// 逐条 UPSERT 资源包明细（volc_quota_packages）：
	//   - model 用 configuration_code 提取名（去掉资源包类型后缀），扣减锚点，
	//     如 "DeepSeek_V4_flash_0731_data_collaboration_resource_pack" → "deepseek-v4-flash-0731"
	//   - initial_total 只在首次写入（定下扣减基准后不动，用户要求保留）。
	//   - local_remaining（rev2）：Go 侧 computeLocalRemaining 按旧值 + billing available 算好
	//     传参（首次=avail 建底数 / 今天扣完保持 0 / 跨天+avail>0 记 avail / 下降校准 / 上升保持）。
	//   - synced_at（rev2 审计 B，P0）：仅首次写入设置，刷新保留；只有扣减（decrementLocalRemaining）
	//     才更新。否则 ticker 每 15min 刷新会把它覆盖为"今天"，跨天判定永远失效、模型永不复活。
	//   - 不再 DELETE 重建（billing API 返回全量，但本地余额必须跨刷新保留）。
	//   - 本次 billing 已不返回的旧包（过期/删除）保留不动，由旧数据清理兜底。
	for _, p := range pkgs {
		if !p.looksLikeArkFreePackage() {
			continue
		}
		if p.InstanceNo == "" {
			continue // 无唯一标识的包不入明细（理论上不会发生）
		}
		totalAmt := parseAmount(p.TotalAmount)
		availAmt := parseAmount(p.AvailableAmount)
		usedAmt := parseAmount(p.UsedAmount)
		old := oldByInst[p.InstanceNo]
		// force=true：local_remaining 直接取远程 available_amount，覆盖"只降不升"防回弹，
		// 把本地余额强制拉回远程权威值（用户显式要求，风险自负）。
		newLocal := availAmt
		if !force {
			newLocal = computeLocalRemaining(old.init, old.local, old.synced, availAmt, now)
		}
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO volc_quota_packages(account_id, instance_no, product, product_name, configuration_code,
			       configuration_name, model, total_amount, available_amount, used_amount, unit, status,
			       effective_time, expiry_time, initial_total, local_remaining, synced_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(account_id, instance_no) DO UPDATE SET
				product = excluded.product,
				product_name = excluded.product_name,
				configuration_code = excluded.configuration_code,
				configuration_name = excluded.configuration_name,
				model = excluded.model,
				total_amount = excluded.total_amount,
				available_amount = excluded.available_amount,
				used_amount = excluded.used_amount,
				unit = excluded.unit,
				status = excluded.status,
				effective_time = excluded.effective_time,
				expiry_time = excluded.expiry_time,
				-- initial_total 只在首次写入（定基准），后续不动
				initial_total = CASE WHEN volc_quota_packages.initial_total = 0 THEN excluded.initial_total ELSE volc_quota_packages.initial_total END,
				-- local_remaining 已由 computeLocalRemaining 算好（跨天/校准/防回弹）
				local_remaining = excluded.local_remaining,
				-- synced_at 仅首次写入；刷新保留（扣减才更新），保跨天判定有效
				synced_at = CASE WHEN volc_quota_packages.initial_total = 0 THEN excluded.synced_at ELSE volc_quota_packages.synced_at END`,
			aid, p.InstanceNo, p.Product, p.ProductName, p.ConfigurationCode, p.ConfigurationName,
			modelNameFromConfigCode(p.ConfigurationCode, p.Product),
			totalAmt, availAmt, usedAmt, p.Unit, p.Status, p.EffectiveTime, p.ExpiryTime,
			totalAmt, newLocal, nowStr); err != nil {
			return nil, err
		}
	}
	// 聚合双向同步（唯一判定入口）：用完 → 冷却；恢复（>45 万）→ 解除；中间态不动。
	return s.syncModelStatesByAggregate(ctx, aid), nil
}

// disabledSorted 从 map 收集去重的 API 模型名，按字符串排序（稳定输出）。
func disabledSorted(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// inactiveQuotaStatuses 非活动资源包状态（聚合/扣减/拦截统一排除，口径一致）。
// billing API 的 status 取值：Effective / UsedUp / Expired / NotEffective /
// FailedToCreate / Refunded。过期/失效/退款包的残留余额会撑住聚合判据，必须排除。
const inactiveQuotaStatuses = `('Expired','NotEffective','FailedToCreate','Refunded')`

// activePackageCond 聚合/扣减/拦截共用的"活跃包"过滤条件（status + 到期时间）。
// 运行期问题（code review P1）：billing 客户端只拉 Effective/UsedUp，refreshOne 从不把
// 本地行标成 Expired——某包到期后 billing 不再返回，本地行仍保留 Effective+正余额，
// 若不按 expiry_time 过滤会永久计入聚合（既不判耗尽、扣减还继续吃幽灵余额）。
// expiry_time 由 billing 返回，实测全非空且 RFC3339 格式（字典序 == 时间序）；
// 空串防御性视为活跃（不排除）。
const activePackageCond = ` AND status NOT IN ` + inactiveQuotaStatuses + ` AND (expiry_time = '' OR expiry_time >= ?)`

// aggregateLocalRemaining 返回该账号下每个 quota model 的聚合本地余额（SUM(local_remaining)）。
//
// 一个模型可能挂多个资源包（billing 按 InstanceNo 逐条返回），判断必须按 model 聚合，
// 不能看单个包（如 deepseek-v4-flash-0731 有 7 个包，5 个 UsedUp + 2 个活包，聚合 201 万）。
// 口径与 decrementLocalRemaining 扣减、checkAllCandidatesExhausted 拦截一致：
//   - 只统计 initial_total > 0（有扣减基准的包）
//   - 排除非活动状态（Expired 等）与已到期（expiry_time < now）的包
func (s *Service) aggregateLocalRemaining(accountID string) map[string]int64 {
	out := make(map[string]int64)
	if accountID == "" {
		return out
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := s.db.Query(`SELECT model, SUM(local_remaining) FROM volc_quota_packages
		WHERE account_id = ? AND initial_total > 0`+activePackageCond+`
		GROUP BY model`, accountID, now)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var m string
		var sum int64
		if err := rows.Scan(&m, &sum); err == nil && m != "" {
			out[m] = sum
		}
	}
	return out
}

// channelsForAccount 返回该账号（account_id）关联的所有渠道 Key ID。
// 免费额度按账号对齐：账号下任一 Key 耗尽，禁用应作用于该账号所有 Key 的对应模型。
func (s *Service) channelsForAccount(accountID string) []string {
	rows, err := s.db.Query(`SELECT channel_id FROM volc_quota_config WHERE account_id = ?`, accountID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil && id != "" {
			out = append(out, id)
		}
	}
	return out
}

// channelsAPIModels 汇总一批渠道 Key 的已配 API 模型（去重并集）。
// 用于把方舟免费资源包 product 名称归一化后映射到真实请求中的模型名。空表示该账号
// 还没有任何已知模型（首次刷新），此时禁用匹配会跳过；不会乱禁用。
func (s *Service) channelsAPIModels(ctx context.Context, channelIDs []string) ([]string, error) {
	if len(channelIDs) == 0 {
		return nil, nil
	}
	placeholders := strings.Repeat("?,", len(channelIDs))
	placeholders = strings.TrimSuffix(placeholders, ",")
	args := make([]any, len(channelIDs))
	for i, id := range channelIDs {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT model FROM channel_models WHERE channel_id IN (`+placeholders+`) AND enabled = 1`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// matchQuotaToAPIModels 根据 product 归一化标签猜出真实 API 模型名集合。
//
// 策略（按序尝试，第一个命中即加入）：
//
//  1. 归一化双向包含：normalize(model) 包含 normalize(quota) 或反之。
//  2. 产品名含 doubao 模型版本号（如 1.5/1.6）则提取数字，apiModels 中包含
//     该数字前 3 段的也认作命中（兼容 product 简写 "doubao-pro-32k" 与 API 模型名
//     "doubao-1-5-pro-32k-250115" 的差异）。
//
// 返回去重后的 apiModels 子集。
func (s *Service) matchQuotaToAPIModels(quota string, apiModels []string) []string {
	q := normalizeModelName(quota)
	if q == "" || len(apiModels) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	for _, m := range apiModels {
		if seen[m] {
			continue
		}
		if matchOne(q, m) {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

// matchOne 单模型归一化后双向包含判定；包含失败时退化为"显著 token 交集"兜底。
//
// 覆盖场景：资源包 product 名 "Doubao-pro-32k"（归一化 "doubao-pro-32k"）与真实
// API 模型名 "doubao-1-5-pro-32k-250115"（归一化不变）——双向包含都不成立，
// 但两者共享显著 token "32k"，据此命中。显著 token 见 shareSignificantToken。
func matchOne(quotaNormalized, apiModel string) bool {
	a := normalizeModelName(apiModel)
	if a == "" {
		return false
	}
	if strings.Contains(a, quotaNormalized) || strings.Contains(quotaNormalized, a) {
		return true
	}
	return shareSignificantToken(quotaNormalized, a)
}

// shareSignificantToken 两个归一化串是否共享至少一个显著 token 且字母序列一致。
//
// 显著 token = 含至少一个数字且总长 >= 2 的连续段（如 "32k"、"128k"、"250115"），
// 单字符数字（版本号 "1"、"5"）排除；字母序列 = 纯字母连续段序列（如
// "doubao-pro-32k" → [doubao pro]）。
//
// 双重约束避免过宽：数字交集非空保证"同规格"，字母序列一致保证"同型号"。
// 用于 product 名与 API 模型名之间"中间夹版本号"的差异匹配。
func shareSignificantToken(a, b string) bool {
	ta := significantTokens(a)
	tb := significantTokens(b)
	hit := false
	for _, x := range ta {
		for _, y := range tb {
			if x == y || sameDateToken(x, y) {
				hit = true
				break
			}
		}
		if hit {
			break
		}
	}
	if !hit {
		return false
	}
	return equalStrings(filterModifiers(alphaTokens(a)), filterModifiers(alphaTokens(b)))
}

// 模型名修饰段：与型号无关，比较字母序列时忽略（API 模型名比 resource 包 code 提取名
// 多出的正式版/预览标记）。如 "deepseek-v4-flash-ga-260731" 的 "ga"。
// 注意：lite/turbo/pro 等是型号的一部分，绝不能过滤。
var modelModifierSegs = map[string]bool{
	"ga":      true,
	"preview": true,
	"latest":  true,
	"beta":    true,
}

// filterModifiers 从字母段序列中去掉模型修饰词（ga/preview/latest/beta）。
func filterModifiers(segs []string) []string {
	out := make([]string, 0, len(segs))
	for _, s := range segs {
		if modelModifierSegs[s] {
			continue
		}
		out = append(out, s)
	}
	return out
}

// sameDateToken 判断两个纯数字 token 是否是同一天（4 位 MMdd vs 6 位 YYMMdd）。
//
// 场景：resource 包 code 用 "0731"（月日），API 模型名用 "260731"（年月日），
// 都是 2026-07-31。判定规则：
//   - 同为 4 位：相等才算（如 "0731" vs "0731"）。
//   - 同为 6 位：相等才算（如 "260731" vs "260731"）。
//   - 4 位 vs 6 位：6 位去掉前 2 位年份后与 4 位相等才算（"0731" ↔ "260731"）。
//
// 非 4/6 位长度的纯数字 token（如 "32"、"128"）直接 false。
func sameDateToken(a, b string) bool {
	// 必须纯数字。
	for i := 0; i < len(a); i++ {
		if !isDigit(a[i]) {
			return false
		}
	}
	for i := 0; i < len(b); i++ {
		if !isDigit(b[i]) {
			return false
		}
	}
	la, lb := len(a), len(b)
	switch {
	case la == 4 && lb == 4:
		return a == b
	case la == 6 && lb == 6:
		return a == b
	case la == 4 && lb == 6:
		return a == b[2:] // "0731" ↔ "260731"（去掉 "26"）
	case la == 6 && lb == 4:
		return a[2:] == b
	default:
		return false
	}
}

// alphaTokens 提取串中的纯字母连续段（顺序保持，去重）。
func alphaTokens(s string) []string {
	var out []string
	for i := 0; i < len(s); {
		if !isAlpha(s[i]) {
			i++
			continue
		}
		j := i
		for j < len(s) && isAlpha(s[j]) {
			j++
		}
		out = append(out, s[i:j])
		i = j
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// significantTokens 提取串中的显著 token（数字段，数字后紧跟的单位字母 k/m 一并纳入）。
//
// 示例："doubao-1-5-pro-32k-250115" → ["32k", "250115"]；"doubao-pro-32k" → ["32k"]。
// 单字符数字（版本号 "1"、"5"）排除，避免过宽误匹配。
func significantTokens(s string) []string {
	var out []string
	for i := 0; i < len(s); {
		if !isDigit(s[i]) {
			i++
			continue
		}
		j := i
		for j < len(s) && isDigit(s[j]) {
			j++
		}
		// 数字后紧跟单个单位字母（k/m/g）一并纳入。
		if j < len(s) && isAlpha(s[j]) {
			j++
		}
		if j-i >= 2 {
			out = append(out, s[i:j])
		}
		i = j
	}
	return out
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isAlpha(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

// modelNameFromConfigCode 从 configuration_code 提取模型名（扣减/拦截的匹配锚点）。
//
// 例："DeepSeek_V4_flash_0731_data_collaboration_resource_pack" → "deepseek-v4-flash-0731"
//      "Doubao_Seed3D_1.0_pack_free_infer" → "doubao-seed3d-1.0"
//      "ym-rodin-gen2-free" → "ym-rodin-gen2"
//
// 策略：按 "_" 切段，从后往前逐个去掉"资源包类型后缀段"（段本身或其组合，
// 如 data_collaboration、resource_pack、free_infer 等），剩余段用 "-" 拼接 + 归一化。
// 全部被吃掉（理论上不会）时退回 product 归一化。
func modelNameFromConfigCode(configCode, product string) string {
	if configCode != "" {
		parts := strings.Split(configCode, "_")
		// 从尾部开始吞"资源包类型后缀段"（_ 分隔的整段：data/collaboration/resource/pack/free/infer...）。
		for len(parts) > 1 {
			tail := parts[len(parts)-1]
			if !isConfigCodeSuffix(tail) {
				break
			}
			parts = parts[:len(parts)-1]
		}
		joined := strings.Join(parts, "-")
		// 处理 "-" 分隔的尾段后缀（ym-rodin-gen2-free → ym-rodin-gen2）：
		// 只去掉尾部 -free / -infer / -pack 这类明确后缀，避免误伤模型名（如 doubao-seed-code）。
		for {
			idx := strings.LastIndexByte(joined, '-')
			if idx <= 0 {
				break
			}
			tail := joined[idx+1:]
			if !isConfigCodeSuffix(tail) {
				break
			}
			joined = joined[:idx]
		}
		if n := normalizeModelName(joined); n != "" {
			return n
		}
	}
	// 回退：product 归一化（ark_bd / ark_open_source_llm）。
	return normalizeModelName(product)
}

// isConfigCodeSuffix 判断单段是否是资源包类型后缀。
// 注意：code 不是后缀（Doubao_Seed_2.0_code_pack 的 code 是模型名一部分）。
// res 是 resource 的缩写变体（如 "..._free_infer_res" / "..._free_inference_res"），
// 作为独立尾段必须吞掉，否则模型名会残留 "-infer-res" 这类错误后缀。
func isConfigCodeSuffix(seg string) bool {
	switch strings.ToLower(seg) {
	case "data", "collaboration", "resource", "res", "pack", "free", "inference", "infer":
		return true
	}
	return false
}

// disableModelForFreeQuota 在 model_states 写入免费额度耗尽导致的临时禁用：
//
//   - status='cooling'，让 model-health 的 CheckNow 在 disabled_until 到期后自动恢复。
//   - manual_enabled 保留原值（CONFLICT 不更新）；用户手动启停的偏好不被覆盖。
//   - last_error 写 "模型免费额度用完"，UI 在 model-status 页面会展示这条信息。
//   - 冷却到 untilNextDayRecovery（次日 12:00 北京时间兜底；正常由定时刷新提前 re-enable）。
//   - fail_count 不累加：免费额度不是健康失败（rev2 审计 F），冷却次数无意义且会无限增长。
func (s *Service) disableModelForFreeQuota(ctx context.Context, channelID, model string, until time.Time) error {
	untilStr := until.UTC().Format(time.RFC3339Nano)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO model_states(channel_id, model, manual_enabled, status, disabled_until, last_error, last_failure_class, last_checked_at, updated_at)
		VALUES (?, ?, 1, 'cooling', ?, '模型免费额度用完', 'free_quota_exhausted', ?, ?)
		ON CONFLICT(channel_id, model) DO UPDATE SET
			status='cooling',
			disabled_until=excluded.disabled_until,
			last_error=excluded.last_error,
			last_failure_class=excluded.last_failure_class,
			last_checked_at=excluded.last_checked_at,
			updated_at=excluded.updated_at`,
		channelID, model, untilStr, now, now)
	return err
}

// reEnableModelForFreeQuota 额度恢复（聚合 > 阈值）后解除该模型的免费额度冷却：
//
//   - 只解除「免费额度耗尽」写入的冷却（last_failure_class='free_quota_exhausted'
//     或旧数据 last_error 匹配），不碰 model-health 因真实上游故障（upstream_error /
//     5xx）写的冷却——避免与 model-health 冷却互搏振荡（解除→再冷却→再解除）。
//   - 不碰用户手动禁用（manual_enabled=0 行不动）。
//   - fail_count 清零：免费额度恢复后旧的冷却计数无意义（disableModelForFreeQuota
//     已不再累加 fail_count，这里清掉存量旧值）。
func (s *Service) reEnableModelForFreeQuota(ctx context.Context, channelID, model string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
		UPDATE model_states SET status='available', disabled_until=NULL, last_error='', last_failure_class='', fail_count=0, updated_at=?
		WHERE channel_id=? AND model=? AND manual_enabled=1 AND status != 'available'
		  AND (last_failure_class='free_quota_exhausted' OR last_error='模型免费额度用完')`,
		now, channelID, model)
	return err
}

// beijingTZ 北京时间固定 +8 时区（中国无夏令时；FixedZone 零依赖，Windows 不依赖系统 tzdata）。
var beijingTZ = time.FixedZone("Asia/Shanghai", 8*3600)

// untilNextDayRecovery 返回额度恢复兜底时间：次日 12:00（北京时间）。
//
// 实测火山免费额度每日恢复时间不固定（8:00~11:30），12:00 留 30min 缓冲。
// 正常由 15min 定时刷新检测 billing 恢复后提前 re-enable（见 syncModelStatesByAggregate），
// 此值只是刷新全挂时 model-health CheckNow 的兜底恢复点。
func untilNextDayRecovery() time.Time {
	now := time.Now().In(beijingTZ)
	return time.Date(now.Year(), now.Month(), now.Day()+1, 12, 0, 0, 0, beijingTZ)
}

// sameBeijingDay 两个时间是否同一北京日期（跨天判定用）。
func sameBeijingDay(a, b time.Time) bool {
	ab := a.In(beijingTZ)
	bb := b.In(beijingTZ)
	return ab.Year() == bb.Year() && ab.YearDay() == bb.YearDay()
}

// computeLocalRemaining 包级余额校准纯函数（账本级，只记数，不做模型级裁决）。
//
// 输入：oldInitial（旧初始总额，0=首次写入）、oldLocal（旧本地余额）、
// oldSyncedAt（最近一次本地扣减时间，北京时间跨天判定用）、avail（billing available）、now（当前时间）。
//
// 分支：
//  1. 首次写入（oldInitial == 0）→ = avail（建底数，全量额度）
//  2. 已耗尽（oldLocal <= 0）：
//       - 扣减发生在今天（北京日期相同）→ 保持 0（billing 可能结算滞后，不记）
//       - 扣减发生在昨天或更早（跨天）且 avail > 0 → = avail（billing 有值即记上；
//         是否"算恢复"由聚合层 config.VolcQuotaReviveThreshold 裁决）
//       - 否则 → 保持 0
//  3. avail < oldLocal → = avail（billing 权威下降校准，防本地漏扣）
//  4. 其余（billing 上升，含结算滞后回升）→ 保持 oldLocal（本地扣减为准，防回弹）
func computeLocalRemaining(oldInitial, oldLocal int64, oldSyncedAt time.Time, avail int64, now time.Time) int64 {
	if oldInitial == 0 {
		return avail
	}
	if oldLocal <= 0 {
		if !sameBeijingDay(oldSyncedAt, now) && avail > 0 {
			return avail
		}
		return 0
	}
	if avail < oldLocal {
		return avail
	}
	return oldLocal
}

// ===== ListStatus 聚合（设置页用） =====

// ListStatus 一次性返回所有配置 + 每条配置下（按账号聚合）的免费模型 + 使用统计 + 资源包明细。
//
// 注意：volc_quota_usage / volc_quota_packages 按 account_id 归属，
// 同一账号下多渠道 Key 共享快照；前端按 channel 行展示，同账号各行回填同一份数据。
func (s *Service) ListStatus(ctx context.Context) (ListStatusResponse, error) {
	configs, err := s.ListConfigs(ctx)
	if err != nil {
		return ListStatusResponse{}, err
	}
	resp := ListStatusResponse{Configs: make([]ConfigWithDetails, 0, len(configs))}
	usageCache := make(map[string][]Usage)
	packagesCache := make(map[string][]Package)
	for _, cfg := range configs {
		aid := cfg.AccountID
		if aid == "" {
			aid = accountID(cfg.AccessKey)
		}
		usage, ok := usageCache[aid]
		if !ok && aid != "" {
			usage, err = s.ListUsage(ctx, aid)
			if err != nil {
				return resp, err
			}
			usageCache[aid] = usage
		}
		if usage == nil {
			usage = []Usage{}
		}
		pkgs, ok := packagesCache[aid]
		if !ok && aid != "" {
			pkgs, err = s.ListPackages(ctx, aid)
			if err != nil {
				return resp, err
			}
			packagesCache[aid] = pkgs
		}
		if pkgs == nil {
			pkgs = []Package{}
		}
		resp.Configs = append(resp.Configs, ConfigWithDetails{
			Config:   cfg,
			Usage:    usage,
			Packages: pkgs,
		})
	}
	return resp, nil
}

// ===== 辅助 =====

// parseAmount 把 string 形式的额度解析为整数（截断小数）。
//
// SDK 返回值可能为小数（如 "100.00"）；解析失败返回 0（UI 显 0，下一次刷新会自动修复）。
func parseAmount(s string) int64 {
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int64(f)
}

// ===== HTTP Handlers（管理后台路由，AuthSession 由 servercore 自动包装） =====

// HandleListStatus GET /api/volc-quota/status
// 返回所有配置 + 每条配置下的免费模型列表 + 使用记录，设置页一次性渲染。
func (s *Service) HandleListStatus(w http.ResponseWriter, r *http.Request) {
	resp, err := s.ListStatus(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "查询额度状态失败: "+err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, resp)
}

// HandleRecentUsage GET /api/volc-quota/recent-usage?channel_id=&minutes=10
// 返回某渠道（base_url）近 N 分钟的请求日志状态，供「刷新远程」前的安全提示用。
// channel_id 为空时统计全部火山引擎渠道。
func (s *Service) HandleRecentUsage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	channelID := q.Get("channel_id")
	minutes := 10
	if v := q.Get("minutes"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			minutes = n
		}
	}
	resp, err := s.recentUsage(r.Context(), channelID, minutes)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "查询请求日志失败: "+err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, resp)
}

// recentUsage 统计某渠道（base_url 维度）近 N 分钟的 route_attempts 请求记录。
//
//   - channelID 为空 → 统计所有 base_url 命中 ark.cn-beijing.volces.com 的渠道。
//   - 否则按该渠道的 base_url 统计（同一 base_url 下可能有多个 Key，共享同一批请求日志）。
//   - started_at 存 RFC3339Nano（UTC），直接用字符串比较（字典序 == 时间序）。
func (s *Service) recentUsage(ctx context.Context, channelID string, minutes int) (RecentUsageResponse, error) {
	resp := RecentUsageResponse{ChannelID: channelID, Minutes: minutes}

	// 确定 base_url 范围。
	var baseURL string
	if channelID != "" {
		if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(base_url, '') FROM channels WHERE id = ?`, channelID).Scan(&baseURL); err != nil {
			return resp, err
		}
	}
	resp.BaseURL = baseURL

	// 统计近 N 分钟的请求条数 + 最后请求时间。
	threshold := time.Now().UTC().Add(-time.Duration(minutes) * time.Minute).Format(time.RFC3339Nano)
	var (
		count   int
		lastAt  string
		scanErr error
	)
	if baseURL != "" {
		scanErr = s.db.QueryRowContext(ctx, `
			SELECT COUNT(*), COALESCE(MAX(a.started_at), '')
			FROM route_attempts a
			JOIN channels c ON c.id = a.channel_id
			WHERE c.base_url = ? AND a.started_at >= ?`,
			baseURL, threshold).Scan(&count, &lastAt)
	} else {
		// 全量：所有火山引擎渠道。
		scanErr = s.db.QueryRowContext(ctx, `
			SELECT COUNT(*), COALESCE(MAX(a.started_at), '')
			FROM route_attempts a
			JOIN channels c ON c.id = a.channel_id
			WHERE c.base_url LIKE '%ark.cn-beijing.volces.com%' AND a.started_at >= ?`,
			threshold).Scan(&count, &lastAt)
	}
	if scanErr != nil {
		return resp, scanErr
	}
	resp.RequestCount = count
	resp.LastRequestAt = lastAt
	resp.HasRecent = count > 0
	return resp, nil
}

// HandleSaveConfigs PUT /api/volc-quota/config
// 批量覆盖配置（原子事务）；SecretKey 为空字符串时保留库内既有密文。
// 请求体：{"configs":[{channel_id, access_key, secret_key, enabled}, ...]}
func (s *Service) HandleSaveConfigs(w http.ResponseWriter, r *http.Request) {
	var req SaveConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "请求体解析失败: "+err.Error())
		return
	}
	if err := s.SaveConfigs(r.Context(), req.Configs); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// HandleRefresh POST /api/volc-quota/refresh
// 手动刷新额度：请求体 {"channel_id":"...", "force":true}。
//   - channel_id 只刷该渠道；缺省/空则全量刷新。
//   - force=true 强制把 local_remaining 拉回远程 available_amount（覆盖防回弹），
//     前端"强制刷新"选项使用；缺省/非 true 保持原有只降不升语义。
// 返回 RefreshResult（含本次禁用/失败明细），前端据此展示。
// 任一条渠道刷新失败 → HTTP 4xx/5xx + 明确错误（不再静默 200），前端 toast 直接可见。
func (s *Service) HandleRefresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ChannelID string `json:"channel_id"`
		Force     bool   `json:"force"`
	}
	// 空 body 也允许（全量刷新）。
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	result, err := s.Refresh(r.Context(), req.ChannelID, req.Force)
	if err != nil {
		// 后台定时刷新占用中 → 409（可重试），避免 502 误导为上游故障。
		if errors.Is(err, errRefreshInFlight) {
			s.writeError(w, http.StatusConflict, "刷新额度失败: "+err.Error())
			return
		}
		s.writeError(w, http.StatusBadGateway, "刷新额度失败: "+err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

// writeJSON 写出标准 JSON 响应。
func (s *Service) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError 写出标准错误 JSON（与 admin-api 风格一致）。
func (s *Service) writeError(w http.ResponseWriter, status int, message string) {
	s.writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "invalid_request_error",
		},
	})
}
