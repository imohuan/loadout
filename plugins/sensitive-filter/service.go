package sensitivefilter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"loadout/core/db"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// capabilityName 能力路由表中敏感词过滤能力的固定名称。
const capabilityName = "sensitive_filter"

// Service 敏感词过滤适配器：查能力路由、对请求体做整体字符串替换，破坏 JSON 时降级为只替换 messages 文本。
type Service struct {
	st   *store.Store
	lg   *slog.Logger
	repo *db.Repository // SQLite 能力路由数据源（装配后注入；nil 时回退 JSON）
}

// NewService 创建敏感词过滤适配器。
func NewService(st *store.Store, lg *slog.Logger) *Service {
	return &Service{st: st, lg: lg}
}

// SetRepository 注入 SQLite 仓储（由装配层在 db 就绪后调用；测试可省略）。
func (s *Service) SetRepository(repo *db.Repository) { s.repo = repo }

// DecideRoute 查能力路由表：model + channelID 的 sensitive_filter 能力。未命中返回 nil（视为 native 透传）。
// channelID 为请求当前渠道（pipe.Metadata["__current_channel"]，聚合模型指定渠道后已设置；
// 普通请求渠道未知传空串）。路由未绑定渠道（channel_ids 为空）= 全渠道命中，行为与 vision 一致；
// 绑定渠道后仅请求渠道命中集合内才生效。
//
// 读表/解析失败采取 fail-open：记录日志并返回 nil（按 native 透传），不拒绝请求。
// 与 vision 不同，敏感词过滤对每个 JSON 请求都会查表，坏表若 fail-closed 会拒绝全部 /v1 流量。
func (s *Service) DecideRoute(model, channelID string) (*types.CapabilityRoute, error) {
	if s.repo != nil {
		routes, err := s.repo.ListCapabilityRoutes(context.Background())
		if err == nil {
			for i := range routes {
				if routes[i].Capability == capabilityName &&
					types.MatchModels(routes[i].Models, model) &&
					types.MatchChannel(routes[i].ChannelIDs, channelID) {
					return &routes[i], nil
				}
			}
			return nil, nil
		}
		s.lg.Error("sensitive-filter: 从 SQLite 读能力路由表失败，回退 JSON", "err", err)
	}
	var routes []types.CapabilityRoute
	if err := s.st.Read(types.FileCapabilityRoutes, &routes); err != nil {
		if errors.Is(err, store.ErrNotExist) {
			return nil, nil
		}
		s.lg.Error("sensitive-filter: 读取能力路由表失败，按透传处理", "err", err)
		return nil, nil
	}
	for i := range routes {
		if routes[i].Capability == capabilityName &&
			types.MatchModels(routes[i].Models, model) &&
			types.MatchChannel(routes[i].ChannelIDs, channelID) {
			return &routes[i], nil
		}
	}
	return nil, nil
}

// HandleProxyBeforeUpstream 透明代理输入 hook：对请求体做敏感词过滤。
// 仅处理合法 JSON body（非 JSON 原样透传，避免误伤二进制/表单）；未命中路由、native 路由原样透传。
// proxy：整体 stringify → 逐条替换 → 校验；若整体替换破坏 JSON（如替换词含引号/换行），
// 自动降级为「只替换 messages 下的文本字段」再放行，绝不报错拒绝请求。
// error：命中任一敏感词直接拒绝。
func (s *Service) HandleProxyBeforeUpstream(payload any) (any, error) {
	pipe, ok := payload.(*modelgateway.ProxyPipeline)
	if !ok || pipe == nil || pipe.Request == nil || len(pipe.Request.Body) == 0 {
		return payload, nil
	}
	if !json.Valid(pipe.Request.Body) {
		return payload, nil
	}

	model := pipe.Request.Model
	channelID, _ := pipe.Metadata["__current_channel"].(string)
	route, err := s.DecideRoute(model, channelID)
	if err != nil {
		// 防御：DecideRoute 现为 fail-open，正常不会走到这里。
		return nil, sensitiveError(err.Error())
	}
	if route == nil || route.Route == types.RouteNative {
		return payload, nil
	}

	text := string(pipe.Request.Body)
	switch route.Route {
	case types.RouteError:
		hit, err := containsAny(text, route.Replacements)
		if err != nil {
			return nil, sensitiveError(err.Error())
		}
		if hit {
			s.lg.Warn("敏感词过滤命中，请求被拒绝", "model", model, "channel_id", channelID, "path", pipe.Request.Path)
			return nil, sensitiveError(fmt.Sprintf("请求命中敏感词过滤规则，模型 %q 已拒绝", model))
		}
		return payload, nil
	case types.RouteProxy:
		replaced, err := replaceAll(text, route.Replacements)
		if err != nil {
			return nil, sensitiveError(err.Error())
		}
		if !json.Valid([]byte(replaced)) {
			// 整体替换破坏了 JSON（如替换词含引号/换行）→ 降级：只替换 messages 下的文本，
			// 循环所有消息的文本字段逐一替换，绝不报错拒绝请求。
			s.lg.Warn("敏感词整体替换破坏 JSON，降级为逐条替换 messages 文本",
				"model", model, "channel_id", channelID, "path", pipe.Request.Path)
			fallback, ferr := replaceMessagesText(pipe.Request.Body, route.Replacements)
			if ferr != nil {
				return nil, sensitiveError(ferr.Error())
			}
			pipe.Request.Body = fallback
			return pipe, nil
		}
		pipe.Request.Body = []byte(replaced)
		return pipe, nil
	default:
		return payload, nil
	}
}

