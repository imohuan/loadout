package multimodalmcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"loadout/core/db"
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
// 识别函数实现在 image.go / video.go / audio.go，经 s.gw 走子请求通道。
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

// ===== 识别方法（由识别函数子代理实现）=====
// 以下方法把工具 Handler 解析出的 args 转成对应资源的请求 payload，经 s.gw 走
// ForwardSubRequest，返回 *mcpkit.ToolResult。实现在 image.go / video.go / audio.go。
