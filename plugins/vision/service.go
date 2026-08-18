package vision

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"loadout/core/config"
	"loadout/core/db"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// capabilityName 能力路由表中视觉能力的固定名称。
const capabilityName = "vision"

// visionCacheVersion 缓存键版本号：提示词/schema 行为变更时 +1，让旧格式缓存
// 自然失效重建，避免新旧描述风格混用（旧缓存是自由文本，新输出是结构化板块）。
const visionCacheVersion = "v2"

// Service 视觉能力适配器：检出图片、查能力路由、调视觉模型、改写 messages。
type Service struct {
	st       *store.Store
	lg       *slog.Logger
	repo     *db.Repository // 渠道数据源（SQLite，与主路由一致；旧 channels.json 文件路径已废弃）
	cacheDir string         // 描述缓存目录（config.VisionCacheDir）
}

// NewService 创建视觉能力适配器。
func NewService(st *store.Store, repo *db.Repository, lg *slog.Logger) *Service {
	return &Service{st: st, lg: lg, repo: repo, cacheDir: config.VisionCacheDir}
}

// DetectImages 检出 messages 里所有图片（image_url，含 URL 与 base64 data URI）。
func (s *Service) DetectImages(messages []modelgateway.ChatMessage) []string {
	var images []string
	for _, m := range messages {
		for _, p := range m.Content.Parts {
			if p.Type == "image_url" && p.ImageURL != "" {
				images = append(images, p.ImageURL)
			}
		}
	}
	return images
}

