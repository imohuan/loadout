package modelgateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"loadout/core/db"
	"loadout/core/plugin"
	"loadout/core/store"
	"loadout/plugins/contracts"
	gatewaykeys "loadout/plugins/gateway-keys"
	"loadout/plugins/types"
)

// Service 模型转发核心：透明代理（/v1/{path...} 原样转发）+ /v1/models 聚合。
type Service struct {
	st       *store.Store
	lg       *slog.Logger
	ctx      plugin.Context
	database *sql.DB
	routing  *db.Repository
	health   contracts.ModelHealth
	routeLog contracts.RouteLog
}

// NewService 创建模型转发服务。
func NewService(st *store.Store, lg *slog.Logger, ctx plugin.Context) *Service {
	return &Service{st: st, lg: lg, ctx: ctx}
}

func (s *Service) SetRoutingServices(database *sql.DB, health contracts.ModelHealth, routeLog contracts.RouteLog) {
	s.database = database
	s.health = health
	s.routeLog = routeLog
	if database != nil {
		s.routing, _ = db.NewRepository(database)
	}
}

// RouteCapability 查能力路由：给定 model + capability + 请求渠道，返回命中条目；
// channelID 为请求当前渠道（空 = 未知，仅全渠道/通配路由命中）。
// 未命中返回 nil（视为 native 透传）。SQLite 优先，fallback capability_routes.json。
func (s *Service) RouteCapability(model, capability, channelID string) (*types.CapabilityRoute, error) {
	requestBaseURL := s.requestChannelBaseURL(channelID)
	if s.routing != nil {
		routes, err := s.routing.ListCapabilityRoutes(context.Background())
		if err == nil {
			for i := range routes {
				if routes[i].Capability == capability &&
					types.MatchModels(routes[i].Models, model) &&
					types.MatchChannelScope(routes[i].ChannelIDs, routes[i].ChannelBaseURLs, channelID, requestBaseURL) {
					return &routes[i], nil
				}
			}
			return nil, nil
		}
		s.lg.Warn("modelgateway: 从 SQLite 读能力路由失败，回退 JSON", "err", err)
	}
	var routes []types.CapabilityRoute
	if err := s.st.Read(types.FileCapabilityRoutes, &routes); err != nil {
		if errors.Is(err, store.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("modelgateway: 读取能力路由表失败: %w", err)
	}
	for i := range routes {
		if routes[i].Capability == capability &&
			types.MatchModels(routes[i].Models, model) &&
			types.MatchChannelScope(routes[i].ChannelIDs, routes[i].ChannelBaseURLs, channelID, requestBaseURL) {
			return &routes[i], nil
		}
	}
	return nil, nil
}

// requestChannelBaseURL 取请求渠道的 base_url（用于渠道级匹配），无渠道或查不到返回空串。
func (s *Service) requestChannelBaseURL(channelID string) string {
	if channelID == "" || s.routing == nil {
		return ""
	}
	channels, err := s.routing.ListChannels(context.Background())
	if err != nil {
		return ""
	}
	for _, ch := range channels {
		if ch.ID == channelID {
			return ch.BaseURL
		}
	}
	return ""
}

// ResolveChannelsForModel 返回支持 model 的候选渠道（按 channels.json 数组顺序）：
// 已知支持（Models 含 model）的在前，未知（Models 空）的在后，明确不支持（Models 非空且不含）的排除。
// 返回的 APIKey 已解密；解密失败或未启用的渠道会被跳过（坏渠道不参与路由）。
func ResolveChannelsForModel(st *store.Store, model string) ([]ResolvedChannel, error) {
	var channels []types.Channel
	if err := st.Read(types.FileChannels, &channels); err != nil {
		if errors.Is(err, store.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("modelgateway: 读取渠道表失败: %w", err)
	}
	var known, unknown []ResolvedChannel
	for _, ch := range channels {
		if !ch.Enabled {
			continue
		}
		key := ""
		if ch.APIKeyCipher != "" {
			k, err := st.Decrypt(ch.APIKeyCipher)
			if err != nil {
				continue // 解密失败跳过该渠道
			}
			key = k
		}
		rc := ResolvedChannel{ID: ch.ID, Name: ch.Name, ChannelName: ch.ChannelName, BaseURL: ch.BaseURL, APIKey: key}
		if len(ch.Models) == 0 {
			unknown = append(unknown, rc)
		} else if containsExact(ch.Models, model) {
			known = append(known, rc)
		}
	}
	return append(known, unknown...), nil
}

// containsExact 判断列表是否精确包含 s（渠道 Models 来自 /v1/models 探测或手动维护）。
func containsExact(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// splitV2Model 按最长 ChannelName 前缀拆分 model，用于 v2 前缀路由。
//
// 规则：遍历 isChannel 返回 true 的渠道名，取「model 以 {name}/ 开头」且 name 最长者。
//   - 命中 → hint=渠道名, realModel=去掉前缀后的剩余部分, ok=true
//   - 未命中 → ok=false（无前缀、前缀非渠道名，或渠道名含斜杠但未完整匹配）
//
// 最长匹配处理渠道名前缀包含问题（渠道 "a" 与 "ab" 并存时，"ab/xxx" 命中 "ab"）；
// 渠道名本身含 /（如 "team/workbuddy"）也天然支持。isChannel 由调用方注入
// （复用一次渠道表扫描），保持函数可单测。
func splitV2Model(model string, isChannel func(string) bool) (hint, realModel string, ok bool) {
	if model == "" {
		return "", "", false
	}
	var bestName string
	for _, slash := range allSlashIndexes(model) {
		name := model[:slash]
		if name == "" {
			continue // 空渠道名不参与匹配
		}
		if isChannel(name) && len(name) > len(bestName) {
			bestName = name
		}
	}
	if bestName == "" {
		return "", "", false
	}
	return bestName, model[len(bestName)+1:], true
}

// allSlashIndexes 返回 s 中所有 '/' 的位置（升序）。
func allSlashIndexes(s string) []int {
	var idx []int
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			idx = append(idx, i)
		}
	}
	return idx
}

// channelModelEntry 渠道模型收集条目：v1/v2 /models 共用。
type channelModelEntry struct {
	ChannelID   string
	ChannelName string // 空 = 未配置渠道名（v2 不输出该模型或输出裸名）
	Model       string
}

// collectChannelModels 聚合渠道模型（探测 + 手动配置）：SQLite 优先，
// routing 为 nil 时回退 JSON 渠道表。返回按渠道顺序的扁平条目列表。
func (s *Service) collectChannelModels(ctx context.Context) []channelModelEntry {
	var out []channelModelEntry
	if s.routing != nil {
		channels, err := s.routing.ListChannels(ctx)
		if err != nil {
			s.lg.Warn("读取渠道失败", "err", err)
			return out
		}
		for _, ch := range channels {
			if !ch.ManualEnabled {
				continue
			}
			for _, m := range ch.Models {
				if !m.Enabled {
					continue
				}
				out = append(out, channelModelEntry{ChannelID: ch.ID, ChannelName: ch.ChannelName, Model: m.Model})
			}
		}
		return out
	}
	var channels []types.Channel
	if err := s.st.Read(types.FileChannels, &channels); err != nil {
		return out
	}
	for _, ch := range channels {
		if !ch.Enabled {
			continue
		}
		for _, m := range ch.Models {
			out = append(out, channelModelEntry{ChannelID: ch.ID, ChannelName: ch.ChannelName, Model: m})
		}
	}
	return out
}

// HandleModels 处理 GET /v1/models：聚合渠道模型（探测 + 手动配置）与虚拟（聚合）模型名，
// 减去 model-status 配置的禁用模型，再按 API key 白名单过滤后去重返回。
//
// 列表构成（用户定稿）：远程探测模型 ∪ 手动配置模型 ∪ 虚拟模型 − model_states 禁用 − key 白名单。
func (s *Service) HandleModels(w http.ResponseWriter, r *http.Request) {
	// model → 支持它的启用渠道集合（同名模型跨渠道：任一渠道可用即保留）。
	modelChannels := map[string]map[string]bool{}
	var virtualModels []string
	seen := map[string]bool{}

	// 1. 渠道模型（探测 + 手动，channel_models 注册表）。
	entries := s.collectChannelModels(r.Context())
	for _, e := range entries {
		if modelChannels[e.Model] == nil {
			modelChannels[e.Model] = map[string]bool{}
		}
		modelChannels[e.Model][e.ChannelID] = true
	}

	// 2. 虚拟（聚合）模型名：收集但先不标记 seen，追加时统一去重
	//（渠道模型过滤与 key 白名单对虚拟模型同样适用，先收集后合并）。
	for _, name := range s.aggregateNames(r.Context()) {
		virtualModels = append(virtualModels, name)
	}

	// 3. 减去 model-status 配置的禁用模型（model_states.manual_enabled=false）。
	// 同名模型在所有渠道都被禁用才从列表移除；至少一个渠道可用则保留。
	disabled := s.disabledModelSet(r.Context())
	var models []string
	for model, channels := range modelChannels {
		available := false
		for cid := range channels {
			if !disabled[cid+"\x00"+model] {
				available = true
				break
			}
		}
		if available && !seen[model] {
			seen[model] = true
			models = append(models, model)
		}
	}
	for _, name := range virtualModels {
		if !seen[name] {
			seen[name] = true
			models = append(models, name)
		}
	}

	// 4. key 白名单统一过滤（真实 + 虚拟模型同等受约束）。
	if key, ok := gatewaykeys.APIKeyFromContext(r.Context()); ok {
		var allowed []string
		for _, m := range models {
			if gatewaykeys.AllowedModel(key.Models, m) {
				allowed = append(allowed, m)
			}
		}
		models = allowed
	}

	data := make([]map[string]any, 0, len(models))
	for _, m := range models {
		data = append(data, map[string]any{"id": m, "object": "model"})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

// HandleModelsV2 处理 GET /v2/models：语义与 v1 一致，但渠道模型输出
// `{ChannelName}/{model}`（客户端可显式选择渠道），虚拟模型原名。
//
// 与 v1 差异：
//  1. 去重维度从「model」改为「ChannelName/model」——同名模型跨渠道全部保留；
//  2. 可用性聚合到 ChannelName 级：同组至少一个 Key 可用才保留（v1 是 Key 级）；
//  3. key 白名单双形态命中：裸名或带前缀名任一命中即放行。
func (s *Service) HandleModelsV2(w http.ResponseWriter, r *http.Request) {
	// displayName → 支持它的启用 Key 集合（v2 维度：ChannelName/model）。
	modelChannels := map[string]map[string]bool{}
	var virtualModels []string
	seen := map[string]bool{}

	entries := s.collectChannelModels(r.Context())
	for _, e := range entries {
		// ChannelName 为空/纯空白：v2 无前缀可显式定位，跳过该模型
		//（不输出 `/gpt-4o` 这种脏名；v1 仍可正常使用裸名）。
		if strings.TrimSpace(e.ChannelName) == "" {
			continue
		}
		display := e.ChannelName + "/" + e.Model
		if modelChannels[display] == nil {
			modelChannels[display] = map[string]bool{}
		}
		modelChannels[display][e.ChannelID] = true
	}

	for _, name := range s.aggregateNames(r.Context()) {
		virtualModels = append(virtualModels, name)
	}

	// 禁用过滤：同组至少一个 Key 的该模型可用才保留。
	disabled := s.disabledModelSet(r.Context())
	// channelID → ChannelName 映射（可用性聚合到组级用）。
	channelNameByID := map[string]string{}
	for _, e := range entries {
		if _, ok := channelNameByID[e.ChannelID]; !ok {
			channelNameByID[e.ChannelID] = e.ChannelName
		}
	}
	var models []string
	for display, channels := range modelChannels {
		available := false
		for cid := range channels {
			if !disabled[cid+"\x00"+strings.TrimPrefix(display, channelNameByID[cid]+"/")] {
				available = true
				break
			}
		}
		if available && !seen[display] {
			seen[display] = true
			models = append(models, display)
		}
	}
	for _, name := range virtualModels {
		if !seen[name] {
			seen[name] = true
			models = append(models, name)
		}
	}

	// 白名单双形态：裸名或带前缀名任一命中即放行。
	// 拆前缀用 LastIndexByte（最后一个 /）：ChannelName 本身可能含 /（如
	// team/workbuddy），"team/workbuddy/gpt-4o" 的真实模型名是 gpt-4o。
	if key, ok := gatewaykeys.APIKeyFromContext(r.Context()); ok {
		var allowed []string
		for _, m := range models {
			real := m
			if i := strings.LastIndexByte(m, '/'); i > 0 {
				real = m[i+1:]
			}
			if gatewaykeys.AllowedModel(key.Models, m) || gatewaykeys.AllowedModel(key.Models, real) {
				allowed = append(allowed, m)
			}
		}
		models = allowed
	}

	data := make([]map[string]any, 0, len(models))
	for _, m := range models {
		data = append(data, map[string]any{"id": m, "object": "model"})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

// disabledModelSet 收集 model-status 配置的禁用模型：map["channelID\x00model"]bool。
// model-health 未装配（s.health nil）时返回空集（不启用禁用过滤）。
func (s *Service) disabledModelSet(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	if s.health == nil {
		return out
	}
	statuses, err := s.health.List(ctx)
	if err != nil {
		s.lg.Warn("读取模型状态失败，跳过禁用过滤", "err", err)
		return out
	}
	for _, cs := range statuses {
		for _, ms := range cs.Models {
			if !ms.ManualEnabled {
				out[cs.ID+"\x00"+ms.Model] = true
			}
		}
	}
	return out
}

// aggregateNames 返回所有已启用的虚拟（聚合）模型名（去重、按顺序）。
func (s *Service) aggregateNames(ctx context.Context) []string {
	var names []string
	seen := map[string]bool{}
	if s.routing != nil {
		aggs, err := s.routing.ListAggregates(ctx)
		if err != nil {
			s.lg.Warn("读取聚合模型失败", "err", err)
			return names
		}
		for _, a := range aggs {
			if a.Enabled && !seen[a.Name] {
				seen[a.Name] = true
				names = append(names, a.Name)
			}
		}
		return names
	}
	var aggs []types.AggregateModel
	if err := s.st.Read(types.FileAggregates, &aggs); err != nil {
		if !errors.Is(err, store.ErrNotExist) {
			s.lg.Warn("读取聚合模型失败", "err", err)
		}
		return names
	}
	for _, a := range aggs {
		if !seen[a.Name] {
			seen[a.Name] = true
			names = append(names, a.Name)
		}
	}
	return names
}

// resolveChannels 按模型匹配渠道（含健康检查/熔断、aggregate 指定渠道过滤）。
func (s *Service) resolveChannels(ctx context.Context, model string, metadata map[string]any) ([]ResolvedChannel, error) {
	if s.routing == nil {
		return ResolveChannelsForModel(s.st, model)
	}
	channels, err := s.routing.ListChannels(ctx)
	if err != nil {
		return nil, err
	}
	specified := ""
	var specifiedIDs []string
	specifiedBaseURL := ""
	if metadata != nil {
		specified, _ = metadata["__current_channel"].(string)
		if v, ok := metadata["__channel_candidates"].([]string); ok {
			specifiedIDs = v
		}
		specifiedBaseURL, _ = metadata["__current_channel_base_url"].(string)
	}
	var known, unknown []ResolvedChannel
	for _, channel := range channels {
		// 候选约束：Key 多选列表 > 渠道级 base_url > 单 Key（向后兼容）。
		if len(specifiedIDs) > 0 {
			if !slices.Contains(specifiedIDs, channel.ID) {
				continue
			}
		} else if specifiedBaseURL != "" {
			if NormalizeBaseURL(channel.BaseURL) != NormalizeBaseURL(specifiedBaseURL) {
				continue
			}
		} else if specified != "" && channel.ID != specified {
			continue
		}
		availability, err := s.health.Check(ctx, channel.ID, model)
		if err != nil {
			return nil, err
		}
		// 不可用（手动禁用或自动熔断）的渠道不真实请求，跳过。
		if !availability.EffectiveAvailable {
			continue
		}
		key := ""
		if channel.APIKeyCipher != "" {
			key, err = s.st.Decrypt(channel.APIKeyCipher)
			if err != nil {
				s.lg.Warn("渠道密钥解密失败", "channel", channel.ID, "err", err)
				continue
			}
		}
		candidate := ResolvedChannel{ID: channel.ID, Name: channel.Name, ChannelName: channel.ChannelName, BaseURL: channel.BaseURL, APIKey: key}
		if len(channel.Models) == 0 {
			unknown = append(unknown, candidate)
			continue
		}
		for _, listed := range channel.Models {
			if listed.Enabled && listed.Model == model {
				known = append(known, candidate)
				break
			}
		}
	}
	return append(known, unknown...), nil
}

// newRequestID 生成 16 位十六进制 request_id。
func newRequestID() string {
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(bytes)
}

// rewriteModelField 精确改写 body 顶层 model 字段的值（仅当值等于 from 时替换为 to）。
// 用 json.Decoder 定位 model 值的字节区间做局部替换，其余字节（空白/字段顺序/数值精度）
// 完全不动；BOM/前导空白先剔除再解析。body 非 JSON 对象 / 无 model 字段 / 值不匹配
// 时原样返回（调用方语义：改不到就让上游报错，不静默坏数据）。
func rewriteModelField(body []byte, from, to string) []byte {
	if len(body) == 0 || from == "" || to == from {
		return body
	}
	// 剔除 BOM 与前导空白（json.Decoder 不认 BOM）。
	content := body
	if bytes.HasPrefix(content, []byte("\xEF\xBB\xBF")) {
		content = content[3:]
	}
	trimmed := bytes.TrimLeft(content, " \t\r\n")
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return body // 非 JSON 对象：原样透传
	}

	dec := json.NewDecoder(bytes.NewReader(trimmed))
	tok, err := dec.Token()
	if err != nil {
		return body
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return body
	}
	var valStart, valEnd int64 = -1, -1
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return body
		}
		key, ok := keyTok.(string)
		if !ok {
			return body
		}
		if key == "model" {
			valStart = dec.InputOffset()
			if _, err := dec.Token(); err != nil {
				return body
			}
			valEnd = dec.InputOffset()
			break
		}
		// 跳过非 model 字段的值（含嵌套对象/数组）。
		if err := skipJSONValue(dec); err != nil {
			return body
		}
	}
	if valStart < 0 || valEnd <= valStart {
		return body // 未找到 model 字段
	}

	// InputOffset 语义：读完 key 后指向冒号之前，seg 形如 `:"newapi/gpt-4o"`。
	// 跳过冒号与空白，定位值 token 的实际起点。
	seg := trimmed[valStart:valEnd]
	valBegin := 0
	for valBegin < len(seg) && (seg[valBegin] == ':' || seg[valBegin] == ' ' ||
		seg[valBegin] == '\t' || seg[valBegin] == '\r' || seg[valBegin] == '\n') {
		valBegin++
	}
	if valBegin >= len(seg) || seg[valBegin] != '"' || seg[len(seg)-1] != '"' {
		return body // 值不是字符串（模型名必为字符串）
	}
	valBytes := seg[valBegin:]
	var cur string
	if err := json.Unmarshal(valBytes, &cur); err != nil || cur != from {
		return body // 值不是要改的 from：原样返回
	}
	newVal, err := json.Marshal(to)
	if err != nil {
		return body
	}
	// 局部替换：值起点 = lead + valStart + valBegin，值终点 = lead + valEnd（InputOffset
	// 指向值 token 最后一个字节之后，无尾随空白）。body 其他字节原样保留。
	lead := len(body) - len(trimmed)
	start := lead + int(valStart) + valBegin
	end := lead + int(valEnd)
	out := make([]byte, 0, len(body)+len(newVal)-(end-start))
	out = append(out, body[:start]...)
	out = append(out, newVal...)
	out = append(out, body[end:]...)
	return out
}

// skipJSONValue 跳过 decoder 当前位置的一个完整 JSON 值（对象/数组递归）。
func skipJSONValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); ok {
		switch d {
		case '{', '[':
			for dec.More() {
				if err := skipJSONValue(dec); err != nil {
					return err
				}
			}
			_, err = dec.Token() // 闭合括号
			return err
		}
	}
	return nil
}