// replaceMessagesText 降级路径：解析 JSON 结构，只对 messages 下的文本字段做替换。
// 遍历每条消息的 content：纯字符串直接替换；分段数组（text/input_text 块）只替换 text 字段。
// 不改动 JSON 结构，替换结果天然合法，绝不破坏 JSON。
func replaceMessagesText(body []byte, rules []types.SensitiveReplacement) ([]byte, error) {
	var root map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("sensitive-filter: 解析请求体失败: %w", err)
	}
	// messages 为 OpenAI/Claude 对话格式，input 为 responses 格式（与 vision 一致）。
	for _, key := range []string{"messages", "input"} {
		raw, ok := root[key]
		if !ok {
			continue
		}
		messages, ok := raw.([]any)
		if !ok {
			continue
		}
		for _, rawMsg := range messages {
			msg, ok := rawMsg.(map[string]any)
			if !ok {
				continue
			}
			content, ok := msg["content"]
			if !ok {
				continue
			}
			// 纯字符串 content。
			if text, ok := content.(string); ok {
				replaced, err := replaceAll(text, rules)
				if err != nil {
					return nil, err
				}
				msg["content"] = replaced
				continue
			}
			// 分段数组 content：只替换文本块，图片等原样保留。
			parts, ok := content.([]any)
			if !ok {
				continue
			}
			for _, rawPart := range parts {
				part, ok := rawPart.(map[string]any)
				if !ok {
					continue
				}
				typ, _ := part["type"].(string)
				if typ != "text" && typ != "input_text" {
					continue
				}
				text, _ := part["text"].(string)
				replaced, err := replaceAll(text, rules)
				if err != nil {
					return nil, err
				}
				part["text"] = replaced
			}
		}
	}
	out, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("sensitive-filter: 重序列化请求体失败: %w", err)
	}
	return out, nil
}

// containsAny 判断 text 是否命中任一规则（error 模式用）。
// 正则规则编译失败视为配置错误，返回 error 让请求拒绝（与 replaceAll 的失败语义一致，
// 避免同一条坏正则在不同 route 下安全姿态相反）。空 from 规则跳过（配置防御）。
func containsAny(text string, rules []types.SensitiveReplacement) (bool, error) {
	for _, r := range rules {
		if r.From == "" {
			continue
		}
		if r.Regex {
			re, err := regexp.Compile(r.From)
			if err != nil {
				return false, fmt.Errorf("敏感词正则规则非法 %q: %w", r.From, err)
			}
			if re.MatchString(text) {
				return true, nil
			}
		} else if strings.Contains(text, r.From) {
			return true, nil
		}
	}
	return false, nil
}

// replaceAll 按数组顺序执行替换；正则规则用 regexp.ReplaceAllString（支持 $1 捕获组）。
// 正则编译失败视为配置错误，返回 error 让请求拒绝（而非静默跳过）。空 from 规则跳过（配置防御）。
func replaceAll(text string, rules []types.SensitiveReplacement) (string, error) {
	out := text
	for _, r := range rules {
		if r.From == "" {
			continue
		}
		if r.Regex {
			re, err := regexp.Compile(r.From)
			if err != nil {
				return "", fmt.Errorf("敏感词正则规则非法 %q: %w", r.From, err)
			}
			out = re.ReplaceAllString(out, r.To)
		} else {
			out = strings.ReplaceAll(out, r.From, r.To)
		}
	}
	return out, nil
}

// sensitiveError 构造统一的敏感词过滤错误（OpenAI error.type = sensitive_filter_error）。
func sensitiveError(msg string) *modelgateway.GatewayError {
	return &modelgateway.GatewayError{Type: "sensitive_filter_error", Msg: msg}
}
