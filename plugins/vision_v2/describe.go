package visionv2

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"loadout/core/config"
	"loadout/core/db"
	"loadout/plugins/contracts"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// DescribeImage 按 id 识别图片：缓存(md5(id|prompt|v3))命中直接返回 (text, true, nil)；
// miss 时读本地文件、压缩、调视觉模型（viaModel 为该渠道绑定的视觉模型名），写缓存后返回 (text, false, nil)。
// streamWriter 非 nil 时识别过程实时输出（SSE reasoning delta）。
// ch 为已解析的视觉渠道（baseURL/apiKey）。
func (s *Service) DescribeImage(ctx context.Context, id, prompt string, streamWriter func(string) error, ch modelgateway.ResolvedChannel, viaModel string) (string, bool, error) {
	key := visionCacheKey(id, prompt)
	if config.VisionCacheEnabled {
		if text, ok := s.readCache(key); ok {
			return text, true, nil
		}
	}
	raw, mime, err := s.loadImageBytes(id)
	if err != nil {
		return "", false, err
	}
	dataURI := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw)
	if config.VisionCompressEnabled {
		if c, err := CompressDataURI(dataURI); err == nil {
			dataURI = c
		} else if !errors.Is(err, ErrImageTooLarge) {
			s.lg.Warn("图片压缩失败，使用原图", "id", id, "err", err)
		}
	}
	text, err := s.callVision(ctx, dataURI, prompt, ch, viaModel, streamWriter)
	if err != nil {
		return "", false, err
	}
	if config.VisionCacheEnabled {
		_ = s.writeCache(key, text)
	}
	s.cleanupStaleFiles() // 懒清理孤儿图片
	return text, false, nil
}

// describeWithFailover 按路由 via_options 依次尝试视觉候选（渠道展开 + 各自 viaModel），
// 任一成功返回；全部失败返回最后错误。缓存命中直接返回（不调模型、不进 failover）。
// 每次候选尝试写 route-log（visionAttempt：running → success/failed），与旧 vision 一致。
func (s *Service) describeWithFailover(ctx context.Context, id, prompt string, streamWriter func(string) error, route *types.CapabilityRoute, requestID string) (string, error) {
	key := visionCacheKey(id, prompt)
	if config.VisionCacheEnabled {
		if text, ok := s.readCache(key); ok {
			return text, nil
		}
	}
	var options []types.ViaOption
	if route != nil {
		options = route.ViaOptions
	}
	if len(options) == 0 {
		options = []types.ViaOption{{ViaModel: config.DefaultVisionModel}}
	}
	channels := s.loadChannels(ctx)
	var lastErr error
	for _, opt := range options {
		viaModel := opt.ViaModel
		if viaModel == "" {
			viaModel = config.DefaultVisionModel
		}
		keys := modelgateway.ExpandCandidateKeys(opt.ChannelID, opt.ChannelIDs, opt.ChannelBaseURL, channels)
		if len(keys) == 0 {
			if opt.ChannelBaseURL != "" || len(opt.ChannelIDs) > 0 {
				lastErr = fmt.Errorf("vision_v2: 视觉候选 %q 无可用渠道（候选 Key 为空）", viaModel)
				continue
			}
			// 无指定渠道：自动路由（复制旧 channelsForModel 语义）
			rcs, err := s.channelsForModel(ctx, viaModel)
			if err != nil {
				lastErr = err
				continue
			}
			for _, rc := range rcs {
				start := time.Now()
				text, _, err := s.DescribeImage(ctx, id, prompt, streamWriter, rc, viaModel)
				if err == nil {
					s.visionAttempt(ctx, requestID, -1, viaModel, start, time.Since(start), "success", rc.ID, "", 1)
					return text, nil
				}
				s.visionAttempt(ctx, requestID, -1, viaModel, start, time.Since(start), "failed", rc.ID, err.Error(), 1)
				lastErr = err
			}
			continue
		}
		var keyErr error
		for _, k := range keys {
			rc, err := s.resolveChannel(ctx, k.ChannelID)
			if err != nil {
				keyErr = err
				continue
			}
			start := time.Now()
			text, _, err := s.DescribeImage(ctx, id, prompt, streamWriter, *rc, viaModel)
			if err == nil {
				s.visionAttempt(ctx, requestID, -1, viaModel, start, time.Since(start), "success", rc.ID, "", 1)
				return text, nil
			}
			s.visionAttempt(ctx, requestID, -1, viaModel, start, time.Since(start), "failed", rc.ID, err.Error(), 1)
			s.lg.Warn("视觉候选 Key 失败，尝试下一个", "via_model", viaModel, "channel", k.ChannelID, "err", err)
			keyErr = err
		}
		lastErr = keyErr
	}
	return "", lastErr
}