// rewriteModelQuery 改写 query 串里的 model 参数（值等于 from 时替换为 to）。
// 用 url.Values 重建，参数顺序可能变化（query 顺序无语义）；无 model 或值不匹配原样返回。
func rewriteModelQuery(rawQuery, from, to string) string {
	if rawQuery == "" || from == "" || to == from {
		return rawQuery
	}
	vals, err := url.ParseQuery(rawQuery)
	if err != nil {
		return rawQuery
	}
	models, ok := vals["model"]
	if !ok || len(models) == 0 {
		return rawQuery
	}
	changed := false
	for i, m := range models {
		if m == from {
			models[i] = to
			changed = true
		}
	}
	if !changed {
		return rawQuery
	}
	vals["model"] = models
	return vals.Encode()
}

func pointer[T any](value T) *T { return &value }

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// truncateErrorBody 把上游错误响应体（JSON 字符串或网络错误文本）裁剪到 8KB 后返回，
// 作为 route_attempts / route_requests.error_body 的入库值。8KB 覆盖绝大多数厂商
// 错误体（典型 1~4KB，腾讯 copilot extError 嵌套最深也 < 3KB），再长则加截断标记。
// 空字符串返回原值避免「被截成空」误导——调用方语义上是 error_body 有没有
// 「真实内容」的指示，空 body 不该被强写非空。
func truncateErrorBody(body string) string {
	const maxBytes = 8 * 1024
	if len(body) == 0 {
		return ""
	}
	if len(body) <= maxBytes {
		return body
	}
	return body[:maxBytes] + "...(truncated)"
}

