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
	"regexp"
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

// Service 完整请求日志适配器：查能力路由、抓请求/响应快照落独立库。
// 所有写库 best-effort：handler 永不 return error（否则中断整个 /v1 转发），
// 出错仅记日志。
type Service struct {
	st      *store.Store
	lg      *slog.Logger
	reqDB   *sql.DB        // 独立库 request-log.db（request_logs / request_log_config）
	loadout *sql.DB        // loadout.db：UPDATE route_requests.request_log_id 关联列
	repo    *db.Repository // SQLite 能力路由数据源（装配后注入；nil 时回退 JSON）
	// dbPath 独立库文件路径：Stats 报「日志占用空间」要的是文件大小（含 WAL），
	// 不能只算表大小（表和实际磁盘占用差得多）。空路径时 Stats 回落为 0。
	dbPath string
}

// NewService 创建完整请求日志适配器。
// database 为 loadout.db（写关联列用，测试可传 nil 跳过 UPDATE）。
func NewService(st *store.Store, lg *slog.Logger, reqDB, database *sql.DB) *Service {
	return &Service{st: st, lg: lg, reqDB: reqDB, loadout: database}
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

// retentionMinKeep 容量清理的下限保护：无论阈值多小，至少保留最近这么多条，
// 避免用户把 MaxSizeMB 设成 1 时把库清得只剩 0 条（前端也设了下限，这里是后端兜底）。
const retentionMinKeep = 100

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
	result, err := s.reqDB.ExecContext(ctx, `DELETE FROM request_logs`)
	if err != nil {
		return 0, err
	}
	affected, _ := result.RowsAffected()
	// 删完必须把空间真正还回去，否则用户点「清空」后看到大小几乎没变，会以为没生效：
	//   - DELETE 只是把页标记为空闲，文件不会自动收缩；
	//   - VACUUM 重建库文件、把空闲页还给操作系统（这是唯一真正缩小文件的办法）；
	//   - 顺序要紧：先 checkpoint 把 WAL 内容并回主文件，VACUUM 才能看到全部空闲页，
	//     否则 WAL 里未合并的部分会在 VACUUM 后又被写回来。
	s.compactedSize(ctx)
	if s.loadout != nil {
		if _, err := s.loadout.ExecContext(ctx, `UPDATE route_requests SET request_log_id = NULL WHERE COALESCE(request_log_id, '') <> ''`); err != nil {
			s.lg.Warn("request-log: 清空关联列失败", "err", err)
		}
	}
	return affected, nil
}

// ApplyRetention 按保留策略清理旧日志（FIFO：先删最旧的）。
//
// 双通道，任意一个开启即生效：
//   - 时间：删除 started_at 早于 now-MaxAgeDays 的行；
//   - 容量：删除后如果文件仍超过 MaxSizeMB，按 started_at 升序继续删最旧的行，
//     直到降到阈值以下（或只剩 retentionMinKeep 条）。
//
// 容量通道按「文件大小」而不是「行大小求和」判断：日志主体是 request_json/
// response_json 大文本，行数无法反映真实占用。删除后 SQLite 不会自动归还磁盘，
// 循环里按估算的已删字节推算，收尾再 checkpoint 一次拿真实值。
//
// 调用点：写日志后（HandleBeforeAttempt 末尾，best-effort）、服务启动时一次、
// 以及在设置页保存保留策略 / 点「立即清理」时手动触发（用户改完上限不必等下一个请求）。
// 出错只记日志，绝不阻塞请求。
func (s *Service) ApplyRetention(ctx context.Context, cfg RetentionConfig) {
	if s.reqDB == nil || (cfg.MaxAgeDays <= 0 && cfg.MaxSizeMB <= 0) {
		return
	}
	if cfg.MaxAgeDays > 0 {
		cutoff := time.Now().UTC().AddDate(0, 0, -cfg.MaxAgeDays).Format(retentionTimeFormat)
		if _, err := s.reqDB.ExecContext(ctx, `DELETE FROM request_logs WHERE started_at < ?`, cutoff); err != nil {
			s.lg.Warn("request-log: 按天数清理日志失败", "err", err)
		}
	}
	if cfg.MaxSizeMB > 0 {
		s.trimBySize(ctx, int64(cfg.MaxSizeMB)*1024*1024)
	}
}

// ApplyCurrentRetention 按当前设置里的策略清理一次，返回执行后的统计。
// 供 HTTP 手动触发（设置页「立即清理」/ 保存设置后立刻见效）与启动时调用。
func (s *Service) ApplyCurrentRetention(ctx context.Context) RequestLogStats {
	s.ApplyRetention(ctx, s.currentRetention())
	return s.Stats(ctx)
}

