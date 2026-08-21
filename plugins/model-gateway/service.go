package modelgateway

import (
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
		rc := ResolvedChannel{ID: ch.ID, Name: ch.Name, BaseURL: ch.BaseURL, APIKey: key}
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
	if s.routing != nil {
		channels, err := s.routing.ListChannels(r.Context())
		if err != nil {
			writeOpenAIError(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		for _, channel := range channels {
			if !channel.ManualEnabled {
				continue
			}
			for _, model := range channel.Models {
				if !model.Enabled {
					continue
				}
				if modelChannels[model.Model] == nil {
					modelChannels[model.Model] = map[string]bool{}
				}
				modelChannels[model.Model][channel.ID] = true
			}
		}
	} else {
		var channels []types.Channel
		if err := s.st.Read(types.FileChannels, &channels); err == nil {
			for _, ch := range channels {
				if !ch.Enabled {
					continue
				}
				for _, m := range ch.Models {
					if modelChannels[m] == nil {
						modelChannels[m] = map[string]bool{}
					}
					modelChannels[m][ch.ID] = true
				}
			}
		}
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
		candidate := ResolvedChannel{ID: channel.ID, Name: channel.Name, BaseURL: channel.BaseURL, APIKey: key}
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

func pointer[T any](value T) *T { return &value }

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
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

// upstreamErrorMsg 尽量从上游错误体提取 message，否则用状态码兜底。
func upstreamErrorMsg(body []byte, status int) string {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Error.Message != "" {
		return fmt.Sprintf("上游返回错误(%d): %s", status, parsed.Error.Message)
	}
	return fmt.Sprintf("上游返回错误(%d)", status)
}
