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
	"strconv"
	"strings"
	"sync"
	"time"

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
	client *volcBillingClient

	// inMemoryChannels 是 doRefresh 期间缓存的渠道 ID→渠道记录，避免刷新热路径再查 channels。
	// 缓存有效到下次显式失效（channel 增删）——插件自身不实现失效，靠定期重建。
	mu                sync.Mutex
	lastChannelCache  map[string]channelSnapshot
	lastChannelCacheT time.Time

	// debounce 远程刷新（v16）：请求成功后 scheduleRefresh() 重置计时器，
	// interval 内无新请求才刷一次 billing（比固定 ticker 准且省请求）。
	refreshMu       sync.Mutex
	refreshInterval time.Duration
	refreshTimer    *time.Timer
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
// 因此所有额度相关记录（volc_quota_models / volc_quota_usage）以 account_id 对齐，
// 而不是 channel_id。AK 是明文存储的稳定标识，SHA256 指纹无碰撞风险、不可逆。
func accountID(accessKey string) string {
	if accessKey == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(accessKey))
	return hex.EncodeToString(sum[:])[:16]
}

// StartBackgroundRefresh 启动"静默 debounce"远程刷新。
//
//   - interval <= 0 表示不启动（仍可手动调用 Refresh）。
//   - runNow=true 启动时立即触发一次刷新（拿到首份数据建底数）。
//
// 与固定 ticker 不同：每次模型请求成功（HandleProxyUpstreamSucceeded）都会调用
// scheduleRefresh() 重置计时器——请求活动期间不刷远程（billing 结算未完成，拉到的
// 数据不准且易 429），请求停止后静默 interval（默认 15 分钟）才刷一次，此时数据可靠。
// 本地 token 扣减（local_remaining）是实时的，不依赖这次刷新。
//
// 返回 Disposer（unload 时调用）。
func (s *Service) StartBackgroundRefresh(interval time.Duration, runNow bool) func() {
	s.refreshMu.Lock()
	s.refreshInterval = interval
	s.refreshMu.Unlock()
	if runNow {
		if _, err := s.Refresh(context.Background(), ""); err != nil {
			s.lg.Warn("volc-free-quota: 启动首次刷新失败", "err", err)
		}
	}
	return func() {
		s.refreshMu.Lock()
		if s.refreshTimer != nil {
			s.refreshTimer.Stop()
			s.refreshTimer = nil
		}
		s.refreshMu.Unlock()
	}
}

// scheduleRefresh 重置 debounce 计时器：请求成功后调用，interval 内无新请求才刷远程。
// 热路径（每请求一次），只做 timer 重置，开销可忽略。
func (s *Service) scheduleRefresh() {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	if s.refreshInterval <= 0 {
		return // 未启用 debounce
	}
	if s.refreshTimer != nil {
		s.refreshTimer.Stop()
	}
	s.refreshTimer = time.AfterFunc(s.refreshInterval, func() {
		if _, err := s.Refresh(context.Background(), ""); err != nil {
			s.lg.Warn("volc-free-quota: 静默刷新失败", "err", err)
		}
	})
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
	// 请求成功 → 重置 debounce 计时器：billing 结算未完成，静默 interval 后再刷远程，
	// 期间本地扣减（上面那行）已实时反映消耗。
	s.scheduleRefresh()
	return payload, nil
}