// DecideRoute 查能力路由表：model + channelID 的 vision 能力。未命中返回 nil（视为 native 透传）。
// channelID 为请求当前渠道（pipe.Metadata["__current_channel"]，聚合模型指定渠道后已设置；
// 普通请求渠道未知传空串）。路由未绑定渠道（channel_ids 为空）= 全渠道命中，行为与旧版一致；
// 绑定渠道后仅请求渠道命中集合内才生效，避免同名模型跨渠道误伤。
func (s *Service) DecideRoute(model, channelID string) (*types.CapabilityRoute, error) {
	var routes []types.CapabilityRoute
	if err := s.st.Read(types.FileCapabilityRoutes, &routes); err != nil {
		if errors.Is(err, store.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("vision: 读取能力路由表失败: %w", err)
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

// Describe 调视觉模型：按 channelID（显式）或 viaModel（自动路由）解析渠道并依次 failover，返回描述文本。
// streamWriter 非 nil 时视觉走流式：识别过程实时通过 streamWriter 输出（reasoning delta），同时累积完整文本。
// 带 md5 缓存（config.VisionCacheEnabled 开启时）：md5(图片拼接|模型) → cacheDir/<md5>.txt，
// TTL 为 config.VisionCacheTTLHours。
func (s *Service) Describe(ctx context.Context, images []string, viaModel, channelID string, streamWriter func(string) error) (string, error) {
	if len(images) == 0 {
		return "", errors.New("vision: 没有可描述的图片")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	key := md5Hex(strings.Join(images, "\n") + "|" + viaModel + "|" + visionCacheVersion)
	if config.VisionCacheEnabled {
		if text, ok := s.readCache(key); ok {
			if streamWriter != nil {
				_ = streamWriter(text) // 缓存命中：把完整描述一次性输出，保证流式客户端也能看到
			}
			s.lg.Info("视觉缓存命中", "via_model", viaModel)
			return text, nil
		}
	}

	var channels []modelgateway.ResolvedChannel
	if channelID != "" {
		ch, err := s.resolveChannel(ctx, channelID)
		if err != nil {
			return "", err
		}
		channels = []modelgateway.ResolvedChannel{*ch}
	} else {
		var err error
		channels, err = s.channelsForModel(ctx, viaModel)
		if err != nil {
			return "", err
		}
	}
	if len(channels) == 0 {
		return "", fmt.Errorf("vision: 没有可用渠道支持视觉模型 %q", viaModel)
	}

	start := time.Now()
	text, err := s.callVision(ctx, images, viaModel, channels, streamWriter)
	if err != nil {
		return "", err
	}
	s.lg.Info("视觉描述完成", "via_model", viaModel, "耗时_ms", time.Since(start).Milliseconds())

	if config.VisionCacheEnabled {
		if err := s.writeCache(key, text); err != nil {
			s.lg.Warn("写入视觉描述缓存失败", "err", err)
		}
	}
	return text, nil
}

// RewriteMessages 把含图片的消息改写：图片分段替换为文字描述（Text 分段），返回新 messages。
func (s *Service) RewriteMessages(messages []modelgateway.ChatMessage, text string) []modelgateway.ChatMessage {
	out := make([]modelgateway.ChatMessage, 0, len(messages))
	for _, m := range messages {
		if len(m.Content.Parts) == 0 {
			out = append(out, m) // 纯文本消息保持原样
			continue
		}
		nm := m
		parts := make([]modelgateway.MessagePart, 0, len(m.Content.Parts))
		replaced := false
		for _, p := range m.Content.Parts {
			if p.Type == "image_url" {
				if !replaced {
					// 多张图合并成一次描述，只插入一个 text 分段。
					parts = append(parts, modelgateway.MessagePart{Type: "text", Text: text})
					replaced = true
				}
				continue
			}
			parts = append(parts, p)
		}
		nm.Content.Parts = parts
		out = append(out, nm)
	}
	return out
}

// HandleBeforeUpstream 事件处理器：检测图片 → 路由 → proxy 时调 Describe 并改写、设 VisionText；
// error 时返回 *modelgateway.GatewayError{Type:"vision_capability_error"}；native 或无图时不处理原样返回 payload。
func (s *Service) HandleBeforeUpstream(payload any) (any, error) {
	pipe, ok := payload.(*modelgateway.Pipeline)
	if !ok || pipe == nil {
		return payload, nil
	}

	images := s.DetectImages(pipe.Messages)
	if len(images) == 0 {
		return payload, nil
	}

	model := ""
	if pipe.Request != nil {
		model = pipe.Request.Model
	}
	s.lg.Info("检测到图片", "model", model, "图片数", len(images))
	channelID, _ := pipe.Metadata["__current_channel"].(string)
	route, err := s.DecideRoute(model, channelID)
	if err != nil {
		return nil, visionError(err.Error())
	}
	if route == nil || route.Route == types.RouteNative {
		return payload, nil
	}
	if route.Route == types.RouteError {
		return nil, visionError(fmt.Sprintf("模型 %q 不支持视觉能力", model))
	}

	// proxy：按 via_options 依次尝试视觉兜底（视觉模型 + 可选渠道，失败换下一个）。
	options := route.ViaOptions
	if len(options) == 0 {
		options = []types.ViaOption{{ViaModel: config.DefaultVisionModel}}
	}
	s.lg.Info("命中视觉路由", "model", model, "候选数", len(options))
	var lastErr error
	for _, opt := range options {
		viaModel := opt.ViaModel
		if viaModel == "" {
			viaModel = config.DefaultVisionModel
		}
		text, err := s.Describe(context.Background(), images, viaModel, opt.ChannelID, pipe.StreamWriter)
		if err != nil {
			lastErr = err
			s.lg.Warn("视觉候选失败，尝试下一个", "via_model", viaModel, "err", err)
			continue
		}
		pipe.Messages = s.RewriteMessages(pipe.Messages, text)
		return pipe, nil
	}
	return nil, visionError(fmt.Sprintf("视觉能力调用失败: %v", lastErr))
}

// handleBeforeUpstream keeps package-level callers on the historical helper
// name while the plugin registers the exported event handler.
func (s *Service) handleBeforeUpstream(payload any) (any, error) {
	return s.HandleBeforeUpstream(payload)
}

// resolveChannel 按 channel_id 查渠道表（SQLite，与主路由同源）并解密 api_key。
// 旧版读 channels.json 文件：渠道存储迁移到 db 后文件不再存在，故统一走 Repository。
func (s *Service) resolveChannel(ctx context.Context, channelID string) (*modelgateway.ResolvedChannel, error) {
	if s.repo == nil {
		return nil, fmt.Errorf("vision: 渠道仓储未初始化")
	}
	channels, err := s.repo.ListChannels(ctx)
	if err != nil {
		return nil, fmt.Errorf("vision: 读取渠道表失败: %w", err)
	}
	for _, ch := range channels {
		if ch.ID != channelID {
			continue
		}
		key := ""
		if ch.APIKeyCipher != "" {
			k, err := s.st.Decrypt(ch.APIKeyCipher)
			if err != nil {
				return nil, fmt.Errorf("vision: 解密渠道 %q 的 api_key 失败: %w", channelID, err)
			}
			key = k
		}
		return &modelgateway.ResolvedChannel{ID: ch.ID, Name: ch.Name, BaseURL: ch.BaseURL, APIKey: key}, nil
	}
	return nil, fmt.Errorf("vision: 渠道不存在: %s", channelID)
}

// channelsForModel 按模型名解析渠道（SQLite）：已知模型（渠道 Models 精确包含）
// 优先，未知模型（渠道 Models 为空）兜底；与 modelgateway 主路由语义一致。
func (s *Service) channelsForModel(ctx context.Context, model string) ([]modelgateway.ResolvedChannel, error) {
	if s.repo == nil {
		return nil, fmt.Errorf("vision: 渠道仓储未初始化")
	}
	channels, err := s.repo.ListChannels(ctx)
	if err != nil {
		return nil, fmt.Errorf("vision: 读取渠道表失败: %w", err)
	}
	var known, unknown []modelgateway.ResolvedChannel
	for _, ch := range channels {
		if !ch.ManualEnabled {
			continue
		}
		key := ""
		if ch.APIKeyCipher != "" {
			k, err := s.st.Decrypt(ch.APIKeyCipher)
			if err != nil {
				continue // 解密失败跳过该渠道
			}
			key = k
		}
		rc := modelgateway.ResolvedChannel{ID: ch.ID, Name: ch.Name, BaseURL: ch.BaseURL, APIKey: key}
		if len(ch.Models) == 0 {
			unknown = append(unknown, rc)
		} else if channelHasModel(ch.Models, model) {
			known = append(known, rc)
		}
	}
	return append(known, unknown...), nil
}

// channelHasModel 判断渠道模型目录是否精确包含 model。
func channelHasModel(models []db.ChannelModel, model string) bool {
	for _, m := range models {
		if m.Model == model {
			return true
		}
	}
	return false
}

// visionError 构造统一的视觉能力错误（OpenAI error.type = vision_capability_error）。
func visionError(msg string) *modelgateway.GatewayError {
	return &modelgateway.GatewayError{Type: "vision_capability_error", Msg: msg}
}

// callVision 用 OpenAI 兼容接口依次调用候选渠道的视觉模型，任一成功即返回描述文本。
// streamWriter 非 nil 时视觉走流式：识别过程实时输出，同时累积完整文本返回。
func (s *Service) callVision(ctx context.Context, images []string, viaModel string, channels []modelgateway.ResolvedChannel, streamWriter func(string) error) (string, error) {
	if len(images) > config.VisionMaxImages {
		return "", fmt.Errorf("vision: 图片数量 %d 超过上限 %d", len(images), config.VisionMaxImages)
	}
	parts := make([]contentPart, 0, len(images)+1)
	for _, img := range images {
		u := img
		// 压缩只处理 data URI（本地字节），远程 URL 原样透传。
		// 超限是硬性失败（fail 请求）；其余压缩失败静默回退原图，不阻塞识别。
		if config.VisionCompressEnabled {
			if c, err := CompressDataURI(img); err == nil {
				u = c
			} else if errors.Is(err, ErrImageTooLarge) {
				return "", err
			} else {
				s.lg.Warn("图片压缩失败，使用原图", "err", err)
			}
		}
		parts = append(parts, contentPart{Type: "image_url", ImageURL: &imageURL{URL: u}})
	}
	// 提示词：env 未覆盖（空）时使用内置结构化板块模板。
	prompt := config.VisionDescriptionPrompt
	if strings.TrimSpace(prompt) == "" {
		prompt = builtinVisionPrompt
	}
	parts = append(parts, contentPart{Type: "text", Text: prompt})

	payload := struct {
		Model    string        `json:"model"`
		Messages []chatMessage `json:"messages"`
		Stream   bool          `json:"stream"`
	}{
		Model:    viaModel,
		Messages: []chatMessage{{Role: "user", Content: parts}},
		Stream:   streamWriter != nil,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("vision: 序列化视觉请求失败: %w", err)
	}

	client := &http.Client{Timeout: config.VisionTimeout}
	var lastErr error
	for _, ch := range channels {
		target := strings.TrimRight(ch.BaseURL, "/") + "/chat/completions"
		// 每次尝试都记录完整目标 URL + 模型，便于排查模型不存在/端点错误。
		s.lg.Info("视觉请求发出",
			"via_model", viaModel,
			"channel", ch.Name,
			"channel_id", ch.ID,
			"url", target,
		)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		if ch.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+ch.APIKey)
		}

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			s.lg.Warn("视觉渠道请求失败",
				"via_model", viaModel,
				"channel", ch.Name,
				"url", target,
				"status", resp.StatusCode,
				"body", string(b),
			)
			lastErr = fmt.Errorf("vision: 视觉模型返回错误(%d): %s", resp.StatusCode, string(b))
			continue
		}

		// 流式：边读边输出 + 累积完整文本。
		if streamWriter != nil {
			text, err := s.readVisionStream(resp.Body, streamWriter)
			resp.Body.Close()
			if err != nil {
				lastErr = err
				continue
			}
			s.lg.Info("视觉渠道成功", "via_model", viaModel, "channel", ch.Name, "url", target)
			return text, nil
		}

		// 非流式。
		respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}

		var parsed struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(respBody, &parsed); err != nil {
			lastErr = err
			continue
		}
		if len(parsed.Choices) == 0 || parsed.Choices[0].Message.Content == "" {
			lastErr = errors.New("vision: 视觉模型未返回描述文本")
			continue
		}
		s.lg.Info("视觉渠道成功", "via_model", viaModel, "channel", ch.Name)
		return parsed.Choices[0].Message.Content, nil
	}
	return "", fmt.Errorf("vision: 所有渠道均失败: %v", lastErr)
}

