package multimodalmcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"loadout/core/db"
	"loadout/core/mcpkit"
	"loadout/core/store"
	"loadout/plugins/contracts"
	mcphub "loadout/plugins/mcp-hub"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// 内置 server 在 mcp-hub 里的固定标识（幂等注册/注销，与用户手动配置的 server 区分）。
const (
	builtinServerID   = "builtin-multimodal"
	builtinServerName = "multimodal"
)

// SubRequestForwarder 子请求转发接口：多模态只依赖这一个方法，不耦合整个
// model-gateway Service（测试可 mock）。实现为 modelgateway.Service.ForwardSubRequest。
type SubRequestForwarder interface {
	ForwardSubRequest(ctx context.Context, pipe *modelgateway.ProxyPipeline, streamWriter func(line []byte) error) (*modelgateway.ProxyPipeline, []byte, error)
}

// Service 多模态 MCP 插件服务：持有 store/db/logger 与子请求网关，
// 提供 MCP 端点、配置读写与 3 个识别工具的 schema 与分发。
// 识别函数实现在 image.go / video.go / audio.go，经 s.gw 走子请求通道。
type Service struct {
	st    *store.Store
	lg    *slog.Logger
	repo  *db.Repository
	gw    SubRequestForwarder // 子请求通道（model-gateway 主链路）
	route contracts.RouteLog  // 路由日志（注入备用）
	hub   *mcphub.Service     // mcp-hub 内置 server 注册（把工具挂进 $smart 聚合）
	mu    sync.Mutex
}

// NewService 创建多模态服务。
func NewService(st *store.Store, repo *db.Repository, lg *slog.Logger) *Service {
	return &Service{st: st, lg: lg, repo: repo}
}

// SetGateway 注入 model-gateway 子请求通道。
func (s *Service) SetGateway(gw SubRequestForwarder) { s.gw = gw }

// SetRouteLog 注入路由日志服务。
func (s *Service) SetRouteLog(rl contracts.RouteLog) { s.route = rl }

// SetMcpHub 注入 mcp-hub（用于把多模态注册成内置 server，工具进 $smart 聚合）。
func (s *Service) SetMcpHub(hub *mcphub.Service) { s.hub = hub }

// builtinTools 构造注册进 mcp-hub 的 3 个内置工具（handler 直调识别方法，复用独立端点逻辑）。
func (s *Service) builtinTools() []mcphub.ToolEntry {
	handler := func(fn func(context.Context, map[string]any) (*mcpkit.ToolResult, error)) func(context.Context, map[string]any) (*mcpkit.ToolResult, error) {
		return func(ctx context.Context, args map[string]any) (*mcpkit.ToolResult, error) {
			return fn(ctx, args)
		}
	}
	out := make([]mcphub.ToolEntry, 0, 3)
	for _, t := range tools(s) {
		out = append(out, mcphub.ToolEntry{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
			BuiltinHandler: handler(func(ctx context.Context, args map[string]any) (*mcpkit.ToolResult, error) {
				return t.Handler(ctx, args)
			}),
		})
	}
	return out
}

// syncHubRegistration 按当前配置把多模态注册/注销为 mcp-hub 的内置 server：
// cfg.Enabled=true → 注册（内置 server + 3 工具进聚合）；否则注销。
// 幂等：hub 注册用固定 ID；未配置 hub（如单测）时安全跳过。
func (s *Service) syncHubRegistration() error {
	if s.hub == nil {
		return nil
	}
	cfg, err := s.loadConfig()
	if err != nil {
		return fmt.Errorf("multimodal-mcp: 读取配置失败: %w", err)
	}
	ctx := context.Background()
	if cfg.Enabled {
		srv := types.MCPServer{
			ID:          builtinServerID,
			Name:        builtinServerName,
			Description: "内置多模态 MCP 端点（图片/视频/音频理解）",
			Transport:   types.TransportHTTP,
			Enabled:     true,
			Builtin:     true,
		}
		return s.hub.RegisterBuiltinServer(ctx, srv, s.builtinTools())
	}
	return s.hub.UnregisterBuiltinServer(ctx, builtinServerID)
}

// checkEndpointEnabled 校验端点总开关（cfg.Enabled）。端点被关闭时，
// 工具调用直接报错，前端「多模态」总开关在后端生效。
func (s *Service) checkEndpointEnabled() error {
	cfg, err := s.loadConfig()
	if err != nil {
		return fmt.Errorf("multimodal-mcp: 读取配置失败: %w", err)
	}
	if !cfg.Enabled {
		return errors.New("multimodal-mcp: 多模态端点未启用，请在设置页开启「多模态」")
	}
	return nil
}