// parseUsageLine 从单条 SSE data 行解析 usage；若不是 data 行或不含 usage 字段则返回零值。
func parseUsageLine(line string) contracts.TokenUsage {
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "data:") {
		return contracts.TokenUsage{}
	}
	data := strings.TrimSpace(line[len("data:"):])
	if data == "" || data == "[DONE]" {
		return contracts.TokenUsage{}
	}
	var parsed struct {
		Usage struct {
			PromptTokens        int `json:"prompt_tokens"`
			CompletionTokens    int `json:"completion_tokens"`
			TotalTokens         int `json:"total_tokens"`
			PromptTokensDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(data), &parsed); err != nil {
		return contracts.TokenUsage{}
	}
	if parsed.Usage.PromptTokens == 0 && parsed.Usage.CompletionTokens == 0 {
		return contracts.TokenUsage{}
	}
	return contracts.TokenUsage{
		PromptTokens:     parsed.Usage.PromptTokens,
		CompletionTokens: parsed.Usage.CompletionTokens,
		TotalTokens:      parsed.Usage.TotalTokens,
		CachedTokens:     parsed.Usage.PromptTokensDetails.CachedTokens,
	}
}

// extractUsageNonStream 从非流式上游响应体（完整 JSON）里提取 usage 字段。
func extractUsageNonStream(body []byte) contracts.TokenUsage {
	var parsed struct {
		Usage struct {
			PromptTokens        int `json:"prompt_tokens"`
			CompletionTokens    int `json:"completion_tokens"`
			TotalTokens         int `json:"total_tokens"`
			PromptTokensDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return contracts.TokenUsage{}
	}
	if parsed.Usage.PromptTokens == 0 && parsed.Usage.CompletionTokens == 0 {
		return contracts.TokenUsage{}
	}
	return contracts.TokenUsage{
		PromptTokens:     parsed.Usage.PromptTokens,
		CompletionTokens: parsed.Usage.CompletionTokens,
		TotalTokens:      parsed.Usage.TotalTokens,
		CachedTokens:     parsed.Usage.PromptTokensDetails.CachedTokens,
	}
}

// setRequestIDHeader 把当前请求的 request_id 写入响应头。
// 客户端（特别是带自动重试的 SDK）重试时复用此 header，后端按 X-Request-Id UPSERT 合并日志。
func setRequestIDHeader(w http.ResponseWriter, requestID string) {
	if requestID == "" {
		return
	}
	w.Header().Set("X-Request-Id", requestID)
}

// writeOpenAIError 写出标准 OpenAI 错误响应 {"error":{"message":...,"type":...}}。
func writeOpenAIError(w http.ResponseWriter, status int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"message": msg, "type": typ},
	})
}

