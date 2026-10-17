package requestcapture

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"loadout/core/db"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	requestlog "loadout/plugins/request-log"
	"loadout/plugins/types"
)

// capabilityName 能力路由表中请求捕获能力的固定名称。
const capabilityName = "request_capture"

// captureExt 捕获文件扩展名；文件名前缀 = request_id（与 request-log 列表的
// "请求 ID" 一致，可以在两个页面之间互相检索）。
const captureExt = ".cap.json.gz"

// metaExt 列表用的轻量元数据文件扩展名。列表要显示模型/时间/大小，逐个解压完整
// 捕获文件太慢（body 里可能有几十 MB 的 base64），因此抓包时顺手写一份小 meta；
// meta 丢失时列表回退去读捕获文件本身。
const metaExt = ".cap.meta.json"

// Service 请求捕获适配器：命中能力路由的请求在进入中转最前端把完整原始数据
// 打包落盘（调试抓包用途）。
type Service struct {
	st   *store.Store
	lg   logger
	repo *db.Repository
	dir  string

	mu            sync.Mutex
	lastRetention time.Time
	maxAgeDays    int
	maxSizeMB     int
}

// logger 最小日志接口（便于测试注入）。
type logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// NewService 创建服务；dir 为捕获文件输出目录（空 = 不落盘，仅注册路由语义）。
func NewService(st *store.Store, lg logger, dir string) *Service {
	return &Service{st: st, lg: lg, dir: dir}
}

func (s *Service) SetRepository(repo *db.Repository) { s.repo = repo }

// SetRetention 设置保留策略（0 = 不限）。由装配层从环境变量读取。
func (s *Service) SetRetention(maxAgeDays, maxSizeMB int) {
	s.maxAgeDays = maxAgeDays
	s.maxSizeMB = maxSizeMB
}

// captureEnvelope 打包文件内容：请求方向的最原始数据（进入中转时、任何插件处理之前）。
// 刻意不做任何脱敏：Authorization 原文、图片 base64 全部保留，供逐字节重放排障。
type captureEnvelope struct {
	CapturedAt string              `json:"captured_at"`
	RequestID  string              `json:"request_id"`
	Method     string              `json:"method"`
	Path       string              `json:"path"`
	Query      string              `json:"query"`
	Model      string              `json:"model"`
	Stream     bool                `json:"stream"`
	Headers    map[string][]string `json:"headers"`
	Body       string              `json:"body"` // 原始字节，UTF-8 不合法时存 hex
}

// captureMeta 列表用的轻量元数据（不含 headers/body）。
type captureMeta struct {
	RequestID  string `json:"request_id"`
	Model      string `json:"model"`
	CapturedAt string `json:"captured_at"`
	Method     string `json:"method"`
	Path       string `json:"path"`
	Stream     bool   `json:"stream"`
	Size       int64  `json:"size"`
}

// captureItem 列表行。
type captureItem struct {
	ID         string `json:"id"`
	RequestID  string `json:"request_id"`
	Model      string `json:"model"`
	Size       int64  `json:"size"`
	CapturedAt string `json:"captured_at"`
	Method     string `json:"method"`
	Path       string `json:"path"`
	Stream     bool   `json:"stream"`
}

// captureDetail 详情：列表行 + 完整原始报文。
type captureDetail struct {
	captureItem
	Query   string              `json:"query"`
	Headers map[string][]string `json:"headers"`
	Body    string              `json:"body"`
}

// capturePage 列表响应（附带体积与当前保留策略，供页面顶部展示）。
type capturePage struct {
	Items      []captureItem `json:"items"`
	Total      int           `json:"total"`
	Size       int64         `json:"size"`
	MaxAgeDays int           `json:"max_age_days"`
	MaxSizeMB  int           `json:"max_size_mb"`
}

