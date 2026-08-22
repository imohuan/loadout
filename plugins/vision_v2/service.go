package visionv2

import (
	"log/slog"
	"path/filepath"
	"sync"

	"loadout/core/config"
	"loadout/core/db"
	"loadout/core/store"
	"loadout/plugins/contracts"
)

// toolLoopState 单个请求的工具循环状态（Service 单例按 requestID 隔离）。
type toolLoopState struct {
	format  visionProxyFormat  // chat / claude / responses
	acc     *StreamAccumulator // 流式解析状态机（后续任务定义）
	pending bool
	calls   []ToolCall
	active  bool
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
	cacheDir string

	mu     sync.Mutex
	states map[string]*toolLoopState // key: pipe.RequestID
}

func NewService(st *store.Store, repo *db.Repository, lg *slog.Logger) *Service {
	return &Service{st: st, lg: lg, repo: repo, cacheDir: config.VisionCacheDir, states: map[string]*toolLoopState{}}
}

func (s *Service) SetRouteLog(rl contracts.RouteLog) { s.routeLog = rl }

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