// loadChannels 读取全部渠道（repo；复制旧 vision service.go:387）。
func (s *Service) loadChannels(ctx context.Context) []db.Channel {
	if s.repo == nil {
		return nil
	}
	channels, err := s.repo.ListChannels(ctx)
	if err != nil {
		s.lg.Warn("vision_v2: 读取渠道表失败", "err", err)
		return nil
	}
	return channels
}

// channelsForModel 按模型名解析渠道（SQLite）：已知模型（渠道 Models 精确包含）
// 优先，未知模型（渠道 Models 为空）兜底；与 modelgateway 主路由语义一致。
// 复制旧 vision service.go:428（含 channelHasModel 辅助）。
func (s *Service) channelsForModel(ctx context.Context, model string) ([]modelgateway.ResolvedChannel, error) {
	if s.repo == nil {
		return nil, fmt.Errorf("vision_v2: 渠道仓储未初始化")
	}
	channels, err := s.repo.ListChannels(ctx)
	if err != nil {
		return nil, fmt.Errorf("vision_v2: 读取渠道表失败: %w", err)
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

// callVision 单渠道调用视觉模型（chat/completions 格式，独立于主请求格式）。
// viaModel 为该渠道绑定的视觉模型名（来自路由 via_options）；空时兜底 config.DefaultVisionModel。
func (s *Service) callVision(ctx context.Context, dataURI, prompt string, ch modelgateway.ResolvedChannel, viaModel string, streamWriter func(string) error) (string, error) {
	if viaModel == "" {
		viaModel = config.DefaultVisionModel
	}
	p := config.VisionDescriptionPrompt
	if strings.TrimSpace(p) == "" {
		p = builtinVisionPrompt
	}
	payload := struct {
		Model    string        `json:"model"`
		Messages []chatMessage `json:"messages"`
		Stream   bool          `json:"stream"`
	}{
		Model: viaModel,
		Messages: []chatMessage{{Role: "user", Content: []contentPart{
			{Type: "image_url", ImageURL: &imageURL{URL: dataURI}},
			{Type: "text", Text: "识别方向: " + prompt + "\n\n" + p},
		}}},
		Stream: streamWriter != nil,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("vision_v2: 序列化视觉请求失败: %w", err)
	}
	target := strings.TrimRight(ch.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if ch.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+ch.APIKey)
	}
	client := &http.Client{Timeout: config.VisionTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("vision_v2: 视觉模型返回错误(%d): %s", resp.StatusCode, string(b))
	}
	if streamWriter != nil {
		return readVisionStream(resp.Body, streamWriter)
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", err
	}
	if len(parsed.Choices) == 0 || parsed.Choices[0].Message.Content == "" {
		return "", errors.New("vision_v2: 视觉模型未返回描述文本")
	}
	return parsed.Choices[0].Message.Content, nil
}

// 复制的辅助类型（从旧 vision/service.go）：chatMessage、contentPart、imageURL、
// readVisionStream、visionAttempt、pointerTime（route-log 用）。
// visionAttempt 签名参考旧版，本任务实现，Task 7 使用。

// visionAttempt 写一条视觉识别 attempt 到 route-log（两阶段）：
// 识别开始调一次（Result=running 占位），识别结束以同一 step_no 再调一次更新状态。
// 视觉 attempt 固定用 stepNo=-1（不占主链路 step 空间，与旧 vision 模式一致）。
// routeLog 为 nil（测试/旧管线）或 stepNo == 0 时静默跳过。
func (s *Service) visionAttempt(ctx context.Context, requestID string, stepNo int, model string, startedAt time.Time, dur time.Duration, result, channelID, errMsg string, imageCount int) {
	if s.routeLog == nil || requestID == "" || stepNo == 0 {
		return
	}
	if _, err := s.routeLog.Attempt(ctx, contracts.RouteAttempt{
		RequestID:    requestID,
		StepNo:       stepNo,
		Action:       "视觉识别",
		Model:        model,
		ChannelID:    channelID,
		StartedAt:    startedAt,
		FinishedAt:   pointerTime(startedAt.Add(dur)),
		Result:       result,
		ErrorMessage: errMsg,
		Duration:     contracts.DurationMS(dur),
		Metadata:     map[string]any{"capability": "vision", "image_count": imageCount},
	}); err != nil {
		s.lg.Warn("route log vision attempt failed", "err", err)
	}
}

// readVisionStream 解析视觉模型的 SSE 流：累积 content delta，并实时通过 streamWriter 输出。
func readVisionStream(body io.Reader, streamWriter func(string) error) (string, error) {
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
						return "", fmt.Errorf("vision_v2: 写出视觉流失败: %w", err)
					}
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return "", fmt.Errorf("vision_v2: 读取视觉流失败: %w", err)
		}
	}
	if sb.Len() == 0 {
		return "", errors.New("vision_v2: 视觉模型未返回描述文本")
	}
	return sb.String(), nil
}

// pointerTime 返回 time.Time 的指针（RouteAttempt.FinishedAt 需要）。
func pointerTime(t time.Time) *time.Time { return &t }

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