// loadRoutes 读能力路由表；读失败返回 nil（fail-open，按透传处理）。
func (s *Service) loadRoutes() []types.CapabilityRoute {
	if s.repo != nil {
		routes, err := s.repo.ListCapabilityRoutes(context.Background())
		if err != nil {
			s.lg.Warn("request-capture: 读能力路由表失败，按透传处理", "err", err)
			return nil
		}
		return routes
	}
	var routes []types.CapabilityRoute
	if err := s.st.Read(types.FileCapabilityRoutes, &routes); err != nil {
		return nil // 不存在/读失败：透传
	}
	return routes
}

// shouldCapture 判定本次请求是否要抓包。
//
// 抓包发生在 proxy:before-upstream——请求刚进中转、渠道还没解析，metadata 里没有
// 任何渠道信息。因此对「渠道约束」采取宽松策略：
//   - 能拿到渠道上下文（IDs/BaseURLs 非空）时严格按 MatchChannelScopeEx 匹配；
//   - 拿不到时忽略渠道约束，只按模型匹配。
//
// 宁可多抓一份文件，也绝不因为"渠道未知"漏掉用户要排查的请求——漏抓会让整个
// 调试链路失去证据。native 路由仍然优先：命中 native 视为精确豁免，不抓。
func (s *Service) shouldCapture(pipe *modelgateway.ProxyPipeline) bool {
	if pipe == nil || pipe.Request == nil {
		return false
	}
	virtualModel := types.VirtualModelFromMetadata(pipe.Metadata)
	scope := types.ChannelScopeFromMetadata(pipe.Metadata, s.requestChannelBaseURLs)
	scopeKnown := len(scope.IDs) > 0 || len(scope.BaseURLs) > 0

	// 只看本能力（request_capture）自己的路由：其它能力的 native 路由与抓包无关，
	// 不能因为它而整体跳过抓包（曾经的 bug：vision(native) 通配 * 会把抓包一并豁免掉）。
	hitProxy := false
	for _, route := range s.loadRoutes() {
		if route.Capability != capabilityName {
			continue
		}
		if !types.MatchModelsEx(route.Models, pipe.Request.Model, virtualModel) {
			continue
		}
		if scopeKnown && !types.MatchChannelScopeEx(route.ChannelIDs, route.ChannelBaseURLs, scope) {
			continue
		}
		if route.Route != types.RouteProxy {
			return false // native 豁免优先
		}
		hitProxy = true
	}
	return hitProxy
}

// requestChannelBaseURLs 反查渠道 base_url（与 field-filter 同款语义）。
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

// HandleProxyBeforeUpstream 入口 hook（proxy:before-upstream，请求到达中转最早阶段、
// 任何安检/剥除之前）：命中 request_capture proxy 路由时把完整原始请求打包落盘。
//
// 【铁律】永不 return error、永不改 body —— 只读快照，出错仅记日志（best-effort）。
// 文件名 = pipe.RequestID（与 request-log 列表的“请求 ID”一致，可互相检索）。
func (s *Service) HandleProxyBeforeUpstream(payload any) (any, error) {
	pipe, ok := payload.(*modelgateway.ProxyPipeline)
	if !ok || pipe == nil || pipe.Request == nil || s.dir == "" {
		return payload, nil
	}
	// 子请求（vision_v2 识别、MCP 图片识别等）：那是插件自己发的内部请求，
	// 不是用户流量，也不该占用调试目录。
	//
	// 【只响应 __sub_request】本插件是只读旁路，不是安检：__sub_request_skip_security
	// 是「跳过安检类处理」的标记，语义上不该由抓包响应（AGENTS.md 红线：不是安检
	// 就不要响应这个标记）。ForwardSubRequest 会同时写这两个标记，只看 __sub_request
	// 已足够跳过全部子请求，行为不变。
	if pipe.Metadata != nil {
		if v, _ := pipe.Metadata["__sub_request"].(bool); v {
			return payload, nil
		}
	}
	if !s.shouldCapture(pipe) {
		return payload, nil
	}

	env := captureEnvelope{
		CapturedAt: time.Now().UTC().Format(time.RFC3339Nano),
		RequestID:  pipe.RequestID,
		Method:     pipe.Request.Method,
		Path:       pipe.Request.Path,
		Query:      pipe.Request.Query,
		Model:      pipe.Request.Model,
		Stream:     pipe.Request.Stream,
		Headers:    pipe.Request.Header,
		Body:       bodyToString(pipe.Request.Body),
	}
	if err := s.writeCapture(pipe.RequestID, env); err != nil {
		s.lg.Warn("request-capture: 写捕获文件失败", "request_id", pipe.RequestID, "err", err)
		return payload, nil
	}
	s.lg.Info("request-capture: 已捕获原始请求",
		"request_id", pipe.RequestID, "model", pipe.Request.Model, "path", pipe.Request.Path)
	s.maybeApplyRetention()
	return payload, nil
}

