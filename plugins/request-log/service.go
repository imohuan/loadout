// Package requestlog 实现 Loadout 的完整请求日志插件（plugins/request-log）。
//
// 把每次 AI 模型请求的完整输入输出（请求体、响应体、流式逐块拼接）落库到
// 独立 SQLite 文件 request-log.db（单表 request_logs，主键 UUID），提供
// GET /api/request-logs 列表搜索与 GET /api/request-logs/{id} 详情。
//
// 订阅 model-gateway 的四个 waterfall 事件：
//   - proxy:before-attempt：请求发出之前抓完整请求 + 生成 UUID（用户拍板）；
//   - proxy:after-upstream：非流式 2xx 响应收尾；
//   - proxy:upstream-failed：失败收尾（4xx/5xx/无渠道，仅 2xx 才走 after）；
//   - proxy:stream-chunk：流式逐块拼接，[DONE] 收尾。
//
// 与 route-log 的关联（方式 A）：route_requests 表加列 request_log_id（UUID），
// 本插件在 before-attempt 生成后 UPDATE 该列，route-log 列表/详情带出，
// 前端点击跳转 /api/request-logs/{id}。能力开关挂 capability_routes
// （capability="request_log"，models × channels 矩阵，参照 sensitive-filter）。
package requestlog

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"loadout/core/config"
	"loadout/core/db"
	"loadout/core/plugin"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// capabilityName 能力路由表中本能力的固定名称。
const capabilityName = "request_log"

// metadataKey UUID 在 pipe.Metadata 中的键——单一来源是 model-gateway 的
// MetadataRequestLogID 常量（proxyAttempt 实际请求位置生成，emit 时随管线传入）。
// 本插件在 HandleBeforeAttempt 里覆写为本次 attempt 的 UUID（per-attempt 独立日志），
// 收尾事件（after-upstream/stream-chunk/upstream-failed）经 pipeRequestLogID 读它命中本次行。
const metadataKey = modelgateway.MetadataRequestLogID

// attemptMetadataKey 本次 attempt 的 request-log 关联 UUID 键：model-gateway 写
// route_attempts 行时读取落 request_log_id 列（前端内层行渲染「日志」按钮）。
const attemptMetadataKey = modelgateway.MetadataRequestLogAttemptID

// skippedKey 本 attempt 未命中 request_log 能力路由的哨兵：置 true 时收尾事件
// （after/stream-chunk/upstream-failed）经 pipeRequestLogID 直接返回空、跳过按
// request_id 反查兜底——防止把上一个已记录 attempt 的日志行错误收尾/覆盖。
const skippedKey = "__request_log_skipped"

// keepBase64Key 本次请求的有效「保留原始 base64」配置（bool）：before-attempt
// 从命中的能力路由读出后写入 metadata，收尾事件（响应/流式块）复用同一口径——
// 同一个请求的请求体与响应体不能一个保留一个打码。
const keepBase64Key = "__request_log_keep_base64"

// Service 完整请求日志适配器：查能力路由、抓请求/响应快照，正文写块存储
// （manifests/ + blobs/），request-log.db 只存元数据行。
// 所有写库 best-effort：handler 永不 return error（否则中断整个 /v1 转发），
// 出错仅记日志。
type Service struct {
	st      *store.Store
	lg      *slog.Logger
	reqDB   *sql.DB        // 元数据库 request-log.db（request_logs 元数据 / request_log_config）
	blobs   *blobStore     // 内容块存储（唯一正文存储；装配层必须 SetBlobDir 指定目录）
	loadout *sql.DB        // loadout.db：UPDATE route_requests.request_log_id 关联列
	repo    *db.Repository // SQLite 能力路由数据源（装配后注入；nil 时回退 JSON）
	// dbPath 独立库文件路径：Stats 报「日志占用空间」要的是文件大小（含 WAL），
	// 不能只算表大小（表和实际磁盘占用差得多）。空路径时 Stats 回落为 0。
	dbPath string
}

// NewService 创建完整请求日志适配器。database 为 loadout.db（写关联列用，
// 测试可传 nil 跳过 UPDATE）。块存储目录由装配层 SetBlobDir 指定。
func NewService(st *store.Store, lg *slog.Logger, reqDB, database *sql.DB) *Service {
	return &Service{st: st, lg: lg, reqDB: reqDB, loadout: database}
}

// SetBlobDir 显式指定块存储目录（装配层调用；测试注入临时目录）。
func (s *Service) SetBlobDir(dir string) error {
	bs, err := newBlobStore(dir)
	if err != nil {
		return err
	}
	s.blobs = bs
	return nil
}

// SetDBPath 记录独立库文件路径（装配层开库后调用），供 Stats 报文件大小。
func (s *Service) SetDBPath(path string) { s.dbPath = path }

// RetentionConfig 日志保留策略（来自全局运行时设置，0 值表示不限制）。
type RetentionConfig struct {
	// MaxAgeDays 只保留最近多少天的日志（按 started_at 判定）；<=0 表示不限。
	MaxAgeDays int
	// MaxSizeMB 日志库最大占用（MB）；超限时按时间从旧到新删除，直到低于阈值。
	// <=0 表示不限。
	MaxSizeMB int
}

// retentionTimeFormat 与写入时一致的 RFC3339Nano：字符串比较即时间比较（SQLite TEXT 列）。
const retentionTimeFormat = time.RFC3339Nano

// RequestLogStats 日志库统计：前端「日志大小」按钮显示的就是 Size。
type RequestLogStats struct {
	// Size 独立库文件占用字节数（request-log.db + WAL 边车，不含固定的 -shm 索引）；
	// 找不到文件时回落为 0。
	Size int64 `json:"size"`
	// Count request_logs 行数（顺序可能因分页取不到；仅作展示）。
	Count int64 `json:"count"`
	// OldestStartedAt / NewestStartedAt 最早/最新一条日志的 started_at（RFC3339，空库为空串）。
	OldestStartedAt string `json:"oldest_started_at,omitempty"`
	NewestStartedAt string `json:"newest_started_at,omitempty"`
	// MaxAgeDays / MaxSizeMB 当前生效的保留配置（回显给前端设置页）。
	MaxAgeDays int `json:"max_age_days"`
	MaxSizeMB  int `json:"max_size_mb"`
}

// Stats 汇总日志库占用：文件大小（含 WAL）+ 行数 + 时间范围 + 当前保留配置。
// 只读，永不改数据；任一子查询失败按缺省值返回，不报错（前端展示用，不值得打断）。
func (s *Service) Stats(ctx context.Context) RequestLogStats {
	stats := RequestLogStats{}
	if s.reqDB == nil {
		return stats
	}
	stats.Size = s.diskSize()
	var oldest, newest sql.NullString
	if err := s.reqDB.QueryRowContext(ctx, `SELECT COUNT(*), MIN(started_at), MAX(started_at) FROM request_logs`).
		Scan(&stats.Count, &oldest, &newest); err != nil {
		s.lg.Warn("request-log: 统计日志库失败", "err", err)
	}
	stats.OldestStartedAt = oldest.String
	stats.NewestStartedAt = newest.String
	if s.repo != nil {
		if settings, err := s.repo.GetSettings(ctx); err == nil {
			stats.MaxAgeDays = settings.RequestLogMaxAgeDays
			stats.MaxSizeMB = settings.RequestLogMaxSizeMB
		}
	}
	return stats
}

