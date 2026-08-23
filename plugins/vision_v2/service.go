package visionv2

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"

	"loadout/core/config"
	"loadout/core/db"
	"loadout/core/store"
	"loadout/plugins/contracts"
	modelgateway "loadout/plugins/model-gateway"
)

// toolLoopState 单个请求的工具循环状态（Service 单例按 requestID 隔离）。
type toolLoopState struct {
	format  visionProxyFormat  // chat / claude / responses
	acc     *StreamAccumulator // 流式解析状态机（后续任务定义）
	pending bool
	calls   []ToolCall
	round   int
	filter  *PlaceholderFilter
	// passthrough 本轮出现非本插件工具（web_search 等）：剩余流完全透传、不拦截。
	passthrough bool
}

// Service 视觉能力 v2 服务。
type Service struct {
	st       *store.Store
	lg       *slog.Logger
	repo     *db.Repository
	routeLog contracts.RouteLog
	// gw 子请求通道：视觉识别 / 续流走 model-gateway 主链路（request-log/额度/failover）。
	gw       SubRequestForwarder
	cacheDir string

	mu     sync.Mutex
	states map[string]*toolLoopState // key: pipe.RequestID
}

// SubRequestForwarder 子请求转发接口：vision_v2 只依赖这一个方法，不耦合整个
// model-gateway Service（测试可 mock）。实现为 modelgateway.Service.ForwardSubRequest。
type SubRequestForwarder interface {
	ForwardSubRequest(ctx context.Context, pipe *modelgateway.ProxyPipeline, streamWriter func(line []byte) error) (*modelgateway.ProxyPipeline, []byte, error)
}

func NewService(st *store.Store, repo *db.Repository, lg *slog.Logger) *Service {
	return &Service{st: st, lg: lg, repo: repo, cacheDir: config.VisionCacheDir, states: map[string]*toolLoopState{}}
}

func (s *Service) SetRouteLog(rl contracts.RouteLog) { s.routeLog = rl }

// SetGateway 注入 model-gateway 服务（子请求通道用）。
func (s *Service) SetGateway(gw SubRequestForwarder) { s.gw = gw }

func (s *Service) state(id string) *toolLoopState {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.states[id]
	if !ok {
		st = &toolLoopState{}
		s.states[id] = st
	}
	return st
}

func (s *Service) dropState(id string) {
	s.mu.Lock()
	delete(s.states, id)
	s.mu.Unlock()
}

// imageFilesDir 图片落盘目录。
func (s *Service) imageFilesDir() string { return filepath.Join(s.cacheDir, "files") }

// visionProxyFormat 描述一种可被视觉能力改写的消息格式。
type visionProxyFormat int

const (
	formatUnknown   visionProxyFormat = iota
	formatChat                        // chat/completions：messages，图片块 image_url
	formatClaude                      // /v1/messages：messages，图片块 image（source）
	formatResponses                   // /v1/responses：input，图片块 input_image
)