// bodyToString 原始 body 转 UTF-8 字符串；非法字节以 hex 保留（不丢信息）。
//
// safeCaptureID 校验并归一化捕获文件名用的 request_id。
//
// request_id 直接来自客户端请求头（X-Request-Id），会被原样复用为文件名。
// 不校验就等于把「往任意路径写文件」的能力交给外部输入——客户端传
// "../../xxx" 就能写到捕获目录之外。这里只放行字母数字/下划线/短横线
// 且长度合理（1~128）的 id，其余一律拒绝（调用方 fail-open：跳过这次抓包）。
func safeCaptureID(raw string) (string, bool) {
	id := strings.TrimSpace(raw)
	if id == "" || len(id) > 128 {
		return id, false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return id, false
		}
	}
	return id, true
}

func bodyToString(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	if utf8.Valid(body) {
		return string(body)
	}
	return fmt.Sprintf("hex:%x", body)
}

// writeCapture gzip 压缩写盘（临时文件 + rename 原子），随后写一份列表用 meta。
func (s *Service) writeCapture(requestID string, env captureEnvelope) error {
	requestID, ok := safeCaptureID(requestID)
	if !ok {
		return fmt.Errorf("request-capture: 非法 request_id %q", requestID)
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("create capture dir: %w", err)
	}
	final := filepath.Join(s.dir, requestID+captureExt)
	tmp := final + ".tmp"

	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	data, err := json.Marshal(env)
	if err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("marshal envelope: %w", err)
	}
	gz := gzip.NewWriter(f)
	if _, err := gz.Write(data); err != nil {
		gz.Close()
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("gzip write: %w", err)
	}
	if err := gz.Close(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("gzip close: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}

	// meta 写失败不影响抓包本身：列表会回退去读捕获文件。
	var size int64
	if info, statErr := os.Stat(final); statErr == nil {
		size = info.Size()
	}
	meta := captureMeta{
		RequestID:  env.RequestID,
		Model:      env.Model,
		CapturedAt: env.CapturedAt,
		Method:     env.Method,
		Path:       env.Path,
		Stream:     env.Stream,
		Size:       size,
	}
	if mb, err := json.Marshal(meta); err == nil {
		_ = os.WriteFile(filepath.Join(s.dir, requestID+metaExt), mb, 0o644)
	}
	return nil
}

// captureFiles 列出目录里的捕获文件 id（文件名去后缀）。
func (s *Service) captureFiles() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, captureExt) {
			continue
		}
		ids = append(ids, strings.TrimSuffix(name, captureExt))
	}
	return ids, nil
}

