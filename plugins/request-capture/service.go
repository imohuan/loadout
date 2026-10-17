package requestcapture

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"loadout/core/db"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// capabilityName 能力路由表中请求捕获能力的固定名称。
const capabilityName = "request_capture"

// Service 请求捕获适配器：命中能力路由的请求在进入中转最前端把完整原始数据
// 打包落盘（调试抓包用途）。
type Service struct {
	st   *store.Store
	lg   logger
	repo *db.Repository
	dir  string
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

// captureEnvelope 打包文件内容：请求方向的最原始数据（进入中转时、任何插件处理之前）。
type captureEnvelope struct {
	CapturedAt string            `json:"captured_at"`
	RequestID  string            `json:"request_id"`
	Method     string            `json:"method"`
	Path       string            `json:"path"`
	Query      string            `json:"query"`
	Headers    map[string][]string `json:"headers"`
	Body       string            `json:"body"` // 原始字节，UTF-8 不合法时存 hex
}

// decideRoutes 查能力路由：model + 渠道上下文的 request_capture 路由。
// fail-open：读表失败按透传处理，绝不阻塞请求。
func (s *Service) decideRoutes(pipe *modelgateway.ProxyPipeline, virtualModel string) []*types.CapabilityRoute {
	if pipe == nil || pipe.Request == nil {
		return nil
	}
	scope := types.ChannelScopeFromMetadata(pipe.Metadata, s.requestChannelBaseURLs)
	if s.repo != nil {
		routes, err := s.repo.ListCapabilityRoutes(context.Background())
		if err == nil {
			return types.SelectCapabilityRoutesEx(routes, capabilityName, pipe.Request.Model, virtualModel, scope)
		}
		s.lg.Warn("request-capture: 读能力路由表失败，按透传处理", "err", err)
		return nil
	}
	var routes []types.CapabilityRoute
	if err := s.st.Read(types.FileCapabilityRoutes, &routes); err != nil {
		return nil // 不存在/读失败：透传
	}
	return types.SelectCapabilityRoutesEx(routes, capabilityName, pipe.Request.Model, virtualModel, scope)
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
// 【铁律】永不 return error、永不改 body —— 只读快照，出错仅记日志（best-effort）。
// 文件名 = pipe.RequestID（与 request-log 列表的"请求 ID"一致，可互相检索）。
func (s *Service) HandleProxyBeforeUpstream(payload any) (any, error) {
	pipe, ok := payload.(*modelgateway.ProxyPipeline)
	if !ok || pipe == nil || pipe.Request == nil || s.dir == "" {
		return payload, nil
	}
	// 子请求（vision_v2 识别等）：跳过——那是插件自己发的内部请求，不是用户流量。
	if pipe.Metadata != nil {
		if v, _ := pipe.Metadata["__sub_request_skip_security"].(bool); v {
			return payload, nil
		}
	}
	routes := s.decideRoutes(pipe, types.VirtualModelFromMetadata(pipe.Metadata))
	// native 透传不抓；error 路由同样不抓（语义只支持 proxy）。
	if len(routes) == 0 || routes[0].Route != types.RouteProxy {
		return payload, nil
	}

	env := captureEnvelope{
		CapturedAt: time.Now().UTC().Format(time.RFC3339Nano),
		RequestID:  pipe.RequestID,
		Method:     pipe.Request.Method,
		Path:       pipe.Request.Path,
		Query:      pipe.Request.Query,
		Headers:    pipe.Request.Header,
		Body:       bodyToString(pipe.Request.Body),
	}
	if err := s.writeCapture(pipe.RequestID, env); err != nil {
		s.lg.Warn("request-capture: 写捕获文件失败", "request_id", pipe.RequestID, "err", err)
		return payload, nil
	}
	s.lg.Info("request-capture: 已捕获原始请求",
		"request_id", pipe.RequestID, "model", pipe.Request.Model, "path", pipe.Request.Path)
	return payload, nil
}

// bodyToString 原始 body 转 UTF-8 字符串；非法字节以 hex 保留（不丢信息）。
func bodyToString(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	if utf8.Valid(body) {
		return string(body)
	}
	return fmt.Sprintf("hex:%x", body)
}

// writeCapture gzip 压缩写盘（临时文件 + rename 原子）。
func (s *Service) writeCapture(requestID string, env captureEnvelope) error {
	if requestID == "" {
		return fmt.Errorf("request-capture: 空 request_id")
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("create capture dir: %w", err)
	}
	final := filepath.Join(s.dir, requestID+".cap.json.gz")
	tmp := final + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	gz := gzip.NewWriter(f)
	data, err := json.Marshal(env)
	if err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("marshal envelope: %w", err)
	}
	if _, err := gz.Write(data); err != nil {
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
	return nil
}