// recognitionResult 一次识别调用的结果（供 runRecognition 写 route-log）。
type recognitionResult struct {
	text    string
	reqLog  string // request-log 关联 UUID（子请求）
	channel string // 成功渠道 id
	err     error
}

// recognizeFn 执行实际识别调用（构造请求 → 经 s.gw 子请求通道），返回结果。
type recognizeFn func() recognitionResult

// runRecognition 统一执行一次识别并记录 route-log（转发日志）：
//   - routeLog 已注入时：Start（route_request）→ recognize → Attempt + Finish；
//   - 未注入（单测/独立环境）时：直接 recognize，不写日志。
//
// action 为日志里的动作名（如「图片识别」），model 为内置识别模型名，meta 为附加展示字段
// （如 tool/image/fps/task），toolName 用于区分工具（生成稳定 requestID 前缀）。
func (s *Service) runRecognition(ctx context.Context, toolName, action, model string, meta map[string]any, recognize recognizeFn) (*mcpkit.ToolResult, error) {
	start := time.Now()
	// 生成独立 requestID：多模态是独立 MCP 调用（无主请求），route-log 里作为一条独立请求。
	reqID := fmt.Sprintf("multimodal-%s-%d", toolName, start.UnixNano())

	if s.route == nil {
		// 未注入 route-log：直接识别，不写日志。
		res := recognize()
		if res.err != nil {
			return nil, res.err
		}
		return textResult(res.text), nil
	}

	_ = s.route.Start(ctx, contracts.RouteRequest{
		RequestID:     reqID,
		RequestedModel: model,
		StartedAt:     start,
		VirtualModel:  "",
	})

	res := recognize()
	dur := time.Since(start)

	if res.err != nil {
		s.routeLogAttempt(ctx, reqID, action, model, res, dur, "failed", res.err.Error(), meta)
		s.routeLogFinish(ctx, reqID, model, res, dur, "failed", res.err.Error())
		return nil, res.err
	}
	s.routeLogAttempt(ctx, reqID, action, model, res, dur, "success", "", meta)
	s.routeLogFinish(ctx, reqID, model, res, dur, "success", "")
	return textResult(res.text), nil
}

// routeLogAttempt 写一条 route-log attempt（识别步骤）。
func (s *Service) routeLogAttempt(ctx context.Context, reqID, action, model string, res recognitionResult, dur time.Duration, result, errMsg string, meta map[string]any) {
	start := time.Now().Add(-dur)
	metaOut := map[string]any{"capability": "multimodal"}
	for k, v := range meta {
		metaOut[k] = v
	}
	if _, err := s.route.Attempt(ctx, contracts.RouteAttempt{
		RequestID:     reqID,
		StepNo:        "1",
		Action:        action,
		Model:         model,
		ChannelID:     res.channel,
		RequestLogID:  res.reqLog,
		StartedAt:     start,
		FinishedAt:    ptrTime(time.Now()),
		Result:        result,
		ErrorMessage:  errMsg,
		Duration:      contracts.DurationMS(dur),
		Stream:        false,
		Metadata:      metaOut,
	}); err != nil {
		s.lg.Warn("multimodal-mcp: route-log attempt 写入失败", "err", err)
	}
}

// routeLogFinish 写 route-log 的 request 完成状态。
func (s *Service) routeLogFinish(ctx context.Context, reqID, model string, res recognitionResult, dur time.Duration, result, errMsg string) {
	if err := s.route.Finish(ctx, contracts.RouteFinish{
		RequestID:      reqID,
		FinishedAt:     time.Now(),
		Result:         result,
		FinalModel:     model,
		FinalChannelID: res.channel,
		ErrorMessage:   errMsg,
		Duration:       contracts.DurationMS(dur),
	}); err != nil {
		s.lg.Warn("multimodal-mcp: route-log finish 写入失败", "err", err)
	}
}

// ptrTime 返回 time 指针。
func ptrTime(t time.Time) *time.Time { return &t }

// ===== 识别方法（由识别函数子代理实现）=====
// 以下方法把工具 Handler 解析出的 args 转成对应资源的请求 payload，经 s.gw 走
// ForwardSubRequest，返回 *mcpkit.ToolResult。实现在 image.go / video.go / audio.go。