// itemFor 读取单个捕获的列表行；meta 缺失或损坏时回退读取捕获文件本身。
func (s *Service) itemFor(id string) (captureItem, bool) {
	if _, ok := safeCaptureID(id); !ok {
		return captureItem{}, false // 非法的 id 直接当作不存在（防路径穿越）
	}
	path := filepath.Join(s.dir, id+captureExt)
	info, err := os.Stat(path)
	if err != nil {
		return captureItem{}, false
	}
	item := captureItem{
		ID:         id,
		RequestID:  id,
		Size:       info.Size(),
		CapturedAt: info.ModTime().UTC().Format(time.RFC3339Nano),
	}
	if raw, err := os.ReadFile(filepath.Join(s.dir, id+metaExt)); err == nil {
		var meta captureMeta
		if json.Unmarshal(raw, &meta) == nil {
			if meta.RequestID != "" {
				item.RequestID = meta.RequestID
			}
			item.Model = meta.Model
			item.Method = meta.Method
			item.Path = meta.Path
			item.Stream = meta.Stream
			if meta.CapturedAt != "" {
				item.CapturedAt = meta.CapturedAt
			}
			if meta.Size > 0 {
				item.Size = meta.Size
			}
			return item, true
		}
	}
	// 回退：解压读 envelope（只在 meta 缺失时发生）。
	if env, err := s.readEnvelope(id); err == nil {
		item.RequestID = env.RequestID
		item.Model = env.Model
		item.Method = env.Method
		item.Path = env.Path
		item.Stream = env.Stream
		if env.CapturedAt != "" {
			item.CapturedAt = env.CapturedAt
		}
	}
	return item, true
}

// readEnvelope 解压并解析一份捕获文件。
func (s *Service) readEnvelope(id string) (captureEnvelope, error) {
	if _, ok := safeCaptureID(id); !ok {
		return captureEnvelope{}, os.ErrNotExist
	}
	f, err := os.Open(filepath.Join(s.dir, id+captureExt))
	if err != nil {
		return captureEnvelope{}, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return captureEnvelope{}, err
	}
	defer gz.Close()
	var env captureEnvelope
	if err := json.NewDecoder(gz).Decode(&env); err != nil {
		return captureEnvelope{}, err
	}
	return env, nil
}

// listItems 扫描目录并按抓包时间倒序返回全部行（含总体积）。
func (s *Service) listItems() ([]captureItem, int64) {
	ids, err := s.captureFiles()
	if err != nil {
		s.lg.Warn("request-capture: 列出捕获文件失败", "dir", s.dir, "err", err)
		return nil, 0
	}
	items := make([]captureItem, 0, len(ids))
	var total int64
	for _, id := range ids {
		item, ok := s.itemFor(id)
		if !ok {
			continue
		}
		total += item.Size
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CapturedAt > items[j].CapturedAt })
	return items, total
}

// CaptureFilter 抓包列表的筛选条件（全部为空 = 不过滤）。
//
// 字段与「转发日志」页对齐：模型 / 渠道 / 结果（这里对应抓包时的请求形态）/
// 时间范围；额外多一个 Request ID（用户排查时最先拿到的就是它）。
type CaptureFilter struct {
	RequestID string // 请求 ID（与抓包文件名一致；子串匹配）
	Model     string // 请求模型（子串匹配）
	Path      string // 路径（如 chat/completions，子串匹配）
	From      string // 起始时间（RFC3339；含）
	To        string // 结束时间（RFC3339；含）
	Stream    *bool  // 是否流式
}

// List 分页 + 过滤。page 从 1 开始。
func (s *Service) List(filter CaptureFilter, page, pageSize int) capturePage {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 500 {
		pageSize = 20
	}
	items, total := s.listItems()
	requestID := strings.ToLower(strings.TrimSpace(filter.RequestID))
	model := strings.ToLower(strings.TrimSpace(filter.Model))
	path := strings.ToLower(strings.TrimSpace(filter.Path))
	from := parseFilterTime(filter.From)
	to := parseFilterTime(filter.To)
	filtered := make([]captureItem, 0, len(items))
	for _, it := range items {
		if requestID != "" && !strings.Contains(strings.ToLower(it.RequestID), requestID) &&
			!strings.Contains(strings.ToLower(it.ID), requestID) {
			continue
		}
		if model != "" && !strings.Contains(strings.ToLower(it.Model), model) {
			continue
		}
		if path != "" && !strings.Contains(strings.ToLower(it.Path), path) {
			continue
		}
		if filter.Stream != nil && it.Stream != *filter.Stream {
			continue
		}
		if from != nil || to != nil {
			ts, err := time.Parse(time.RFC3339Nano, it.CapturedAt)
			if err != nil {
				continue
			}
			if from != nil && ts.Before(*from) {
				continue
			}
			if to != nil && ts.After(*to) {
				continue
			}
		}
		filtered = append(filtered, it)
	}
	start := (page - 1) * pageSize
	if start > len(filtered) {
		start = len(filtered)
	}
	end := start + pageSize
	if end > len(filtered) {
		end = len(filtered)
	}
	return capturePage{
		Items:      filtered[start:end],
		Total:      len(filtered),
		Size:       total,
		MaxAgeDays: s.maxAgeDays,
		MaxSizeMB:  s.maxSizeMB,
	}
}