// writeGatewayError 把 waterfall 返回的错误按类型写回客户端。
func writeGatewayError(w http.ResponseWriter, err error) {
	var gw *GatewayError
	if errors.As(err, &gw) {
		status := gw.Status
		if status == 0 {
			status = http.StatusBadRequest
		}
		writeOpenAIError(w, status, gw.Type, gw.Msg)
		return
	}
	writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
}

// writeSSEError 向流式响应补发一个 OpenAI 标准 error 事件。
func writeSSEError(w http.ResponseWriter, flusher http.Flusher, msg string) {
	block := map[string]any{
		"error": map[string]string{"message": msg, "type": "upstream_stream_error"},
	}
	if data, err := json.Marshal(block); err == nil {
		_, _ = io.WriteString(w, "data: "+string(data)+"\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}
}

// upstreamErrorMsg 从上游错误体里尝试抽出最核心的 message 字段。
//
// 只返回原始 message 字符串（不再带「上游返回错误(N):」前缀）——前缀交给调用方
// 按统一格式加；旧实现既返回前缀又在 proxy.go 外层再拼一遍前缀，导致同一错误
// 在响应/日志里被打印成「上游返回错误(500): 上游返回错误(500)」。
//
// 返回值约定：
//   - 错误体是合法 JSON 且带 error.message → 直接返回 message（不含前缀，不含状态码）；
//   - 错误体是合法 JSON 但无 error.message / 空 message → 返回 ""，调用方按纯状态码兜底；
//   - 错误体不是 JSON → 返回 ""，调用方按纯状态码兜底，body 部分另由 errorBody 字段独立保留。
//
// 调用方需要原始错误体（含不匹配 JSON 的 HTML/纯文本）时直接读 errorBody，不要从
// 本函数返回值里"反推"——本函数只负责给日志/响应一个简短的分类标签。
func upstreamErrorMsg(body []byte, status int) string {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil {
		return strings.TrimSpace(parsed.Error.Message)
	}
	return ""
}

// upstreamErrorSummary 给日志/响应构造统一一行错误摘要：「上游返回错误(N)[ : msg]」，
// msg 为空时省略冒号部分。统一收口，避免重复前缀历史再次出现（参见 upstreamErrorMsg）。
func upstreamErrorSummary(status int, msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return fmt.Sprintf("上游返回错误(%d)", status)
	}
	return fmt.Sprintf("上游返回错误(%d): %s", status, msg)
}
