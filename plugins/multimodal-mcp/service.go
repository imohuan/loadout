package multimodalmcp

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"loadout/core/db"
	"loadout/core/mcpkit"
	"loadout/core/store"
	"loadout/plugins/contracts"
	modelgateway "loadout/plugins/model-gateway"
)

// SubRequestForwarder 子请求转发接口：多模态只依赖这一个方法，不耦合整个
// model-gateway Service（测试可 mock）。实现为 modelgateway.Service.ForwardSubRequest。
type SubRequestForwarder interface {
	ForwardSubRequest(ctx context.Context, pipe *modelgateway.ProxyPipeline, streamWriter func(line []byte) error) (*modelgateway.ProxyPipeline, []byte, error)
}

// Service 多模态 MCP 插件服务：持有 store/db/logger 与子请求网关，
// 提供 MCP 端点、配置读写与 3 个识别工具的 schema 与分发。
// 识别函数签名已在此定义（供各识别子代理实现），当前均为 TODO 占位。
type Service struct {
	st    *store.Store
	lg    *slog.Logger
	repo  *db.Repository
	gw    SubRequestForwarder // 子请求通道（model-gateway 主链路）
	route contracts.RouteLog  // 路由日志（注入备用）
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

// ===== 识别方法签名（与其他子代理的契约）=====
// 以下方法由「识别函数」子代理实现：把工具 Handler 解析出的 args 转成
// 对应资源的请求 payload，经 s.gw 走 ForwardSubRequest，返回 *mcpkit.ToolResult。
// 当前均未实现（TODO），返回「未实现」错误占位，保证包结构可编译。

// understandImage 实现 understand_image 工具：图片理解（detail 精细度 + 资源三态）。
func (s *Service) understandImage(ctx context.Context, args map[string]any) (*mcpkit.ToolResult, error) {
	return nil, errNotImplemented("understand_image")
}

// understandVideo 实现 understand_video 工具：视频理解（fps 抽帧 + 资源三态）。
func (s *Service) understandVideo(ctx context.Context, args map[string]any) (*mcpkit.ToolResult, error) {
	return nil, errNotImplemented("understand_video")
}

// understandAudio 实现 understand_audio 工具：音频理解（task 决定识别模式 + 语种参数）。
func (s *Service) understandAudio(ctx context.Context, args map[string]any) (*mcpkit.ToolResult, error) {
	return nil, errNotImplemented("understand_audio")
}

// errNotImplemented 返回「识别函数尚未实现」占位错误。
func errNotImplemented(tool string) error {
	return fmt.Errorf("multimodal-mcp: 工具 %s 的识别函数尚未实现（待识别子代理接入）", tool)
}