// parseFilterTime 解析筛选用的时间参数。
//
// 前端 date-time 输入可能给出带时区的 RFC3339，也可能是无时区的 "2006-01-02T15:04"
// （浏览器 datetime-local 的原生格式）。无时区按本地时区解释——用户在界面上填的
// 就是自己看到的墙钟时间。解析失败返回 nil（该条件不生效，绝不因此漏掉记录）。
func parseFilterTime(raw string) *time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02"} {
		if ts, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return &ts
		}
	}
	return nil
}

// Detail 读取一条捕获的完整原始报文。
func (s *Service) Detail(id string) (captureDetail, error) {
	item, ok := s.itemFor(id)
	if !ok {
		return captureDetail{}, os.ErrNotExist
	}
	env, err := s.readEnvelope(id)
	if err != nil {
		return captureDetail{}, err
	}
	return captureDetail{
		captureItem: item,
		Query:       env.Query,
		Headers:     env.Headers,
		Body:        env.Body,
	}, nil
}

// ReplayScript 把捕获到的原始请求体写成一个可直接运行的 Node.js 复现脚本。
//
// 复用 request-log 的脚本生成器（同一个函数、同一种脚本外形），只是路径换成
// 抓包里记录的原始 path；脚本落到 <data>/replay-scripts/<id>.mjs，重复生成覆盖。
// 注意：捕获文件含 Authorization 原文，脚本本身只带请求体、不带密钥，安全边界不变。
func (s *Service) ReplayScript(id string) (string, int, error) {
	env, err := s.readEnvelope(id)
	if err != nil {
		return "", 0, err
	}
	if env.Body == "" {
		return "", 0, fmt.Errorf("该捕获没有请求体（可能是空 body 请求）")
	}
	// body 可能是 hex: 兜底格式（非 UTF-8 原始字节），这种无法作为 JSON 重放。
	if strings.HasPrefix(env.Body, "hex:") {
		return "", 0, fmt.Errorf("请求体不是 UTF-8 文本，无法生成 JSON 复现脚本")
	}
	return requestlog.WriteReplayScriptFile(
		filepath.Dir(s.dir), // <data>/request-captures 的上一级 = 数据目录
		id,
		[]byte(env.Body),
		requestlog.ReplayScriptOptions{
			RequestID: env.RequestID,
			Path:      env.Path,
		},
	)
}

// Delete 删除一条捕获（含 meta）。返回是否真的删到了文件。
func (s *Service) Delete(id string) bool {
	if _, ok := safeCaptureID(id); !ok {
		return false
	}
	removed := false
	for _, name := range []string{id + captureExt, id + metaExt, id + captureExt + ".tmp"} {
		if err := os.Remove(filepath.Join(s.dir, name)); err == nil {
			removed = true
		}
	}
	return removed
}

// Clear 删除全部捕获文件，返回删除条数。
func (s *Service) Clear() int {
	ids, err := s.captureFiles()
	if err != nil {
		return 0
	}
	removed := 0
	for _, id := range ids {
		if s.Delete(id) {
			removed++
		}
	}
	// 清掉可能残留的 meta 孤儿文件。
	if entries, err := os.ReadDir(s.dir); err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), metaExt) {
				_ = os.Remove(filepath.Join(s.dir, e.Name()))
			}
		}
	}
	return removed
}