// readVisionStream 解析视觉模型的 SSE 流：累积 content delta，并实时通过 streamWriter 输出。
func (s *Service) readVisionStream(body io.Reader, streamWriter func(string) error) (string, error) {
	var sb strings.Builder
	reader := bufio.NewReader(body)
	for {
		line, err := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				break
			}
			var parsed struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
				} `json:"choices"`
			}
			if json.Unmarshal([]byte(data), &parsed) == nil && len(parsed.Choices) > 0 {
				delta := parsed.Choices[0].Delta.Content
				if delta != "" {
					sb.WriteString(delta)
					if err := streamWriter(delta); err != nil {
						return "", fmt.Errorf("vision: 写出视觉流失败: %w", err)
					}
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return "", fmt.Errorf("vision: 读取视觉流失败: %w", err)
		}
	}
	if sb.Len() == 0 {
		return "", errors.New("vision: 视觉模型未返回描述文本")
	}
	return sb.String(), nil
}

// readCache 读取描述缓存；未命中或已过期返回 ok=false。
func (s *Service) readCache(key string) (string, bool) {
	path := filepath.Join(s.cacheDir, key+".txt")
	info, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	ttl := time.Duration(config.VisionCacheTTLHours) * time.Hour
	if time.Since(info.ModTime()) > ttl {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(data), true
}

// writeCache 把描述文本写入缓存文件（先建目录，0600 权限）。
func (s *Service) writeCache(key, text string) error {
	if err := os.MkdirAll(s.cacheDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.cacheDir, key+".txt"), []byte(text), 0o600)
}

// md5Hex 返回输入串的 md5 十六进制表示。
func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// chatMessage 视觉请求里的一条消息。
type chatMessage struct {
	Role    string        `json:"role"`
	Content []contentPart `json:"content"`
}

// contentPart 视觉请求 content 数组里的一个分段。
type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

// imageURL 视觉请求里 image_url 分段的对象形态。
type imageURL struct {
	URL string `json:"url"`
}