// diskSize 独立库的实际磁盘占用，单位字节。只算主文件 request-log.db。
//
// 【为什么不算 -wal】WAL 是「已提交但还没并回主文件」的增量缓冲，有两个要命的性质：
//   - 它不会自己回落。实测写入 2000 行 × 64KB 后 WAL 停在 4.1MB，空闲 2 秒纹丝不动，
//     只有 TRUNCATE checkpoint 才会清零；库越大、写得越频繁，它残留得越多。
//   - 它会随写入活动实时波动。同一秒钟读两次可能差几十 MB。
//
// 把它算进「日志占用」，数字就会虚高、还会随时间跳——用户在转发日志页（每 3 秒刷）
// 和设置页（进页面时刷一次）看到两个不同的值，看起来像 bug。日志真正占的地方是主
// 文件，WAL 只是过程中的中转。
//
// 读之前先做一次 PASSIVE checkpoint 把已提交内容并回主文件：它很便宜（不重建文件，
// 只是搬页），能保证主文件的尺寸反映最新数据，不至于刚写完一堆日志、主文件还停在旧值。
//
// 【为什么不算 -shm】共享内存索引，固定 32KB，不随数据量变化。
func (s *Service) diskSize() int64 {
	if s.dbPath == "" {
		return 0
	}
	// best-effort：checkpoint 失败（例如别的连接正占着）就按当前主文件大小报，
	// 不阻塞、不报错——这只是展示用的数字。
	_, _ = s.reqDB.ExecContext(context.Background(), `PRAGMA wal_checkpoint(PASSIVE)`)
	info, err := os.Stat(s.dbPath)
	if err != nil {
		return 0
	}
	return info.Size()
}

// Clear 清空全部完整请求日志。同时把 loadout.db 里残留的关联列 request_log_id 清空：
// 关联行没了但 route_requests.request_log_id 还留着，前端会一直显示「进入日志」按钮，
// 点进去 404。清列后列表刷新即不再显示入口。
// 返回删除的行数。
func (s *Service) Clear(ctx context.Context) (int64, error) {
	if s.reqDB == nil {
		return 0, fmt.Errorf("request-log: 独立库未装配")
	}
	if s.blobs == nil {
		return 0, fmt.Errorf("request-log: 块存储未装配")
	}
	// 清空 = 删全部清单文件 + GC 无引用块（纯文件操作，无 VACUUM）+ 清元数据行。
	ids, err := s.blobs.manifestIDs()
	if err != nil {
		return 0, err
	}
	var removed int64
	for _, id := range ids {
		if s.blobs.deleteManifest(id) {
			removed++
		}
	}
	if _, err := s.blobs.collectGarbage(); err != nil {
		s.lg.Warn("request-log: 清空后块回收失败", "err", err)
	}
	if _, err := s.reqDB.ExecContext(ctx, `DELETE FROM request_logs`); err != nil {
		return removed, err
	}
	if s.loadout != nil {
		if _, err := s.loadout.ExecContext(ctx, `UPDATE route_requests SET request_log_id = NULL WHERE COALESCE(request_log_id, '') <> ''`); err != nil {
			s.lg.Warn("request-log: 清空关联列失败", "err", err)
		}
	}
	return removed, nil
}

// ApplyRetention 按保留策略清理旧日志（FIFO：先删最旧的）。
//
// 双通道，任意一个开启即生效：
//   - 时间：删除 started_at 早于 now-MaxAgeDays 的行；
//   - 容量：request-logs 文件夹真实占用超过 MaxSizeMB 时，从最旧的清单开始删
//     （文件夹大小 = 磁盘真相，去重后块共享与否都不影响判断）。
//
// 调用点：写日志后（HandleBeforeAttempt 末尾，best-effort）、服务启动时一次、
// 以及在设置页保存保留策略 / 点「立即清理」时手动触发（用户改完上限不必等下一个请求）。
// 出错只记日志，绝不阻塞请求。
func (s *Service) ApplyRetention(ctx context.Context, cfg RetentionConfig) {
	if s.reqDB == nil || (cfg.MaxAgeDays <= 0 && cfg.MaxSizeMB <= 0) {
		return
	}
	s.applyBlobRetention(ctx, cfg)
}

// applyBlobRetention 块存储路径的保留策略：删除过期清单文件 + GC 无引用块。
// 无 VACUUM、无库锁；清单删除后页面立刻不可见（ExistingIDs 走元数据表，
// 元数据行同步删除）。
func (s *Service) applyBlobRetention(ctx context.Context, cfg RetentionConfig) {
	ids, err := s.blobs.manifestIDs()
	if err != nil {
		s.lg.Warn("request-log: 列出清单失败", "err", err)
		return
	}
	type entry struct {
		id      string
		started time.Time
	}
	entries := make([]entry, 0, len(ids))
	for _, id := range ids {
		m, err := s.blobs.readManifest(id)
		if err != nil {
			continue
		}
		started, err := time.Parse(time.RFC3339Nano, m.StartedAt)
		if err != nil {
			continue
		}
		entries = append(entries, entry{id: id, started: started})
	}
	limit := int64(cfg.MaxSizeMB) * 1024 * 1024
	used := s.blobDirSize()
	removed := 0
	// 从最旧开始删（entries 保持清单顺序即可，过期判断逐条；容量判断从最旧起删）
	sort.Slice(entries, func(i, j int) bool { return entries[i].started.Before(entries[j].started) })
	for _, e := range entries {
		expired := cfg.MaxAgeDays > 0 && time.Since(e.started) > time.Duration(cfg.MaxAgeDays)*24*time.Hour
		overSize := cfg.MaxSizeMB > 0 && used > limit
		if !expired && !overSize {
			continue
		}
		if s.blobs.deleteManifest(e.id) {
			if _, err := s.reqDB.ExecContext(ctx, `DELETE FROM request_logs WHERE id = ?`, e.id); err != nil {
				s.lg.Warn("request-log: 删元数据行失败", "id", e.id, "err", err)
			}
			removed++
			used = s.blobDirSize() // 删完重算（简单可靠；清理不频繁，开销可忽略）
		}
	}
	if removed > 0 {
		if n, err := s.blobs.collectGarbage(); err == nil && n > 0 {
			s.lg.Info("request-log: 块回收完成", "manifests", removed, "blobs", n)
		}
	}
}