// ApplyRetention 按「时间 / 容量」FIFO 清理最旧的捕获文件。
// 两个维度都是 0 时直接返回（默认不删，调试证据默认保留）。
func (s *Service) ApplyRetention() int {
	if s.maxAgeDays <= 0 && s.maxSizeMB <= 0 {
		return 0
	}
	items, size := s.listItems()
	if len(items) == 0 {
		return 0
	}
	// items 已是「新 → 旧」，倒过来从最旧开始删。
	limit := int64(s.maxSizeMB) * 1024 * 1024
	removed := 0
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		expired := false
		if s.maxAgeDays > 0 {
			if ts, err := time.Parse(time.RFC3339Nano, it.CapturedAt); err == nil {
				expired = time.Since(ts) > time.Duration(s.maxAgeDays)*24*time.Hour
			}
		}
		overSize := s.maxSizeMB > 0 && size > limit
		if !expired && !overSize {
			continue
		}
		if s.Delete(it.ID) {
			removed++
			size -= it.Size
		}
	}
	return removed
}

// maybeApplyRetention 写盘后顺带清理，最多 5 分钟一次（避免每个请求都扫目录）。
func (s *Service) maybeApplyRetention() {
	s.mu.Lock()
	due := time.Since(s.lastRetention) > 5*time.Minute
	if due {
		s.lastRetention = time.Now()
	}
	s.mu.Unlock()
	if due {
		s.ApplyRetention()
	}
}

// ---- HTTP handlers（AuthSession 由框架自动挂认证中间件） ----

// handleList GET /api/request-captures
func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	pageSize, _ := strconv.Atoi(q.Get("pageSize"))
	filter := CaptureFilter{
		RequestID: q.Get("request_id"),
		Model:     q.Get("model"),
		Path:      q.Get("path"),
		From:      q.Get("from"),
		To:        q.Get("to"),
	}
	// stream 用三态：缺省 = 不限；"1"/"true" = 只看流式；"0"/"false" = 只看非流式。
	if raw := q.Get("stream"); raw != "" {
		v := raw == "1" || raw == "true"
		filter.Stream = &v
	}
	writeJSON(w, http.StatusOK, s.List(filter, page, pageSize))
}

// handleDetail GET /api/request-captures/{id}
func (s *Service) handleDetail(w http.ResponseWriter, r *http.Request) {
	detail, err := s.Detail(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": map[string]any{"message": "捕获文件不存在或已损坏"},
		})
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// handleDelete DELETE /api/request-captures/{id}
func (s *Service) handleDelete(w http.ResponseWriter, r *http.Request) {
	if !s.Delete(r.PathValue("id")) {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": map[string]any{"message": "捕获文件不存在"},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleClear DELETE /api/request-captures
func (s *Service) handleClear(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "affected": s.Clear()})
}

// handleReplayScript POST /api/request-captures/{id}/replay-script
//
// 详情页「填入测试」按钮调用：生成 Node 复现脚本并返回绝对路径。
// Auth 为 session（与详情一致）：捕获文件里有 Authorization 原文，绝不能像
// request-log 那样 public 暴露。
func (s *Service) handleReplayScript(w http.ResponseWriter, r *http.Request) {
	path, bytes, err := s.ReplayScript(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{"message": err.Error()},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": path, "bytes": bytes})
}

// handleDownload GET /api/request-captures/{id}/download
//
// 直接把原始的 .cap.json.gz 交给浏览器，用于跨机器保留证据。文件本身是 gzip，
// 这里不重新编码（Content-Type 用 application/gzip）。
func (s *Service) handleDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := safeCaptureID(id); !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{"message": "非法捕获 ID"},
		})
		return
	}
	path := filepath.Join(s.dir, id+captureExt)
	if _, err := os.Stat(path); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": map[string]any{"message": "捕获文件不存在"},
		})
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+id+captureExt+"\"")
	http.ServeFile(w, r, path)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

