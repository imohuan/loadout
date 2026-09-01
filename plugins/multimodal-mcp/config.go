package multimodalmcp

import (
	"encoding/json"
	"errors"
	"net/http"

	"loadout/core/store"
)

// FileMultimodalConfig 多模态配置落盘的 JSON 文件名（自定义常量，types 包暂无此文件）。
const FileMultimodalConfig = "multimodal_config.json"

// ToolKind 工具类型：图片 / 视频 / 音频。
type ToolKind string

const (
	ToolImage ToolKind = "image"
	ToolVideo ToolKind = "video"
	ToolAudio ToolKind = "audio"
)

// ToolConfig 单个工具的配置：启用 + 内置模型名 + 默认参数。
type ToolConfig struct {
	Kind     ToolKind       `json:"kind"`
	Enabled  bool           `json:"enabled"`
	Model    string         `json:"model"`              // 内置模型名（请求体 model 字段，不参与渠道匹配）
	Defaults map[string]any `json:"defaults,omitempty"` // 图片: detail; 视频: fps; 音频: task/language 等
}

// MultimodalConfig 多模态插件整体配置（store JSON 落盘）。
type MultimodalConfig struct {
	Enabled bool         `json:"enabled"` // 端点总开关
	Tools   []ToolConfig `json:"tools"`   // 3 个工具配置
	// 音频 task 的 instructions 模板内置在 prompt.go，不存配置（除非需要用户覆盖）。
}

// DefaultConfig 返回多模态配置的默认值（端点关闭、3 个工具默认启用且带默认参数）。
func DefaultConfig() *MultimodalConfig {
	return &MultimodalConfig{
		Enabled: false,
		Tools: []ToolConfig{
			{Kind: ToolImage, Enabled: true, Defaults: map[string]any{"detail": "high"}},
			{Kind: ToolVideo, Enabled: true, Defaults: map[string]any{"fps": 1}},
			{Kind: ToolAudio, Enabled: true, Defaults: map[string]any{"task": "asr", "language": ""}},
		},
	}
}

// loadConfig 从 store 读取多模态配置；文件不存在时返回默认配置。
func (s *Service) loadConfig() (*MultimodalConfig, error) {
	var cfg MultimodalConfig
	if err := s.st.Read(FileMultimodalConfig, &cfg); err != nil {
		if errors.Is(err, store.ErrNotExist) {
			return DefaultConfig(), nil
		}
		return nil, err
	}
	return &cfg, nil
}

// saveConfig 把多模态配置写入 store（JSON 文件）。
func (s *Service) saveConfig(cfg *MultimodalConfig) error {
	return s.st.Write(FileMultimodalConfig, cfg)
}

// HandlerConfig 处理 GET/PUT /api/multimodal/config（AuthSession 由 servercore 包装）。
func (s *Service) HandlerConfig() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			cfg, err := s.loadConfig()
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, cfg)
		case http.MethodPut:
			var cfg MultimodalConfig
			if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
				writeError(w, http.StatusBadRequest, "请求体不是合法 JSON")
				return
			}
			if err := s.saveConfig(&cfg); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			// 保存后同步 mcp-hub 的内置 server 注册（启用 → 工具进聚合；关闭 → 注销）。
			if err := s.syncHubRegistration(); err != nil {
				s.lg.Warn("multimodal-mcp: 同步内置 server 注册失败", "err", err)
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		default:
			writeError(w, http.StatusMethodNotAllowed, "不支持的请求方法")
		}
	})
}

// writeJSON 写 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError 写错误 JSON 响应。
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}