// trimBySize 容量通道：文件超过 limit 时从最旧的行开始删（FIFO），直到低于 limit
// 或触及 retentionMinKeep 下限。
//
// 【为什么不能只看文件大小】SQLite 删行只是把页标成空闲、文件不会跟着缩小，只有
// VACUUM 才真正还磁盘。所以「删一轮 → 看文件大小 → 还超就再删」如果在中间不做
// VACUUM，就会一路删到保留下限（曾实际发生：301 行只有 19MB、上限 20MB 却被删到
// 100 行，用户白丢日志）。
//
// 【为什么不能只按内容体积估算】反过来，纯估算（把 length(request_json)+... 求和
// 当作占用）也有个坑：SQLite 还有页头、B 树碎片、索引、schema 等固定开销，估算值
// 与文件实际大小对不上。差得少时会出现「估算说到顶了、文件其实还超」，于是反复
// VACUUM 却怎么都不达标。
//
// 【做法】分两步，各自只做一次重活：
//  1. 按 bytes 列估算：从最新往旧累加，找到「累计量 <= 预算」的那批行留下，
//     一次 DELETE 掉更旧的全部（FIFO）。bytes 是写入时记好的，这一步只是纯
//     数值的窗口求和，不碰正文。
//  2. 收尾 VACUUM 一次，把删掉的空间真正还给磁盘。
//
// 【为什么不像早先那样循环 VACUUM】VACUUM 要重建整个库文件，是整个流程里唯一的
// 大头，且耗时与库大小成正比（实测 300MB 约 1.2s，15GB 要 1 分钟左右）。早先每轮
// 都 VACUUM 来「拿真实大小判断还超不超」，300MB 的库要跑十几秒、15GB 就是十几
// 分钟。但 VACUUM 只影响文件占多少磁盘、不影响还剩几行数据——把判断依据换成
// bytes（内容体积）后，一次就能删到位，VACUUM 只需要收尾那一次。
//
// 【估算偏差怎么补】内容体积总是略小于文件大小（页头、索引、碎片），所以先在
// 预算里按比例留出余量；VACUUM 后若仍超限，用实测到的「文件/内容」比值再删一轮
// （最多 allowExtraRounds 轮，避免极端情况无限重试）。
func (s *Service) trimBySize(ctx context.Context, limit int64) {
	if limit <= 0 {
		return
	}
	var total int64
	if err := s.reqDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM request_logs`).Scan(&total); err != nil {
		s.lg.Warn("request-log: 清理前统计行数失败", "err", err)
		return
	}
	if total <= retentionMinKeep {
		s.lg.Info("request-log: 已触及保留下限，停止容量清理",
			"limit", limit, "remaining", total, "min_keep", retentionMinKeep)
		s.compactOnly(ctx)
		return
	}

	// 删除辅助：删掉当前最旧的 need 行（FIFO），返回真实删掉的行数。
	deleteOldest := func(need int64) int64 {
		if need <= 0 {
			return 0
		}
		result, err := s.reqDB.ExecContext(ctx,
			`DELETE FROM request_logs WHERE id IN (SELECT id FROM request_logs ORDER BY started_at ASC LIMIT ?)`, need)
		if err != nil {
			s.lg.Warn("request-log: 按容量清理日志失败", "err", err)
			return 0
		}
		n, _ := result.RowsAffected()
		return n
	}

	// 先量一次真实文件大小，没超限就直接收工（大多数「写日志后顺手清理」都走这里，
	// 此时超限通常只是 WAL 没合并，VACUUM 一次让文件回落即可）。
	size := s.diskSize()
	if size <= limit {
		return
	}

	// 按 bytes 估算要保留的行数，一次删到位。
	if keep := s.keepRowsWithin(ctx, limit); keep < total {
		if keep < retentionMinKeep {
			keep = retentionMinKeep
		}
		if deleted := deleteOldest(total - keep); deleted > 0 {
			total -= deleted
			s.lg.Info("request-log: 容量清理删除旧日志",
				"deleted", deleted, "remaining", total, "limit", limit)
		}
	}

	// 收尾 VACUUM：把删掉的空间真正还给磁盘（唯一能让文件变小的手段）。
	after := s.compactedSize(ctx)
	if after <= limit {
		s.lg.Info("request-log: 容量清理完成", "size", after, "limit", limit, "remaining", total)
		return
	}

	// 仍超限：说明「文件 / 内容体积」的比值比预估更大（碎片、索引膨胀等）。
	// 用实测比值反推还要删多少行，最多再补几轮；每轮只多一次 VACUUM。
	const allowExtraRounds = 3
	for round := 1; round <= allowExtraRounds; round++ {
		if total <= retentionMinKeep {
			s.lg.Warn("request-log: 已触及保留下限，清理后仍超限（可能单条日志就超过上限）",
				"size", after, "limit", limit, "remaining", total, "min_keep", retentionMinKeep)
			return
		}
		// 超出量按比例折算成行数：剩余行均摊了 after 的占用，删掉超标那部分即可。
		need := (after - limit) * total / after
		if need < 1 {
			need = 1
		}
		if total-need < retentionMinKeep {
			need = total - retentionMinKeep
		}
		deleted := deleteOldest(need)
		if deleted <= 0 {
			return
		}
		total -= deleted
		after = s.compactedSize(ctx)
		if after <= limit {
			s.lg.Info("request-log: 容量清理完成", "size", after, "limit", limit, "remaining", total, "rounds", round)
			return
		}
		s.lg.Info("request-log: 容量清理补删一轮", "round", round, "size", after, "deleted", deleted, "remaining", total)
	}
	s.lg.Warn("request-log: 容量清理后仍超限", "size", after, "limit", limit, "remaining", total)
}

// keepRowsWithin 估算「保留最近多少行时，内容体积落到预算内」。
//
// 从最新往旧累加写入时记好的 bytes，找累计量 <= 预算的那批行——纯数值窗口求和，
// 不需要把 request_json/response_json 读出来量长度。
//
// 【索引方向很要命】rn 由 ROW_NUMBER() OVER (ORDER BY started_at DESC) 得到，
// 最新一行的 rn = 1。要「保留最新的 k 行」，就得问「rn <= k 的这批行内容量是否
// 还装得下」——所以这里是按 rn 升序（从最新往旧）累加，然后取满足 acc <= budget
// 的 MAX(rn)，它就是能保留的最新行数 k。
//
// 注意别把累加方向写反（写成 ORDER BY rn DESC 就是从最旧往新累加，acc 命中的
// 是最旧的几行，算出来的数含义完全不同，会导致怎么都删不到位）。
func (s *Service) keepRowsWithin(ctx context.Context, limit int64) int64 {
	budget := s.contentBudget(limit)
	var keep int64
	query := `
		SELECT COALESCE(MAX(rn), 0) FROM (
			SELECT rn, SUM(row_bytes) OVER (ORDER BY rn ASC) AS acc
			FROM (
				SELECT ROW_NUMBER() OVER (ORDER BY started_at DESC) AS rn, bytes AS row_bytes
				FROM request_logs
			)
		) WHERE acc <= ?`
	if err := s.reqDB.QueryRowContext(ctx, query, budget).Scan(&keep); err != nil {
		s.lg.Warn("request-log: 估算可保留行数失败", "err", err)
		return 0
	}
	return keep
}

// sizeRatio 文件大小 / 内容体积 的兜底比值。
//
// SQLite 的文件除了正文还有页头、B 树填充、各索引、schema。实测这个比值很接近 1
// （3000 行 × 64KB：1.009；剩 300 行：1.011），因为没有额外的独立索引占大块空间。
// 取 1.05 略偏保守，既留出碎片余量，又不会像 1.10 那样多删掉一成的日志。
const sizeRatio = 1.05

// fixedSizeOverhead 库里与行数无关的固定占用（schema、页头等），约 256KB。
const fixedSizeOverhead = 256 * 1024

// contentBudget 把「目标文件大小」折算成「允许的内容体积」。
//
// 反比折算：文件 ≈ 内容 × sizeRatio + 固定开销，所以要留的内容体积就是
// (目标 - 固定开销) / sizeRatio。再压 95% 留余量，避免刚清完又立刻超限。
func (s *Service) contentBudget(limit int64) int64 {
	target := limit - limit/20 // 95%
	budget := int64(float64(target-fixedSizeOverhead) / sizeRatio)
	if budget < 1 {
		budget = 1
	}
	return budget
}

// compactOnly 只做 VACUUM 收尾（把已删数据的空闲页还给磁盘），不改数据。
func (s *Service) compactOnly(ctx context.Context) {
	_, _ = s.reqDB.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	if _, err := s.reqDB.ExecContext(ctx, `VACUUM`); err != nil {
		s.lg.Warn("request-log: VACUUM 回收空间失败，文件暂不收缩", "err", err)
	}
	_, _ = s.reqDB.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
}

// compactedSize 当前的实时占用：先 checkpoint 把 WAL 并回主文件，再 VACUUM 收缩，
// 最后返回真实文件大小。用于「清空后」「容量清理收尾」这类需要真实数字的场合。
//
// 注意它很贵（VACUUM 重建整个库），大库上会明显耗时；只做判定时请用 diskSize。
func (s *Service) compactedSize(ctx context.Context) int64 {
	s.compactOnly(ctx)
	return s.diskSize()
}

// SetRepository 注入 SQLite 仓储（由装配层在 db 就绪后调用；测试可省略）。
// 同时用于能力路由查询与 route_requests.request_log_id 的 UPDATE（同一 loadout.db）。
func (s *Service) SetRepository(repo *db.Repository) { s.repo = repo }

// DecideRoutesScope 查能力路由表：model + 请求渠道上下文（含聚合模型的候选 Key 集合）。
// 返回所有匹配的路由；native（及历史 error 降级）立即返回，proxy 路由收集全部匹配项。
// 选择策略统一走 types.SelectCapabilityRoutes（与 vision/sensitive/field-filter 一致）。
// 读表/解析失败 fail-open：记录日志并返回 nil（按 native 透传），不拒绝请求。
func (s *Service) DecideRoutesScope(model string, scope types.ChannelRequestScope) ([]*types.CapabilityRoute, error) {
	if s.repo != nil {
		routes, err := s.repo.ListCapabilityRoutes(context.Background())
		if err == nil {
			return types.SelectCapabilityRoutes(routes, capabilityName, model, scope), nil
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
	return types.SelectCapabilityRoutes(routes, capabilityName, model, scope), nil
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
	routes, err := s.DecideRoutesScope(model, scope)
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

	// UPDATE 关联列（best-effort；loadout 连接为空或行不存在时忽略，不影响记录）。
	// COALESCE 条件保证仅首次命中写入——外层按钮指向第一次渠道尝试的日志。
	if s.loadout != nil {
		if _, err := s.loadout.Exec(`UPDATE route_requests SET request_log_id = ? WHERE request_id = ? AND COALESCE(request_log_id, '') = ''`, uuid, pipe.RequestID); err != nil {
			s.lg.Warn("request-log: 写 route_requests.request_log_id 失败", "request_id", pipe.RequestID, "err", err)
		}
	}

	channel, _ := pipe.Metadata["__current_channel"].(string)
	started := time.Now().UTC()
	snap := buildRequestSnapshot(pipe, model, s.redactEnabled())
	reqJSON, err := json.Marshal(snap)
	if err != nil {
		s.lg.Warn("request-log: 序列化请求快照失败", "request_id", pipe.RequestID, "err", err)
		return payload, nil
	}
	// 纯 INSERT：每次 attempt 独立 UUID 恒不冲突（无需 ON CONFLICT 分支）。
	// bytes 在这里先记「请求体 + 固定开销」；响应收尾时再重算一次总量。
	if _, err := s.reqDB.Exec(`INSERT INTO request_logs(id, request_id, model, channel, stream, started_at, result, request_json, bytes, created_at) VALUES (?, ?, ?, ?, ?, ?, 'running', ?, ?, ?)`,
		uuid, pipe.RequestID, model, channel, boolToInt(pipe.Request.Stream), started.Format(time.RFC3339Nano), string(reqJSON), rowBytes(string(reqJSON), ""), started.Format(time.RFC3339Nano)); err != nil {
		s.lg.Warn("request-log: 写 request_logs 半条失败", "request_id", pipe.RequestID, "err", err)
		return payload, nil
	}
	// 保留策略：新日志写入后顺带清理一次旧数据（best-effort，同步执行但只删少量行）。
	// 放在这里而不是定时器：库只会被请求撑大，写日志的时刻就是最该检查容量的时刻。
	s.ApplyRetention(context.Background(), s.currentRetention())
	return payload, nil
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
	snap := responseSnapshot{
		StatusCode: ap.Response.StatusCode,
		Headers:    redactHeaders(ap.Response.Header, s.redactEnabled()),
		Body:       redactBody(string(ap.Response.Body), s.redactEnabled()),
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
		Body:       redactBody(fp.ErrorBody, s.redactEnabled()),
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
// best-effort，出错仅记日志。
func (s *Service) finishRequestLog(uuid string, status int, respJSON, channel, result string) {
	var startedStr string
	if err := s.reqDB.QueryRow(`SELECT started_at FROM request_logs WHERE id = ?`, uuid).Scan(&startedStr); err != nil {
		s.lg.Warn("request-log: 收尾时找不到记录", "id", uuid, "err", err)
		return
	}
	started, err := time.Parse(time.RFC3339Nano, startedStr)
	if err != nil {
		s.lg.Warn("request-log: 解析 started_at 失败", "id", uuid, "err", err)
		return
	}
	now := time.Now().UTC()
	duration := now.Sub(started).Milliseconds()
	// 【P1 幂等】WHERE 加 finished_at IS NULL：聚合末次失败 attempt 会先后收到
	// ProxyAttemptFailed（每次失败 emit）与 ProxyUpstreamFailed（聚合全败 failover）
	// 两个事件，重复收尾同一条；限定仅首次生效可避免二次 UPDATE 与结果覆盖。
	// bytes 就地重算：用库里的 length(request_json) 加上本次响应体长度与固定开销，
	// 不必把请求体读回内存。
	if _, err := s.reqDB.Exec(`UPDATE request_logs SET response_json = ?, http_status = ?, finished_at = ?, duration_ms = ?, result = ?, bytes = length(request_json) + length(?) + ?, channel = CASE WHEN ? = '' THEN channel ELSE ? END WHERE id = ? AND finished_at IS NULL`,
		respJSON, status, now.Format(time.RFC3339Nano), duration, result, respJSON, rowFixedOverhead, channel, channel, uuid); err != nil {
		s.lg.Warn("request-log: 收尾写库失败", "id", uuid, "err", err)
	}
}

// ---- 输出方向：流式逐块拼接 ----

// streamBufferKey 流式响应累积缓冲（SSE 原文逐块拼接）在 metadata 中的键。
const streamBufferKey = "__request_log_buffer"

// streamTruncatedKey 流式缓冲触顶截断标记（response_json 会带 "truncated": true）。
const streamTruncatedKey = "__request_log_truncated"

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
		buf.WriteString(redactBody(string(sp.Data), s.redactEnabled()))
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

// Detail 按 UUID 查详情。命中且卡 running 超时 → 先 self-heal 再返回；
// 未命中返回 sql.ErrNoRows 语义的错误（由 handler 转 404）。
func (s *Service) Detail(ctx context.Context, id string) (requestLogDetail, error) {
	if s.reqDB == nil {
		return requestLogDetail{}, fmt.Errorf("request-log: 独立库未装配")
	}
	var d requestLogDetail
	var status int
	var stream int
	var finished sql.NullString
	var respJSON sql.NullString
	var reqJSON string
	var started string
	if err := s.reqDB.QueryRowContext(ctx, `SELECT id, request_id, model, channel, COALESCE(http_status, 0), stream, started_at, finished_at, COALESCE(duration_ms, 0), result, request_json, response_json FROM request_logs WHERE id = ?`, id).
		Scan(&d.ID, &d.RequestID, &d.Model, &d.Channel, &status, &stream, &started, &finished, &d.DurationMS, &d.Result, &reqJSON, &respJSON); err != nil {
		return requestLogDetail{}, err
	}
	d.HTTPStatus = status
	d.Stream = stream != 0
	d.StartedAt = started
	d.RequestJSON = json.RawMessage(reqJSON)
	if finished.Valid {
		d.FinishedAt = &finished.String
	}
	if respJSON.Valid && respJSON.String != "" {
		d.ResponseJSON = json.RawMessage(respJSON.String)
	}
	// self-heal：卡 running 超时（断连/EOF 无 [DONE]、failover 中断等）收尾
	if d.Result == "running" && !finished.Valid {
		if parsed, err := time.Parse(time.RFC3339Nano, started); err == nil {
			s.healStuck(d.ID, d.RequestID, parsed)
			// 重读收尾结果（含 response_json：heal 可能从 route_requests 还原 error_body）
			if err := s.reqDB.QueryRowContext(ctx, `SELECT result, finished_at, COALESCE(http_status, 0), COALESCE(duration_ms, 0), response_json FROM request_logs WHERE id = ?`, id).
				Scan(&d.Result, &finished, &status, &d.DurationMS, &respJSON); err == nil {
				if finished.Valid {
					d.FinishedAt = &finished.String
				}
				d.HTTPStatus = status
				if respJSON.Valid && respJSON.String != "" {
					d.ResponseJSON = json.RawMessage(respJSON.String)
				}
			}
		}
	}
	return d, nil
}

// healStuckList 批量收尾超时 running 行（List 每次调用前执行）。
// running 行数量少（正常秒级收尾），扫描开销可忽略。
// 注意：必须先收集并 Close rows 再写库——SQLite 单连接（SetMaxOpenConns(1)）下
// 在 rows 迭代期间执行写操作会让新查询阻塞等连接释放，死锁。
func (s *Service) healStuckList(ctx context.Context) {
	rows, err := s.reqDB.QueryContext(ctx, `SELECT id, request_id, started_at FROM request_logs WHERE result = 'running' AND finished_at IS NULL`)
	if err != nil {
		return
	}
	type stuckRow struct{ id, reqID, startedStr string }
	var stuck []stuckRow
	for rows.Next() {
		var r stuckRow
		if err := rows.Scan(&r.id, &r.reqID, &r.startedStr); err != nil {
			continue
		}
		stuck = append(stuck, r)
	}
	_ = rows.Close() // 关键：释放连接后再写库
	for _, r := range stuck {
		started, err := time.Parse(time.RFC3339Nano, r.startedStr)
		if err != nil {
			continue
		}
		s.healStuck(r.id, r.reqID, started)
	}
}

// healStuck 卡 running 超时收尾：route_requests 侧已 failed（可反查）则标 failed
// 并带上已捕获的 http_status/error_body（写入 response_json），否则标
// stream_interrupted。复用 route-log 的 SelfHeal 阈值（config.RouteLogSelfHealTimeout）。
func (s *Service) healStuck(id, requestID string, started time.Time) {
	if time.Since(started) < config.RouteLogSelfHealTimeout {
		return
	}
	result := "stream_interrupted"
	status := 0
	errBody := ""
	if s.loadout != nil {
		// per-attempt 优先：失败 attempt 的错误信息在 route_attempts 表（429 等真实上游
		// 错误体），按 request_log_id 精确反查本行对应的 attempt。route_requests 只存
		// 外层最终结果——per-attempt 语义下外层 success 时它为空，反查 attempt 才能
		// 拿到失败详情（原实现只查 route_requests 会把失败 attempt 误标 stream_interrupted）。
		// 【P0 修复】必须限定 result='failed'：流式断开（无 [DONE]）场景 attempt 行可能是
		// success+200，若只看 error_body/status_code 会把成功 attempt 误标 failed。
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
	// error_body 还原进 response_json（普通模型失败无事件，这是唯一拿到错误详情的途径）
	respJSON := ""
	if errBody != "" {
		snap := responseSnapshot{StatusCode: status, Body: redactBody(errBody, s.redactEnabled())}
		if b, err := json.Marshal(snap); err == nil {
			respJSON = string(b)
		}
	}
	now := time.Now().UTC()
	// bytes 与 response_json 用同一个 CASE 条件：只有在真的写入了响应体时才重算，
	// 否则保持原值（否则会把已有的响应体字节数抹掉）。
	_, _ = s.reqDB.Exec(`UPDATE request_logs SET finished_at = ?, duration_ms = ?, result = ?, http_status = CASE WHEN ? = 0 THEN http_status ELSE ? END, response_json = CASE WHEN ? = '' THEN response_json ELSE ? END, bytes = CASE WHEN ? = '' THEN bytes ELSE length(request_json) + length(?) + ? END WHERE id = ?`,
		now.Format(time.RFC3339Nano), now.Sub(started).Milliseconds(), result, status, status, respJSON, respJSON, respJSON, respJSON, rowFixedOverhead, id)
}

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

// buildRequestSnapshot 序列化请求快照（脱敏 + base64 图片占位）。
func buildRequestSnapshot(pipe *modelgateway.ProxyPipeline, model string, redact bool) requestSnapshot {
	return requestSnapshot{
		Method:  pipe.Request.Method,
		Path:    pipe.Request.Path,
		Query:   pipe.Request.Query,
		Headers: redactHeaders(pipe.Request.Header, redact),
		Body:    redactBody(string(pipe.Request.Body), redact),
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

// redactBody 对 body 文本做脱敏：sk- 密钥打码、base64 data URI 转占位标记。
func redactBody(body string, enabled bool) string {
	if !enabled {
		return body
	}
	// sk- 后跟 4+ 位字母数字才算密钥（避免误伤普通文本，且已打码的 sk-*** 不重复打码）
	body = skSecretRegex.ReplaceAllString(body, "sk-***")
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