// decrementLocalRemaining 从该账号所有匹配该 API 模型的资源包行扣减 tokens，
// 并同步扣减 volc_quota_models 聚合行（UI 本地余额卡片的数据源）。
//
// 匹配锚点是 volc_quota_packages.model（configuration_code 提取名，如
// "deepseek-v4-flash-0731"），与请求的 API 模型名（如 "deepseek-v4-flash-ga-260731"）
// 用 matchOne 模糊匹配（含日期归一化）。扣减到 <= 0 时置 status='UsedUp'；
// local_remaining 下限 0（不出现负数）。
//
// 两张表同步：packages 是逐条资源包真实账本，models 是 UI 卡片读的聚合视图。
// models.model 是 Product 级聚合名（ark_bd 等），与 packages.model（code 提取名）
// 不同粒度，因此通过 packages.product 反查 models 行同步扣减。
//
// 日志：每次成功扣减记 Info（含扣前/扣后余额），未匹配到任何资源包记 Warn
// （说明该 API 模型没有对应的免费额度记录，本地扣减没生效，需要排查）。
func (s *Service) decrementLocalRemaining(accountID, apiModel string, tokens int) {
	if accountID == "" || apiModel == "" || tokens <= 0 {
		return
	}
	rows, err := s.db.Query(`SELECT instance_no, model, configuration_name, product, local_remaining FROM volc_quota_packages WHERE account_id = ? AND initial_total > 0`, accountID)
	if err != nil {
		s.lg.Warn("volc-free-quota: 查询资源包余额失败", "account_id", accountID, "model", apiModel, "err", err)
		return
	}
	defer rows.Close()
	// matched: instance_no -> [model, configuration_name, product, 扣前余额]
	matched := make(map[string][4]string)
	for rows.Next() {
		var instanceNo, model, confName, product string
		var before int64
		if err := rows.Scan(&instanceNo, &model, &confName, &product, &before); err == nil && model != "" && matchOne(model, apiModel) {
			matched[instanceNo] = [4]string{model, confName, product, strconv.FormatInt(before, 10)}
		}
	}
	if len(matched) == 0 {
		s.lg.Warn("volc-free-quota: 未匹配到免费额度资源包，本地不扣减",
			"account_id", accountID, "api_model", apiModel, "tokens", tokens,
			"hint", "该 API 模型没有对应的 volc_quota_packages 行（model 为 configuration_code 提取名），请检查渠道是否已刷新远程额度")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for inst, mm := range matched {
		// 1) 扣 packages（逐条资源包真实账本）。
		_, err := s.db.Exec(`
			UPDATE volc_quota_packages
			SET local_remaining = MAX(local_remaining - ?, 0),
			    status = CASE WHEN local_remaining - ? <= 0 THEN 'UsedUp' ELSE status END,
			    synced_at = ?  -- 复用 synced_at 记录最近一次本地扣减时间（展示用）
			WHERE account_id = ? AND instance_no = ?`,
			tokens, tokens, now, accountID, inst)
		if err != nil {
			s.lg.Warn("volc-free-quota: 扣减资源包余额失败", "account_id", accountID, "instance_no", inst, "model", mm[0], "configuration_name", mm[1], "tokens", tokens, "err", err)
			continue
		}
		// 2) 同步扣 volc_quota_models 聚合行（UI 本地余额卡片数据源）。
		//    匹配键：packages.product 归一化（models 的聚合键来源）。
		//    models.model 历史数据存在 "_" 与 "-" 两种写法（ark_bd / ark-bd），
		//    用 REPLACE 归一化后比较，避免漏掉。
		if mm[2] != "" {
			normProd := normalizeModelName(mm[2])
			if normProd != "" {
				if _, err := s.db.Exec(`
					UPDATE volc_quota_models
					SET local_remaining = MAX(local_remaining - ?, 0),
					    status = CASE WHEN local_remaining - ? <= 0 THEN 'exhausted' ELSE status END,
					    synced_at = ?
					WHERE account_id = ? AND REPLACE(model, '_', '-') = ?`,
					tokens, tokens, now, accountID, normProd); err != nil {
					s.lg.Warn("volc-free-quota: 同步扣减 models 聚合行失败", "account_id", accountID, "product", mm[2], "norm_prod", normProd, "tokens", tokens, "err", err)
				}
			}
		}
		var after int64
		if err := s.db.QueryRow(`SELECT local_remaining FROM volc_quota_packages WHERE account_id = ? AND instance_no = ?`, accountID, inst).Scan(&after); err != nil {
			after = -1
		}
		s.lg.Info("volc-free-quota: 本地余额扣减",
			"account_id", accountID, "api_model", apiModel, "matched_model", mm[0],
			"configuration_name", mm[1], "instance_no", inst,
			"tokens", tokens, "before", mm[3], "after", after)
	}
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
//  1. 若请求的 model 对应的本地余额（local_remaining）在所有候选账号上都已耗尽（<=0）
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
	return nil, &modelgateway.GatewayError{
		Status: http.StatusTooManyRequests,
		Type:   "free_quota_exhausted",
		Msg:    "模型免费额度用完",
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
//   - 耗尽判定唯一来源是本地余额：initial_total > 0（有本地记录）且 local_remaining <= 0。
//     billing API 的 status='exhausted' 不参与——远程数据不准确，只作初始底数。
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
	// 取出候选账号中所有匹配该 model 的资源包本地余额记录。
	placeholders := strings.Repeat("?,", len(accountIDs))
	placeholders = strings.TrimSuffix(placeholders, ",")
	args := make([]any, 0, len(accountIDs))
	for _, id := range accountIDs {
		args = append(args, id)
	}
	rows, err := s.db.Query(`SELECT account_id, model, initial_total, local_remaining FROM volc_quota_packages WHERE account_id IN (`+placeholders+`) AND initial_total > 0`, args...)
	if err != nil {
		return false, false, err
	}
	defer rows.Close()
	// map[account_id] -> 该账号是否有匹配且本地余额耗尽的资源包记录
	accountExhausted := make(map[string]bool)
	for rows.Next() {
		var aid, quotaModel string
		var initialTotal, localRemaining int64
		if err := rows.Scan(&aid, &quotaModel, &initialTotal, &localRemaining); err != nil {
			continue
		}
		if !matchOne(quotaModel, requestModel) {
			continue
		}
		// 本地余额主判据：有本地记录（initial_total > 0）且已扣到 0。
		if initialTotal > 0 && localRemaining <= 0 {
			accountExhausted[aid] = true
		}
	}
	if len(accountExhausted) == 0 {
		// 没有任何账号有匹配的耗尽 model
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
		if _, err := tx.ExecContext(ctx, `DELETE FROM volc_quota_models`); err != nil {
			return err
		}
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
		if _, err := tx.ExecContext(ctx, `DELETE FROM volc_quota_models WHERE account_id NOT IN (SELECT DISTINCT account_id FROM volc_quota_config WHERE account_id != '')`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM volc_quota_usage WHERE account_id NOT IN (SELECT DISTINCT account_id FROM volc_quota_config WHERE account_id != '')`); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListModels 返回某账号的资源包视图（account_id 维度）。
func (s *Service) ListModels(ctx context.Context, accountID string) ([]Model, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT account_id, model, product_name, total_amount, available_amount, used_amount,
		       initial_total, local_remaining, unit, status, synced_at
		FROM volc_quota_models WHERE account_id = ? ORDER BY model`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Model
	for rows.Next() {
		var m Model
		if err := rows.Scan(&m.AccountID, &m.Model, &m.ProductName,
			&m.TotalAmount, &m.AvailableAmount, &m.UsedAmount,
			&m.InitialTotal, &m.LocalRemaining,
			&m.Unit, &m.Status, &m.SyncedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
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
//
// 关键：免费额度按火山账号（AK）对齐，因此刷新前先把 enabled 配置按 account_id 去重，
// 每个账号只调一次火山接口（同账号多 Key 共享额度，重复查询浪费 QPS 且结果一致）。
// 返回 RefreshResult 给前端展示：失败/禁用条数。
func (s *Service) Refresh(ctx context.Context, channelID string) (RefreshResult, error) {
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
		disabled, rerr := s.refreshOne(ctx, cfg)
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

// refreshOne 刷新一个账号的额度并触发自动 disable（按账号对齐）。
//
// 流程：解密 secret_key → 调 SDK → 过滤方舟免费资源包 → 写 volc_quota_models（按账号）→
// 检测 Available<=0 对该账号关联的所有渠道 Key 写 model_states（冷却到次日 0:00）。
func (s *Service) refreshOne(ctx context.Context, cfg Config) ([]string, error) {
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
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// 账号指纹：该账号的额度快照归属。
	aid := cfg.AccountID
	if aid == "" {
		aid = accountID(cfg.AccessKey)
	}
	// 不直接 DELETE 重建——会丢失 local_remaining 本地递减值。
	// 改为 UPSERT：首次写入 initial_total + local_remaining（= total_amount），
	// 后续刷新只更新 billing API 的额度字段，保留 local_remaining 不覆盖。
	// 对 billing API 已不返回的模型（过期/删除），保留旧记录不动。

	// 该账号下所有渠道 Key 的 API 模型并集（禁用要作用于这些 Key 的模型）。
	channelIDs := s.channelsForAccount(aid)
	apiModels, err := s.channelsAPIModels(ctx, channelIDs)
	if err != nil {
		return nil, err
	}

	// 同 model 名可能有多个资源包（如几十个 ark_bd 包，总额度各不相同）：
	// 先按归一化 model 名在内存累加 total/available/used，再一次 UPSERT。
	// 不能逐条 UPSERT——主键 (account_id, model) 会让后写覆盖先写，只剩最后一个包。
	type agg struct {
		productName string
		total       int64
		avail       int64
		used        int64
		unit        string
	}
	groups := make(map[string]*agg)
	var order []string // 保持首次出现顺序，输出稳定
	for _, p := range pkgs {
		if !p.looksLikeArkFreePackage() {
			continue
		}
		label := normalizeModelName(p.ProductName)
		if label == "" {
			label = normalizeModelName(p.Product)
		}
		if label == "" {
			continue
		}
		a, ok := groups[label]
		if !ok {
			a = &agg{productName: p.ProductName, unit: p.Unit}
			groups[label] = a
			order = append(order, label)
		}
		a.total += parseAmount(p.TotalAmount)
		a.avail += parseAmount(p.AvailableAmount)
		a.used += parseAmount(p.UsedAmount)
	}
	var disabled []string
	for _, label := range order {
		a := groups[label]
		status := "ok"
		if a.avail <= 0 {
			status = "exhausted"
		}
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO volc_quota_models(account_id, model, product_name, total_amount, available_amount, used_amount,
			       initial_total, local_remaining, unit, status, synced_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(account_id, model) DO UPDATE SET
				product_name = excluded.product_name,
				total_amount = excluded.total_amount,
				available_amount = excluded.available_amount,
				used_amount = excluded.used_amount,
				-- initial_total 跟随累加更新（避免旧单包值残留在新累加体系下误导）；
				-- local_remaining 保留历史递减（不覆盖），同时 clamp 到 ≤ initial_total
				-- 防止旧 local_remaining 大于新额度池时 UI 进度条反向。
				initial_total = excluded.initial_total,
				local_remaining = MIN(volc_quota_models.local_remaining, excluded.initial_total),
				unit = excluded.unit,
				-- 本地余额已扣到 0 时保持 exhausted：billing API 可能返回旧的可用量，
				-- 若直接覆盖 status 会让本地已耗尽的模型"复活"。本地余额优先。
				status = CASE WHEN volc_quota_models.local_remaining <= 0 THEN 'exhausted' ELSE excluded.status END,
				synced_at = excluded.synced_at`,
			aid, label, a.productName, a.total, a.avail, a.used,
			a.total, a.total, // initial_total, local_remaining（首次写入=累加 total）
			a.unit, status, now); err != nil {
			return nil, err
		}
		if status == "exhausted" {
			// 把该 product 映射到该账号所有渠道实际请求用的 API 模型，逐个渠道落 model_states。
			matched := s.matchQuotaToAPIModels(label, apiModels)
			for _, m := range matched {
				for _, chID := range channelIDs {
					if err := s.disableModelForFreeQuota(ctx, chID, m, untilNextDayMidnightLocal()); err != nil {
						return nil, err
					}
				}
				disabled = append(disabled, m)
			}
		}
	}

	// 逐条 UPSERT 资源包明细（volc_quota_packages）：
	//   - model 用 configuration_code 提取名（去掉资源包类型后缀），扣减锚点，
	//     如 "DeepSeek_V4_flash_0731_data_collaboration_resource_pack" → "deepseek-v4-flash-0731"
	//   - initial_total 只在首次写入（定下扣减基准后不动，用户要求保留）。
	//   - local_remaining 校准策略（v16）：
	//       * 首次写入（旧 initial_total=0）→ = 本次总额（建底数）
	//       * 本地已耗尽（local_remaining<=0 且旧 initial_total>0）→ 保持 0，不复活
	//         （billing 可能滞后返回旧可用量，直接覆盖会让已耗尽的模型"复活"）
	//       * 正常 → 用 billing available_amount 校准（billing 是权威，本地扣减只是间隔期兜底）
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
				-- local_remaining：首次=总额；已耗尽不复活；否则用 billing 校准
				local_remaining = CASE
					WHEN volc_quota_packages.initial_total = 0 THEN excluded.local_remaining
					WHEN volc_quota_packages.local_remaining <= 0 THEN 0
					ELSE excluded.available_amount
				END,
				synced_at = excluded.synced_at`,
			aid, p.InstanceNo, p.Product, p.ProductName, p.ConfigurationCode, p.ConfigurationName,
			modelNameFromConfigCode(p.ConfigurationCode, p.Product),
			totalAmt, availAmt, usedAmt, p.Unit, p.Status, p.EffectiveTime, p.ExpiryTime,
			totalAmt, availAmt, // initial_total, local_remaining（首次写入=总额/可用）
			now); err != nil {
			return nil, err
		}
	}
	return disabled, nil
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

// 资源包类型后缀：configuration_code 中用于区分"这是什么包"的段，
// 提取模型名时丢弃（这些段与 API 模型名无关）。
var configCodeSuffixes = []string{
	"data_collaboration",
	"resource_pack",
	"pack",
	"free_inference",
	"free_infer",
	"collaboration",
}

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
func isConfigCodeSuffix(seg string) bool {
	switch strings.ToLower(seg) {
	case "data", "collaboration", "resource", "pack", "free", "inference", "infer":
		return true
	}
	return false
}

// disableModelForFreeQuota 在 model_states 写入免费额度耗尽导致的临时禁用：
//
//   - status='cooling'，让 model-health 的 CheckNow 在 disabled_until 到期后自动恢复。
//   - manual_enabled 保留原值（CONFLICT 不更新）；用户手动启停的偏好不被覆盖。
//   - last_error 写 "模型免费额度用完"，UI 在 model-status 页面会展示这条信息。
//   - 冷却到次日 0:00（本地时区）——火山引擎账单资源包每日重置。
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
			fail_count=model_states.fail_count+1,
			last_checked_at=excluded.last_checked_at,
			updated_at=excluded.updated_at`,
		channelID, model, untilStr, now, now)
	return err
}

// untilNextDayMidnightLocal 返回距离次日 0:00（本地时区）的目标 time。
//
// 用户设定："冷却时间改为 距离第二天到0点的间隔（因为每天0点刷新）"。
func untilNextDayMidnightLocal() time.Time {
	now := time.Now()
	loc := now.Location()
	tomorrow := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, loc)
	// 防御性：万一本地时区异常，回退到 UTC。
	if loc == nil || loc.String() == "" {
		tomorrow = time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	}
	return tomorrow
}

// ===== ListStatus 聚合（设置页用） =====

// ListStatus 一次性返回所有配置 + 每条配置下（按账号聚合）的免费模型 + 使用统计 + 资源包明细。
//
// 注意：volc_quota_models / volc_quota_usage / volc_quota_packages 按 account_id 归属，
// 同一账号下多渠道 Key 共享快照；前端按 channel 行展示，同账号各行回填同一份数据。
func (s *Service) ListStatus(ctx context.Context) (ListStatusResponse, error) {
	configs, err := s.ListConfigs(ctx)
	if err != nil {
		return ListStatusResponse{}, err
	}
	resp := ListStatusResponse{Configs: make([]ConfigWithDetails, 0, len(configs))}
	modelsCache := make(map[string][]Model)
	usageCache := make(map[string][]Usage)
	packagesCache := make(map[string][]Package)
	for _, cfg := range configs {
		aid := cfg.AccountID
		if aid == "" {
			aid = accountID(cfg.AccessKey)
		}
		models, ok := modelsCache[aid]
		if !ok && aid != "" {
			models, err = s.ListModels(ctx, aid)
			if err != nil {
				return resp, err
			}
			modelsCache[aid] = models
		}
		if models == nil {
			models = []Model{}
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
			Models:   models,
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
// 手动刷新额度：请求体 {"channel_id":"..."} 只刷该渠道；缺省/空则全量刷新。
// 返回 RefreshResult（含本次禁用/失败明细），前端据此展示。
// 任一条渠道刷新失败 → HTTP 4xx/5xx + 明确错误（不再静默 200），前端 toast 直接可见。
func (s *Service) HandleRefresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ChannelID string `json:"channel_id"`
	}
	// 空 body 也允许（全量刷新）。
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	result, err := s.Refresh(r.Context(), req.ChannelID)
	if err != nil {
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