// blobDirSize 统计块存储目录的真实磁盘占用（blobs/ + manifests/ 全部文件）。
// 清理判断用它而不是任何估算列：这就是用户在磁盘上看到的数字。
func (s *Service) blobDirSize() int64 {
	var total int64
	_ = filepath.WalkDir(s.blobs.dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() == false {
			if info, ierr := d.Info(); ierr == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

// ApplyCurrentRetention 按当前设置里的策略清理一次，返回执行后的统计。
// 供 HTTP 手动触发（设置页「立即清理」/ 保存设置后立刻见效）与启动时调用。
func (s *Service) ApplyCurrentRetention(ctx context.Context) RequestLogStats {
	s.ApplyRetention(ctx, s.currentRetention())
	return s.Stats(ctx)
}

// SetRepository 注入 SQLite 仓储（由装配层在 db 就绪后调用；测试可省略）。
// 同时用于能力路由查询与 route_requests.request_log_id 的 UPDATE（同一 loadout.db）。
func (s *Service) SetRepository(repo *db.Repository) { s.repo = repo }

// DecideRoutesScope 查能力路由表：model + 请求渠道上下文（含聚合模型的候选 Key 集合）。
// 返回所有匹配的路由；native（及历史 error 降级）立即返回，proxy 路由收集全部匹配项。
// 选择策略统一走 types.SelectCapabilityRoutes（与 vision/sensitive/field-filter 一致）。
// 读表/解析失败 fail-open：记录日志并返回 nil（按 native 透传），不拒绝请求。
//
// 注意：这里只按真实模型名匹配，不知道聚合模型的虚拟名。before-attempt 场景请用
// DecideRoutesScopeEx（聚合在 before-upstream 已把 Request.Model 改写成真实模型）。
func (s *Service) DecideRoutesScope(model string, scope types.ChannelRequestScope) ([]*types.CapabilityRoute, error) {
	return s.DecideRoutesScopeEx(model, "", scope)
}

// DecideRoutesScopeEx 同 DecideRoutesScope，额外支持按聚合（虚拟）模型名匹配。
// 聚合模型（如 debug）在 before-upstream 就被改写成真实模型（deepseek-v4.1-flash），
// 虚拟名只剩在 metadata 的 __virtual_model 里；只按真实模型匹配会让「debug + 保留
// base64」这类专项规则永远命不中，静默退到下面的 * 兜底规则。
func (s *Service) DecideRoutesScopeEx(model, virtualModel string, scope types.ChannelRequestScope) ([]*types.CapabilityRoute, error) {
	if s.repo != nil {
		routes, err := s.repo.ListCapabilityRoutes(context.Background())
		if err == nil {
			return types.SelectCapabilityRoutesEx(routes, capabilityName, model, virtualModel, scope), nil
		}
		s.lg.Error("request-log: 从 SQLite 读能力路由表失败，回退 JSON", "err", err)
	}
	var routes []types.CapabilityRoute
	if err := s.st.Read(types.FileCapabilityRoutes, &routes); err != nil {
		if errors.Is(err, store.ErrNotExist) {
			return nil, nil
		}
		s.lg.Error("request-log: 读取能力路由表失败，按透传处理", "err", err)
		return nil, nil
	}
	return types.SelectCapabilityRoutesEx(routes, capabilityName, model, virtualModel, scope), nil
}

// requestChannelBaseURLs 反查渠道 base_url 列表：term 可为渠道 key id（精确匹配，返回该 key
// 所在渠道组的 base_url）或渠道名 ChannelName（返回组内全部启用 Key 共享的 base_url，去重）。
// 无渠道或查不到返回空 slice。入口阶段（BeforeUpstream）只有 __channel_hint 渠道名时
// 也能反查，供渠道级约束（channel_base_urls）路由匹配。
func (s *Service) requestChannelBaseURLs(term string) []string {
	if term == "" || s.repo == nil {
		return nil
	}
	channels, err := s.repo.ListChannels(context.Background())
	if err != nil {
		return nil
	}
	var byID string
	var byName []string
	for _, ch := range channels {
		if ch.ID == term {
			byID = ch.BaseURL
		}
		if ch.ManualEnabled && ch.ChannelName == term && ch.BaseURL != "" {
			byName = append(byName, ch.BaseURL)
		}
	}
	if byID != "" {
		return []string{byID}
	}
	return byName
}

// redactEnabled 读脱敏开关（request_log_config.redact，默认 1=开）。
// 读失败按开启处理（安全默认）。
func (s *Service) redactEnabled() bool {
	if s.reqDB == nil {
		return true
	}
	var redact int
	if err := s.reqDB.QueryRow(`SELECT redact FROM request_log_config WHERE id = 1`).Scan(&redact); err != nil {
		return true
	}
	return redact != 0
}

// redactOptions 一次请求的脱敏口径：总开关 + 是否保留原始 base64。
//
// KeepBase64 来自命中的能力路由（RequestLogKeepBase64），before-attempt 时写进
// metadata（keepBase64Key），收尾事件从这里取——保证同一次请求的请求体、响应体、
// 流式块用同一个口径，不会出现请求体保留图片、响应体被打码这种半截行为。
type redactOptions struct {
	Enabled    bool
	KeepBase64 bool
}

// redactOptions 组装本次请求的脱敏口径（metadata 缺失时按打码处理）。
func (s *Service) redactOptions(pipe *modelgateway.ProxyPipeline) redactOptions {
	opts := redactOptions{Enabled: s.redactEnabled()}
	if pipe != nil && pipe.Metadata != nil {
		if v, _ := pipe.Metadata[keepBase64Key].(bool); v {
			opts.KeepBase64 = true
		}
	}
	return opts
}

// subscribe 订阅 model-gateway 事件。
func (s *Service) subscribe(ctx plugin.Context) {
	ctx.On(modelgateway.ProxyBeforeAttempt, s.HandleBeforeAttempt)
	ctx.On(modelgateway.ProxyAfterUpstream, s.HandleAfterUpstream)
	ctx.On(modelgateway.ProxyUpstreamFailed, s.HandleUpstreamFailed)
	// proxy:attempt-failed：每次渠道尝试失败即触发（普通模型 + 聚合中间失败 attempt），
	// payload 与 ProxyUpstreamFailed 同构（*ProxyFailurePayload），直接复用收尾逻辑。
	ctx.On(modelgateway.ProxyAttemptFailed, s.HandleUpstreamFailed)
	ctx.On(modelgateway.ProxyStreamChunk, s.HandleStreamChunk)
}

// ---- 请求方向：proxy:before-attempt（请求发出之前抓完整请求 + 生成 UUID） ----

// HandleBeforeAttempt 每次渠道尝试前触发（proxy.go:282，早于构建 :304 / 发出 :337）。
// 本 handler 只**消费** UUID——model-gateway 已在实际请求位置（proxyAttempt）生成并
// 写入 pipe.Metadata（MetadataRequestLogID），emit 事件时随管线传入；这里不自己造，
// 仅当 metadata 缺失（测试直构 pipe / 管道被重建）才兜底：反查 route_requests 复用，
// 再不行才自造。
// 命中 request_log 能力路由时：UPDATE route_requests.request_log_id → 写 request_logs
// 半条（running）→ 标记已记录（failover 同 pipe 重复触发时早退）。
// 【铁律】永不 return error、永不改 body/响应——只读快照，出错仅记日志（best-effort）。
func (s *Service) HandleBeforeAttempt(payload any) (any, error) {
	pipe, ok := payload.(*modelgateway.ProxyPipeline)
	if !ok || pipe == nil || pipe.Request == nil || s.reqDB == nil {
		return payload, nil
	}
	if pipe.Metadata == nil {
		pipe.Metadata = map[string]any{}
	}
	// 能力路由匹配用「当前 attempt 的真实模型」：聚合插件在 before-upstream 已把
	// pipe.Request.Model 改写为真实模型（aggregate/service.go:124），虚拟名只留在
	// __virtual_model 里供 route-log 展示。不能拿虚拟名覆盖——否则用户只配置物理模型
	// （如 hy3）时，聚合内部切换到的真实模型永远匹配不上（与 sensitive-filter 对齐）。
	model := pipe.Request.Model
	scope := types.ChannelScopeFromMetadata(pipe.Metadata, s.requestChannelBaseURLs)
	// 聚合模型的虚拟名（如 debug）也要参与匹配：聚合在 before-upstream 已把
	// Request.Model 改写成真实模型，只用真实名会让专项规则静默落到 * 兜底。
	routes, err := s.DecideRoutesScopeEx(model, types.VirtualModelFromMetadata(pipe.Metadata), scope)
	if err != nil {
		s.lg.Warn("request-log: 能力路由决策失败，跳过记录", "request_id", pipe.RequestID, "err", err)
		return payload, nil
	}
	if len(routes) == 0 || routes[0].Route != types.RouteProxy {
		// 未命中 / native：不记录（列保持 NULL，前端不显示入口）。
		// 【P1 修复】必须清掉本 attempt 的关联 key 并打哨兵：聚合 failover 换模型后若
		// 新模型不在 request_log 路由内，上一 attempt 的 UUID 残留会导致本 attempt 的
		// route_attempts.request_log_id 错误指向旧 UUID，收尾事件（after/stream-chunk）
		// 经 pipeRequestLogID 反查兜底还会把旧 attempt 的日志结果覆盖掉。哨兵让
		// pipeRequestLogID 直接返回空、跳过反查。
		delete(pipe.Metadata, metadataKey)
		delete(pipe.Metadata, attemptMetadataKey)
		delete(pipe.Metadata, keepBase64Key)
		pipe.Metadata[skippedKey] = true
		return payload, nil
	}

	// 每次渠道尝试独立 UUID（per-attempt 语义）：不复用 model-gateway 的 pipe 级 UUID
	//（failover 同 pipe 所有 attempt 共享同一个，无法区分），每次生成新 UUID 写新行。
	uuid := newRequestLogID()
	// 覆写 metadata：收尾事件（after/stream-chunk/upstream-failed）经 pipeRequestLogID
	// 读 metadataKey 命中本次行；model-gateway 写 route_attempts 行读 attemptMetadataKey
	// 落 request_log_id 列（前端内层行渲染「日志」按钮）。
	pipe.Metadata[metadataKey] = uuid
	pipe.Metadata[attemptMetadataKey] = uuid
	// 本 attempt 的 base64 保留口径（能力路由配置）：写进 metadata，收尾事件
	// （after/stream-chunk/upstream-failed）复用，保证请求体与响应体同口径。
	// 只认 routes[0]——与 SelectCapabilityRoutes 的「首条生效」语义一致。
	pipe.Metadata[keepBase64Key] = routes[0].RequestLogKeepBase64

	// UPDATE 关联列（best-effort；loadout 连接为空或行不存在时忽略，不影响记录）。
	// 每次尝试都覆写：外层「完整日志」入口必须指向**最后一次**渠道尝试的日志，也就是真正
	// 把响应交给用户的那次。早先只在首次写入（`AND COALESCE(request_log_id, '') = ''`），
	// failover 后外层按钮仍指着早已 429 失败的第 1 条，点进去与列表显示的「成功」矛盾。
	if s.loadout != nil {
		if _, err := s.loadout.Exec(`UPDATE route_requests SET request_log_id = ? WHERE request_id = ?`, uuid, pipe.RequestID); err != nil {
			s.lg.Warn("request-log: 写 route_requests.request_log_id 失败", "request_id", pipe.RequestID, "err", err)
		}
	}

	channel, _ := pipe.Metadata["__current_channel"].(string)
	started := time.Now().UTC()
	snap := buildRequestSnapshot(pipe, model, s.redactOptions(pipe))
	reqJSON, err := json.Marshal(snap)
	if err != nil {
		s.lg.Warn("request-log: 序列化请求快照失败", "request_id", pipe.RequestID, "err", err)
		return payload, nil
	}
	// 纯 INSERT：每次 attempt 独立 UUID 恒不冲突（无需 ON CONFLICT 分支）。
	// 正文走块存储（beginBlobLog），元数据行 request_json 置空。
	return payload, s.beginBlobLog(pipe, model, uuid, channel, started, reqJSON)
}

// beginBlobLog 块存储路径的「写半条」：请求体拆消息块（逐条 SHA-256 去重）+
// 剩余参数块，正文写 blobs/，清单写 manifests/；request_logs 表只落一行元数据
// （request_json 列存空串占位——详情从块存储组装，列表/统计/搜索不读正文）。
// 拆块失败（非 JSON / 无 messages）自动退回整体单块（ReqRaw），语义不丢。
func (s *Service) beginBlobLog(pipe *modelgateway.ProxyPipeline, model, uuid, channel string, started time.Time, reqJSON []byte) error {
	if s.blobs == nil {
		// 块存储未装配：退化为只写元数据行（正文缺失会在详情页看到空 body，
		// 但不阻塞请求——best-effort 语义与整个插件一致）。
		if _, err := s.reqDB.Exec(`INSERT INTO request_logs(id, request_id, model, channel, stream, started_at, result, created_at) VALUES (?, ?, ?, ?, ?, ?, 'running', ?)`,
			uuid, pipe.RequestID, model, channel, boolToInt(pipe.Request.Stream), started.Format(time.RFC3339Nano), started.Format(time.RFC3339Nano)); err != nil {
			s.lg.Warn("request-log: 写 request_logs 元数据行失败", "request_id", pipe.RequestID, "err", err)
		}
		return nil
	}
	m := requestManifest{
		ID:        uuid,
		RequestID: pipe.RequestID,
		Model:     model,
		Stream:    pipe.Request.Stream,
		StartedAt: started.Format(time.RFC3339Nano),
		Result:    "running",
	}
	// 快照 headers/method 等进 ReqExtra 的包装层（组装时还原 requestSnapshot 结构）
	snap := buildRequestSnapshot(pipe, model, s.redactOptions(pipe))
	// snap.Body 已含完整请求体（脱敏后）；拆的是脱敏后的 body——脱敏是确定性
	// 替换，同一条消息前后轮脱敏结果一致，哈希命中不受影响。
	body := []byte(snap.Body)
	msgs, contents, extra, ok := splitRequestMessages(body)
	if ok {
		for i := range msgs {
			if _, err := s.blobs.putBlob(contents[i]); err != nil {
				s.lg.Warn("request-log: 写消息块失败", "request_id", pipe.RequestID, "err", err)
				return nil
			}
			m.Messages = append(m.Messages, msgs[i].Hash)
		}
		if extraHash, err := s.blobs.putBlob([]byte(extra)); err != nil {
			s.lg.Warn("request-log: 写参数块失败", "request_id", pipe.RequestID, "err", err)
			return nil
		} else {
			m.ReqExtra = extraHash
		}
	} else {
		// 不可拆：整个请求体一个块
		hash, err := s.blobs.putBlob(body)
		if err != nil {
			s.lg.Warn("request-log: 写请求块失败", "request_id", pipe.RequestID, "err", err)
			return nil
		}
		m.ReqRaw = hash
	}
	// headers 等包装元数据也存块（脱敏后序列化，同请求头基本不变 → 命中率高）
	envJSON, err := json.Marshal(requestEnvelope{
		Method:  snap.Method,
		Path:    snap.Path,
		Query:   snap.Query,
		Headers: snap.Headers,
		Stream:  snap.Stream,
	})
	if err == nil {
		if envHash, err := s.blobs.putBlob(envJSON); err == nil {
			m.Envelope = envHash
		}
	}
	if err := s.blobs.writeManifest(uuid, m); err != nil {
		s.lg.Warn("request-log: 写清单失败", "request_id", pipe.RequestID, "err", err)
		return nil
	}
	// 清单快照留在 metadata：收尾事件直接复用（不必重读文件），也供流式缓冲定位。
	pipe.Metadata[manifestKey] = &m
	// 元数据行：只有小字段，正文全部在块存储（详情走块组装）。
	if _, err := s.reqDB.Exec(`INSERT INTO request_logs(id, request_id, model, channel, stream, started_at, result, created_at) VALUES (?, ?, ?, ?, ?, ?, 'running', ?)`,
		uuid, pipe.RequestID, model, channel, boolToInt(pipe.Request.Stream), started.Format(time.RFC3339Nano), started.Format(time.RFC3339Nano)); err != nil {
		s.lg.Warn("request-log: 写 request_logs 元数据行失败", "request_id", pipe.RequestID, "err", err)
	}
	return nil
}

// requestEnvelope 请求的包装元数据（headers/method/path 等），存块、按哈希去重。
type requestEnvelope struct {
	Method  string         `json:"method"`
	Path    string         `json:"path"`
	Query   string         `json:"query"`
	Headers headerSnapshot `json:"headers"`
	Stream  bool           `json:"stream"`
}

// currentRetention 读全局运行时设置里的日志保留配置；读不到返回零值（= 不清理）。
// 独立库没装 repo 时（测试）返回零值。
func (s *Service) currentRetention() RetentionConfig {
	if s.repo == nil {
		return RetentionConfig{}
	}
	settings, err := s.repo.GetSettings(context.Background())
	if err != nil {
		return RetentionConfig{}
	}
	return RetentionConfig{MaxAgeDays: settings.RequestLogMaxAgeDays, MaxSizeMB: settings.RequestLogMaxSizeMB}
}

// ---- 输出方向：非流式收尾（2xx 走 after-upstream，失败走 upstream-failed） ----

// HandleAfterUpstream 非流式 2xx 响应返回后触发（proxy.go:413，仅 2xx 分支）。
// 收尾 request_logs：result=success、response_json/http_status/finished_at/duration_ms、
// channel 回填 __last_tried_channel。永不 return error。
func (s *Service) HandleAfterUpstream(payload any) (any, error) {
	ap, ok := payload.(*modelgateway.AfterUpstreamPayload)
	if !ok || ap == nil || ap.Pipe == nil || s.reqDB == nil {
		return payload, nil
	}
	uuid := s.pipeRequestLogID(ap.Pipe)
	if uuid == "" {
		return payload, nil // 未被记录（未命中能力路由）
	}
	channel, _ := ap.Pipe.Metadata["__last_tried_channel"].(string)
	opts := s.redactOptions(ap.Pipe)
	snap := responseSnapshot{
		StatusCode: ap.Response.StatusCode,
		Headers:    redactHeaders(ap.Response.Header, opts.Enabled),
		Body:       redactBody(string(ap.Response.Body), opts),
	}
	respJSON, err := json.Marshal(snap)
	if err != nil {
		s.lg.Warn("request-log: 序列化响应快照失败", "request_id", ap.Pipe.RequestID, "err", err)
		return payload, nil
	}
	s.finishRequestLog(uuid, ap.Response.StatusCode, string(respJSON), channel, "success")
	return payload, nil
}

// HandleUpstreamFailed 上游转发失败（4xx/5xx、无渠道、网络错误、安检拒绝）。
// ProxyAfterUpstream 仅 2xx 触发，失败必须靠本事件收尾（B2），否则行永远卡 running。
// 永不 return error。
func (s *Service) HandleUpstreamFailed(payload any) (any, error) {
	fp, ok := payload.(*modelgateway.ProxyFailurePayload)
	if !ok || fp == nil || fp.Pipe == nil || s.reqDB == nil {
		return payload, nil
	}
	uuid := s.pipeRequestLogID(fp.Pipe)
	if uuid == "" {
		return payload, nil
	}
	channel, _ := fp.Pipe.Metadata["__last_tried_channel"].(string)
	snap := responseSnapshot{
		StatusCode: fp.StatusCode,
		Body:       redactBody(fp.ErrorBody, s.redactOptions(fp.Pipe)),
	}
	respJSON, err := json.Marshal(snap)
	if err != nil {
		s.lg.Warn("request-log: 序列化失败快照失败", "request_id", fp.Pipe.RequestID, "err", err)
		return payload, nil
	}
	s.finishRequestLog(uuid, fp.StatusCode, string(respJSON), channel, "failed")
	return payload, nil
}

// responseSnapshot request_logs.response_json 的结构。
type responseSnapshot struct {
	StatusCode int            `json:"status_code"`
	Headers    headerSnapshot `json:"headers,omitempty"`
	Body       string         `json:"body,omitempty"`
	Truncated  bool           `json:"truncated,omitempty"` // 流式缓冲触顶截断标记
}

// pipeRequestLogID 取本次请求的 UUID：metadata 优先（同 pipe），丢失则按
// request_id 反查独立库（插件重建 pipe / 恢复场景）。
func (s *Service) pipeRequestLogID(pipe *modelgateway.ProxyPipeline) string {
	if pipe == nil {
		return ""
	}
	if pipe.Metadata != nil {
		// 本 attempt 未命中路由（哨兵）：直接返回空，跳过反查兜底——否则会反查到
		// 同 request_id 上一个已记录 attempt 的行，错误收尾/覆盖其日志（P1）。
		if skipped, _ := pipe.Metadata[skippedKey].(bool); skipped {
			return ""
		}
		if id, _ := pipe.Metadata[metadataKey].(string); id != "" {
			return id
		}
	}
	if s.reqDB == nil || pipe.RequestID == "" {
		return ""
	}
	var id string
	if err := s.reqDB.QueryRow(`SELECT id FROM request_logs WHERE request_id = ? ORDER BY created_at DESC LIMIT 1`, pipe.RequestID).Scan(&id); err != nil {
		return ""
	}
	return id
}

// finishRequestLog 收尾：写响应快照、状态码、结束时刻、时长、result；channel 非空时回填。
// finishRequestLog 收尾：响应体写块（响应各不相同，无去重价值，gzip 已压掉
// 重复）、manifest 落盘、request_logs 元数据行回填状态/耗时/结果。
// best-effort，出错仅记日志。幂等：清单已收尾（非 running）直接返回，对齐
// 旧版 finished_at IS NULL 语义（聚合末次失败 attempt 会先后收到两个收尾事件）。
func (s *Service) finishRequestLog(uuid string, status int, respJSON, channel, result string) {
	if s.blobs == nil {
		return // 块存储未装配：无正文可收尾
	}
	m, err := s.blobs.readManifest(uuid)
	if err != nil {
		s.lg.Warn("request-log: 收尾读清单失败", "id", uuid, "err", err)
		return
	}
	if m.Result != "running" {
		return
	}
	respHash, err := s.blobs.putBlob([]byte(respJSON))
	if err != nil {
		s.lg.Warn("request-log: 写响应块失败", "id", uuid, "err", err)
		return
	}
	m.Resp = respHash
	m.HTTPStatus = status
	m.Result = result
	if channel != "" {
		m.Channel = channel
	}
	if started, err := time.Parse(time.RFC3339Nano, m.StartedAt); err == nil {
		m.DurationMS = time.Since(started).Milliseconds()
		m.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if err := s.blobs.writeManifest(uuid, m); err != nil {
		s.lg.Warn("request-log: 收尾写清单失败", "id", uuid, "err", err)
	}
	if _, err := s.reqDB.Exec(`UPDATE request_logs SET http_status = ?, finished_at = ?, duration_ms = ?, result = ?, channel = CASE WHEN ? = '' THEN channel ELSE ? END WHERE id = ?`,
		status, m.FinishedAt, m.DurationMS, result, channel, channel, uuid); err != nil {
		s.lg.Warn("request-log: 收尾写元数据行失败", "id", uuid, "err", err)
	}
}

// ---- 输出方向：流式逐块拼接 ----

// streamBufferKey 流式响应累积缓冲（SSE 原文逐块拼接）在 metadata 中的键。
const streamBufferKey = "__request_log_buffer"

// streamTruncatedKey 流式缓冲触顶截断标记（response_json 会带 "truncated": true）。
const streamTruncatedKey = "__request_log_truncated"

// manifestKey 本次 attempt 的清单指针（*requestManifest）在 metadata 中的键：
// begin 时写入，收尾事件直接改内存对象并落盘，免去文件重读。
const manifestKey = "__request_log_manifest"

// maxStreamBuffer 流式响应缓冲上限（防大流式 OOM；超限后丢弃后续 chunk 并标记截断）。
var maxStreamBuffer = 32 << 20 // 32MB

// HandleStreamChunk 流式响应逐块触发（proxy.go:659，SSE 每行）。把 chunk 原文
// （脱敏后）追加到 metadata 缓冲；检测到 "data: [DONE]"（model-gateway 内部 isSSEDone
// 是私有函数，这里等价实现）时收尾：result=success、response_json=SSE 原文。
// 缓冲超 maxStreamBuffer 截断并标记 truncated。中断（断连/EOF 无 [DONE]）保持
// running，靠 self-heal 超时收尾。永不 return error。
func (s *Service) HandleStreamChunk(payload any) (any, error) {
	sp, ok := payload.(*modelgateway.StreamChunkPayload)
	if !ok || sp == nil || sp.Pipe == nil || s.reqDB == nil {
		return payload, nil
	}
	uuid := s.pipeRequestLogID(sp.Pipe)
	if uuid == "" {
		return payload, nil // 未被记录
	}
	if sp.Pipe.Metadata == nil {
		sp.Pipe.Metadata = map[string]any{}
	}
	buf, _ := sp.Pipe.Metadata[streamBufferKey].(*strings.Builder)
	if buf == nil {
		buf = &strings.Builder{}
		sp.Pipe.Metadata[streamBufferKey] = buf
	}
	// 触顶后丢弃后续 chunk（只保留截断标记），防止大流式无限累积
	if buf.Len() < maxStreamBuffer {
		buf.WriteString(redactBody(string(sp.Data), s.redactOptions(sp.Pipe)))
	} else {
		sp.Pipe.Metadata[streamTruncatedKey] = true
	}

	if !isSSEDoneLine(string(sp.Data)) {
		return payload, nil
	}
	truncated, _ := sp.Pipe.Metadata[streamTruncatedKey].(bool)
	snap := responseSnapshot{StatusCode: http.StatusOK, Body: buf.String(), Truncated: truncated}
	respJSON, err := json.Marshal(snap)
	if err != nil {
		s.lg.Warn("request-log: 序列化流式快照失败", "request_id", sp.Pipe.RequestID, "err", err)
		return payload, nil
	}
	channel, _ := sp.Pipe.Metadata["__last_tried_channel"].(string)
	s.finishRequestLog(uuid, http.StatusOK, string(respJSON), channel, "success")
	return payload, nil
}

// isSSEDoneLine 判断一条 SSE 行是否为流结束标记 data: [DONE]
// （允许 data:[DONE] 无空格写法），与 model-gateway 的 isSSEDone 语义一致。
func isSSEDoneLine(line string) bool {
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "data:") {
		return false
	}
	return strings.TrimSpace(line[len("data:"):]) == "[DONE]"
}

// ---- 查询：列表 / 详情 / self-heal ----

// requestLogItem 列表行（不含 request_json/response_json，避免大 payload）。
type requestLogItem struct {
	ID         string  `json:"id"`
	RequestID  string  `json:"request_id"`
	Model      string  `json:"model"`
	Channel    string  `json:"channel"`
	HTTPStatus int     `json:"http_status,omitempty"`
	Stream     bool    `json:"stream"`
	StartedAt  string  `json:"started_at"`
	FinishedAt *string `json:"finished_at,omitempty"`
	DurationMS int64   `json:"duration_ms,omitempty"`
	Result     string  `json:"result"`
}

// requestLogPage 列表响应（与计划定义一致：{items, total}，非 admin-api 裸数组）。
type requestLogPage struct {
	Items []requestLogItem `json:"items"`
	Total int              `json:"total"`
}

// requestLogDetail 详情行：列表字段 + 完整 request/response JSON。
type requestLogDetail struct {
	ID           string          `json:"id"`
	RequestID    string          `json:"request_id"`
	Model        string          `json:"model"`
	Channel      string          `json:"channel"`
	HTTPStatus   int             `json:"http_status,omitempty"`
	Stream       bool            `json:"stream"`
	StartedAt    string          `json:"started_at"`
	FinishedAt   *string         `json:"finished_at,omitempty"`
	DurationMS   int64           `json:"duration_ms,omitempty"`
	Result       string          `json:"result"`
	RequestJSON  json.RawMessage `json:"request_json"`
	ResponseJSON json.RawMessage `json:"response_json,omitempty"`
	BodyError    string          `json:"body_error,omitempty"` // 请求体块缺失/损坏无法还原时的错误说明
}

// requestLogFilter 列表过滤条件（对应 GET /api/request-logs 的 query 参数）。
type requestLogFilter struct {
	Model      string
	Channel    string
	RequestID  string
	Result     string
	StatusCode int  // 0 = 未过滤
	Stream     *int // nil = 未过滤（0/1 才过滤；用指针避免零值歧义）
	From       *time.Time
	To         *time.Time
	Limit      int
	Offset     int
}

// listWhere 返回 List/Count 共用的 WHERE 子句与参数。
func listWhere(filter requestLogFilter) (string, []any) {
	query := ` WHERE 1=1`
	args := []any{}
	if filter.Model != "" {
		query += ` AND model = ?`
		args = append(args, filter.Model)
	}
	if filter.Channel != "" {
		query += ` AND channel = ?`
		args = append(args, filter.Channel)
	}
	if filter.RequestID != "" {
		query += ` AND request_id = ?`
		args = append(args, filter.RequestID)
	}
	if filter.Result != "" {
		query += ` AND result = ?`
		args = append(args, filter.Result)
	}
	if filter.StatusCode != 0 {
		query += ` AND http_status = ?`
		args = append(args, filter.StatusCode)
	}
	if filter.Stream != nil {
		query += ` AND stream = ?`
		args = append(args, *filter.Stream)
	}
	if filter.From != nil {
		query += ` AND started_at >= ?`
		args = append(args, filter.From.UTC().Format(time.RFC3339Nano))
	}
	if filter.To != nil {
		query += ` AND started_at <= ?`
		args = append(args, filter.To.UTC().Format(time.RFC3339Nano))
	}
	return query, args
}

// List 列表 + 搜索（分页：Limit 默认 100、上限 500；Offset 可选）。
// 先对超时的 running 行批量 self-heal（P0：ProxyUpstreamFailed 仅聚合模型触发，
// 普通模型失败无输出事件，只能靠这里兜底收尾），再查当前页。
func (s *Service) List(ctx context.Context, filter requestLogFilter) (requestLogPage, error) {
	if s.reqDB == nil {
		return requestLogPage{}, fmt.Errorf("request-log: 独立库未装配")
	}
	s.healStuckList(ctx)
	where, whereArgs := listWhere(filter)
	var total int
	if err := s.reqDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM request_logs`+where, whereArgs...).Scan(&total); err != nil {
		return requestLogPage{}, err
	}
	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	query := `SELECT id, request_id, model, channel, COALESCE(http_status, 0), stream, started_at, finished_at, COALESCE(duration_ms, 0), result FROM request_logs` + where + ` ORDER BY started_at DESC LIMIT ? OFFSET ?`
	args := append(whereArgs, limit, offset)
	rows, err := s.reqDB.QueryContext(ctx, query, args...)
	if err != nil {
		return requestLogPage{}, err
	}
	defer rows.Close()
	items := make([]requestLogItem, 0)
	for rows.Next() {
		var it requestLogItem
		var finished sql.NullString
		var status int
		var stream int
		if err := rows.Scan(&it.ID, &it.RequestID, &it.Model, &it.Channel, &status, &stream, &it.StartedAt, &finished, &it.DurationMS, &it.Result); err != nil {
			return requestLogPage{}, err
		}
		it.HTTPStatus = status
		it.Stream = stream != 0
		if finished.Valid {
			it.FinishedAt = &finished.String
		}
		items = append(items, it)
	}
	return requestLogPage{Items: items, Total: total}, rows.Err()
}

// healStuckList 批量收尾超时 running 清单（List 每次调用前执行）。running 行
// 数量少（正常秒级收尾），扫描开销可忽略。
func (s *Service) healStuckList(ctx context.Context) {
	if s.blobs == nil {
		return
	}
	ids, err := s.blobs.manifestIDs()
	if err != nil {
		return
	}
	for _, id := range ids {
		m, err := s.blobs.readManifest(id)
		if err != nil || m.Result != "running" {
			continue
		}
		started, err := time.Parse(time.RFC3339Nano, m.StartedAt)
		if err != nil {
			continue
		}
		s.healStuckBlob(id, m.RequestID, started)
	}
}

// Detail 按 UUID 查详情：读清单 → 逐块取回正文 → 组装成 requestSnapshot /
// responseSnapshot 结构（与旧表时代的 API 结构一致，前端零改动）。
// 卡 running 超时先 self-heal 再返回。
func (s *Service) Detail(ctx context.Context, id string) (requestLogDetail, error) {
	if s.blobs == nil {
		return requestLogDetail{}, fmt.Errorf("request-log: 块存储未装配")
	}
	if !s.blobs.hasManifest(id) {
		return requestLogDetail{}, sql.ErrNoRows // 未命中：handler 转 404
	}
	m, err := s.blobs.readManifest(id)
	if err != nil {
		return requestLogDetail{}, err
	}
	d := requestLogDetail{
		ID:         m.ID,
		RequestID:  m.RequestID,
		Model:      m.Model,
		Channel:    m.Channel,
		HTTPStatus: m.HTTPStatus,
		Stream:     m.Stream,
		StartedAt:  m.StartedAt,
		DurationMS: m.DurationMS,
		Result:     m.Result,
	}
	if m.FinishedAt != "" {
		f := m.FinishedAt
		d.FinishedAt = &f
	}
	// 组装 request_json：envelope（headers/method/path）+ body（拼回的请求体）。
	var snap requestSnapshot
	bodyErr := ""
	if m.Envelope != "" {
		envData, err := s.blobs.getBlob(m.Envelope)
		if err == nil {
			_ = json.Unmarshal(envData, &snap)
		}
	}
	body, err := s.blobs.rebuildRequestBody(m)
	if err != nil {
		// 不静默吞错：块缺失/损坏时把原因带给前端（body_error），而不是
		// 让用户以为请求本来就没 body（真实事故：GC 误删后详情页 body 显示为空）。
		bodyErr = err.Error()
	}
	if body != "" {
		// 部分块缺失时 rebuildRequestBody 仍返回已还原的部分（占位符标注缺失块），
		// 优先展示，err 只作提示。
		snap.Body = body
	}
	snap.Model = m.Model
	snap.Stream = m.Stream
	if reqJSON, err := json.Marshal(snap); err == nil {
		d.RequestJSON = reqJSON
	}
	if bodyErr != "" {
		d.BodyError = bodyErr
	}
	// 响应块 → response_json
	if m.Resp != "" {
		if respData, err := s.blobs.getBlob(m.Resp); err == nil {
			d.ResponseJSON = json.RawMessage(respData)
		}
	}
	// self-heal：卡 running 超时收尾（对齐旧版语义）
	if d.Result == "running" && d.FinishedAt == nil {
		if parsed, err := time.Parse(time.RFC3339Nano, m.StartedAt); err == nil {
			s.healStuckBlob(id, m.RequestID, parsed)
			if m2, err := s.blobs.readManifest(id); err == nil {
				d.Result = m2.Result
				d.HTTPStatus = m2.HTTPStatus
				d.DurationMS = m2.DurationMS
				if m2.FinishedAt != "" {
					f := m2.FinishedAt
					d.FinishedAt = &f
				}
				if m2.Resp != "" {
					if respData, err := s.blobs.getBlob(m2.Resp); err == nil {
						d.ResponseJSON = json.RawMessage(respData)
					}
				}
			}
		}
	}
	return d, nil
}

// healStuckBlob 自愈收尾：反查 route_attempts / route_requests 拿错误体，
// 走 finishRequestLog 落盘。
func (s *Service) healStuckBlob(id, requestID string, started time.Time) {
	if time.Since(started) < config.RouteLogSelfHealTimeout {
		return
	}
	result := "stream_interrupted"
	status := 0
	errBody := ""
	if s.loadout != nil {
		var attemptErr string
		var attemptStatus int
		if err := s.loadout.QueryRow(`SELECT COALESCE(error_body, ''), COALESCE(status_code, 0) FROM route_attempts WHERE request_log_id = ? AND result = 'failed' ORDER BY started_at DESC LIMIT 1`, id).Scan(&attemptErr, &attemptStatus); err == nil && (attemptErr != "" || attemptStatus != 0) {
			errBody = attemptErr
			status = attemptStatus
			result = "failed"
		} else {
			var rr string
			if err := s.loadout.QueryRow(`SELECT COALESCE(result, '') FROM route_requests WHERE request_id = ?`, requestID).Scan(&rr); err == nil && rr == "failed" {
				result = "failed"
				_ = s.loadout.QueryRow(`SELECT COALESCE(http_status, 0) FROM route_requests WHERE request_id = ?`, requestID).Scan(&status)
				_ = s.loadout.QueryRow(`SELECT COALESCE(error_body, '') FROM route_requests WHERE request_id = ?`, requestID).Scan(&errBody)
			}
		}
	}
	respJSON := ""
	if errBody != "" {
		// 自愈收尾拿不到当时的路由配置（manifest 不存 keep_base64），按默认打码处理；
		// 正常链路（after-upstream / stream-chunk / attempt-failed）都用当次请求的口径。
		snap := responseSnapshot{StatusCode: status, Body: redactBody(errBody, redactOptions{Enabled: s.redactEnabled()})}
		if b, err := json.Marshal(snap); err == nil {
			respJSON = string(b)
		}
	}
	s.finishRequestLog(id, status, respJSON, "", result)
}

// healStuckList 批量收尾超时 running 行（List 每次调用前执行）。

// ---- HTTP handlers（RegisterRoute 注册，Auth: AuthSession 由框架挂 session） ----

// handleList GET /api/request-logs
func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	filter := requestLogFilter{
		Model:     r.URL.Query().Get("model"),
		Channel:   r.URL.Query().Get("channel"),
		RequestID: r.URL.Query().Get("request_id"),
		Result:    r.URL.Query().Get("result"),
	}
	if v := r.URL.Query().Get("from"); v != "" {
		if parsed, err := time.Parse(time.RFC3339, v); err == nil {
			filter.From = &parsed
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if parsed, err := time.Parse(time.RFC3339, v); err == nil {
			filter.To = &parsed
		}
	}
	if v := r.URL.Query().Get("status_code"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			filter.StatusCode = n
		}
	}
	if v := r.URL.Query().Get("stream"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && (n == 0 || n == 1) {
			filter.Stream = &n
		}
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			filter.Limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			filter.Offset = n
		}
	}
	page, err := s.List(r.Context(), filter)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{"message": err.Error()}})
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// ExistingIDs 返回 candidates 里仍然存在于本库的 id 集合。
//
// route-log 列表用它判断「进入日志」入口是否还有效：关联列是历史快照，日志行可能
// 已被保留策略或手动清空删除。按每批 500 个分段查询，避免一次性拼出超长 IN 子句
// （SQLite 默认变量上限 999）。
func (s *Service) ExistingIDs(ctx context.Context, candidates []string) (map[string]bool, error) {
	found := make(map[string]bool, len(candidates))
	if s.reqDB == nil || len(candidates) == 0 {
		return found, nil
	}
	const chunk = 500
	for start := 0; start < len(candidates); start += chunk {
		end := start + chunk
		if end > len(candidates) {
			end = len(candidates)
		}
		batch := candidates[start:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		args := make([]any, len(batch))
		for i, id := range batch {
			args[i] = id
		}
		rows, err := s.reqDB.QueryContext(ctx, `SELECT id FROM request_logs WHERE id IN (`+placeholders+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			found[id] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return found, nil
}

// handleStats GET /api/request-logs/stats
// 「日志大小」按钮的数据源：文件占用 + 行数 + 时间范围 + 当前保留配置。
func (s *Service) handleStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Stats(r.Context()))
}

// handleApplyRetention POST /api/request-logs/retention:apply
// 立即按当前保留策略清理一次，返回清理后的统计。
//
// 存在的理由：自动清理只在「写新日志」和「服务启动」时触发。用户刚把上限从
// 无限改成 1000MB、库里已经堆了 15GB 时，不该逼他先发一个请求或重启才能看到效果。
// 设置页保存后与「立即清理」按钮都打这个接口。
func (s *Service) handleApplyRetention(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.ApplyCurrentRetention(r.Context()))
}

// handleClear DELETE /api/request-logs
// 清空完整请求日志（用户确认后才调；库可能很大，前端会先弹确认框）。
func (s *Service) handleClear(w http.ResponseWriter, r *http.Request) {
	affected, err := s.Clear(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{"message": err.Error()}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "affected": affected})
}

// handleDetail GET /api/request-logs/{id}
func (s *Service) handleDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	detail, err := s.Detail(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"message": "该请求未记录完整日志"}})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{"message": err.Error()}})
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// handleReplayScript POST /api/request-logs/{id}/replay-script
//
// 把日志里的原始请求体生成一个可直接运行的 Node.js 复现脚本，写到
// <数据目录>/replay-scripts/<id>.mjs，返回绝对路径。前端只负责展示路径，
// 由用户自己在终端运行——调试 400 时不依赖管理台的模型测试页。
//
// 注意：脚本内容来自 request-log 记录的 body，图片 base64 在写日志时已被
// 替换成占位符（除非该路由开启了完整保留）。需要逐字节复现请用请求捕获插件
// 生成的脚本，那份是原始报文。
func (s *Service) handleReplayScript(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	detail, err := s.Detail(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"message": "该请求未记录完整日志"}})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{"message": err.Error()}})
		return
	}
	body, path, err := replayBodyFromDetail(detail)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": err.Error()}})
		return
	}
	scriptPath, size, err := s.WriteReplayScript(id, body, ReplayScriptOptions{
		RequestID: detail.RequestID,
		Path:      path,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{"message": err.Error()}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"path":  scriptPath,
		"bytes": size,
	})
}

// writeJSON 统一 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// newRequestLogID 生成 UUID：crypto/rand 16 字节 → 32 位 hex（零依赖，够唯一）。
func newRequestLogID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// requestSnapshot request_logs.request_json 的结构（body 存原始字符串保真，前端再格式化）。
type requestSnapshot struct {
	Method  string         `json:"method"`
	Path    string         `json:"path"`
	Query   string         `json:"query"`
	Headers headerSnapshot `json:"headers"`
	Body    string         `json:"body"`
	Model   string         `json:"model"`
	Stream  bool           `json:"stream"`
}

// buildRequestSnapshot 序列化请求快照（脱敏 + 可选 base64 图片占位）。
func buildRequestSnapshot(pipe *modelgateway.ProxyPipeline, model string, opts redactOptions) requestSnapshot {
	return requestSnapshot{
		Method:  pipe.Request.Method,
		Path:    pipe.Request.Path,
		Query:   pipe.Request.Query,
		Headers: redactHeaders(pipe.Request.Header, opts.Enabled),
		Body:    redactBody(string(pipe.Request.Body), opts),
		Model:   model,
		Stream:  pipe.Request.Stream,
	}
}

// ---- 脱敏 ----

// sensitiveHeaderKeys 打码的敏感头（不区分大小写，子串匹配）。
var sensitiveHeaderKeys = []string{"authorization", "api-key", "x-api-key", "cookie", "proxy-authorization"}

// headerSnapshot HTTP headers 快照：底层仍是 map[string][]string，但 JSON
// 序列化时单值输出为字符串，多值保留数组，避免前端看到所有 header 都是数组。
type headerSnapshot map[string][]string

func (h headerSnapshot) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, len(h))
	for k, vv := range h {
		switch len(vv) {
		case 0:
			m[k] = ""
		case 1:
			m[k] = vv[0]
		default:
			m[k] = vv
		}
	}
	return json.Marshal(m)
}

// Get 模拟 http.Header.Get：返回 key 的第一个值（大小写不敏感）。
func (h headerSnapshot) Get(key string) string {
	return http.Header(h).Get(key)
}

// UnmarshalJSON 支持单值字符串或多值数组两种写法，兼容序列化后的 JSON。
func (h *headerSnapshot) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	out := make(headerSnapshot, len(raw))
	for k, v := range raw {
		var arr []string
		if err := json.Unmarshal(v, &arr); err == nil {
			out[k] = arr
			continue
		}
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			out[k] = []string{s}
			continue
		}
		return fmt.Errorf("header %q: cannot unmarshal %s", k, v)
	}
	*h = out
	return nil
}

// redactHeaders 复制 headers，敏感键的值替换为 ***。
func redactHeaders(h http.Header, enabled bool) headerSnapshot {
	out := make(headerSnapshot, len(h))
	for k, vv := range h {
		if enabled && isSensitiveHeader(k) {
			out[k] = []string{"***"}
			continue
		}
		out[k] = append([]string(nil), vv...)
	}
	return out
}

func isSensitiveHeader(k string) bool {
	lk := strings.ToLower(k)
	for _, sk := range sensitiveHeaderKeys {
		if strings.Contains(lk, sk) {
			return true
		}
	}
	return false
}

// redactBody 对 body 文本做脱敏：sk- 密钥打码、按配置决定 base64 data URI 是否转占位标记。
//
// KeepBase64=true（能力路由开了「保留原始 base64」）只跳过图片占位，sk- 密钥
// 仍然打码——图片原文是调试需要的，密钥原文任何时候都不该落日志。
func redactBody(body string, opts redactOptions) string {
	// sk- 后跟 4+ 位字母数字才算密钥（避免误伤普通文本，且已打码的 sk-*** 不重复打码）
	// 不论总开关是否开启都执行：密钥打码是安全底线，不随「脱敏」偏好关闭。
	body = skSecretRegex.ReplaceAllString(body, "sk-***")
	if !opts.Enabled {
		return body
	}
	if opts.KeepBase64 {
		return body
	}
	return dataURIRegex.ReplaceAllStringFunc(body, func(m string) string {
		parts := dataURIRegex.FindStringSubmatch(m)
		if len(parts) != 3 {
			return m
		}
		// 只算字节大小（解码占位统计用），不存图片字节
		n := len(parts[2]) * 3 / 4
		if decoded, derr := base64.StdEncoding.DecodeString(parts[2]); derr == nil {
			n = len(decoded)
		}
		return fmt.Sprintf("[image: %s, %dB]", parts[1], n)
	})
}

// skSecretRegex 匹配 sk- 开头的 API 密钥（4+ 位字母数字；已打码的 sk-*** 不命中）。
var skSecretRegex = regexp.MustCompile(`sk-[A-Za-z0-9]{4,}`)

// dataURIRegex 匹配 data:<mime>;base64,<payload>。
var dataURIRegex = regexp.MustCompile(`data:([a-zA-Z0-9.+-]+/[a-zA-Z0-9.+-]+);base64,([A-Za-z0-9+/=]+)`)

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
