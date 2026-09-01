package multimodalmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 火山方舟 Files API 上传轮询的默认参数（测试可覆盖以加速/模拟超时）。
var (
	// uploadPollInterval 轮询文件状态的时间间隔。
	uploadPollInterval = time.Second
	// uploadPollTimeout 从上传成功到文件状态变 active 的总等待上限。
	uploadPollTimeout = 60 * time.Second
	// arkHTTPClient 方舟 Files API 的 HTTP 客户端（测试可替换以重定向到 mock server）。
	arkHTTPClient = http.DefaultClient
)

// arkFileStatus 文件上传/检索响应里的状态字段。
type arkFileStatus string

const (
	arkStatusProcessing arkFileStatus = "processing" // 处理中（需轮询）
	arkStatusActive     arkFileStatus = "active"     // 就绪，可用 file_id 引用
)

// arkUploadResp POST /v3/files 的响应体（只取需要的字段）。
type arkUploadResp struct {
	ID     string        `json:"id"`
	Status arkFileStatus `json:"status"`
}

// arkRetrieveResp GET /v3/files/{id} 的响应体。
type arkRetrieveResp struct {
	ID     string        `json:"id"`
	Status arkFileStatus `json:"status"`
}

// uploadAndGetID 把大文件上传到火山方舟 Files API，返回 file_id。
// 识别层在本地文件超过 base64 阈值时调用本方法（已读取文件字节并带上媒体类型与文件名）：
// 从渠道列表按 Base URL 识别方舟平台取 key，调 POST /v3/files 上传（multipart，
// purpose=user_data），随后轮询文件状态到 active。多个方舟 key 依次尝试（failover）。
// 识别层在收到错误后决定降级（base64 或直接报错）。
func (s *Service) uploadAndGetID(ctx context.Context, mediaType string, data []byte, filename string) (string, error) {
	if len(data) == 0 {
		return "", errors.New("multimodal-mcp: 待上传文件内容为空")
	}

	// 1. 渠道识别：按 Base URL 找启用的方舟平台渠道（含解密后的 key 与 API 地址）。
	arks, err := s.arkChannels(ctx)
	if err != nil {
		return "", err
	}
	if len(arks) == 0 {
		return "", errors.New("multimodal-mcp: 未找到启用的方舟（volces.com）渠道，无法上传大文件")
	}

	// 2. 逐个方舟 key 尝试上传 + 轮询（failover）。
	var lastErr error
	for _, a := range arks {
		fileID, err := s.uploadAndPoll(ctx, a, data, mediaType, filename)
		if err == nil {
			return fileID, nil
		}
		lastErr = err
		s.lg.Warn("multimodal-mcp: 方舟渠道上传失败，尝试下一个", "base", a.apiBase, "err", err)
	}
	return "", fmt.Errorf("multimodal-mcp: 所有方舟渠道上传均失败: %w", lastErr)
}

// arkChannel 一个可用的方舟渠道：API 地址 + 解密后的 API key。
type arkChannel struct {
	apiBase string
	apiKey  string
}

// arkChannels 遍历渠道表，按 Base URL 识别方舟平台，返回所有启用的方舟渠道。
// 识别规则：解析 ch.BaseURL 的 host，以 ".volces.com" 结尾即判定为方舟平台
// （覆盖 ark.cn-beijing.volces.com 及后续其它 region 前缀）。key 用 AES 解密。
func (s *Service) arkChannels(ctx context.Context) ([]arkChannel, error) {
	if s.repo == nil {
		return nil, errors.New("multimodal-mcp: 渠道仓储未初始化")
	}
	channels, err := s.repo.ListChannels(ctx)
	if err != nil {
		return nil, fmt.Errorf("multimodal-mcp: 读取渠道表失败: %w", err)
	}
	var out []arkChannel
	for _, ch := range channels {
		apiBase, ok := arkAPIBase(ch.BaseURL)
		if !ok {
			continue // 非方舟平台
		}
		if !ch.ManualEnabled {
			continue // 渠道被手动停用
		}
		key, err := s.st.Decrypt(ch.APIKeyCipher)
		if err != nil {
			s.lg.Warn("multimodal-mcp: 解密方舟渠道 key 失败，跳过", "channel", ch.ID, "err", err)
			continue
		}
		if key == "" {
			continue
		}
		out = append(out, arkChannel{apiBase: apiBase, apiKey: key})
	}
	return out, nil
}

// arkAPIBase 解析渠道 BaseURL，判断是否为方舟平台并返回其 API 根地址。
// 返回 (apiBase, true) 表示是方舟渠道；非方舟返回 ("", false)。
// 用 net/url 解析 host（而非仅 NormalizeBaseURL 去尾斜杠），
// 以 ".volces.com" 结尾判定方舟；API 根地址取 scheme+host+"/api/v3"。
func arkAPIBase(baseURL string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" {
		return "", false
	}
	if !strings.HasSuffix(strings.ToLower(u.Host), ".volces.com") {
		return "", false
	}
	scheme := u.Scheme
	if scheme == "" {
		scheme = "https"
	}
	apiBase := scheme + "://" + u.Host + "/api/v3"
	// 若 BaseURL 本身就是完整 /api/v3 地址，去掉可能的重复拼接。
	return strings.TrimSuffix(apiBase, "/api/v3/api/v3"), true
}

// uploadAndPoll 用单个方舟渠道完成：上传拿 file_id → 轮询状态到 active。
func (s *Service) uploadAndPoll(ctx context.Context, a arkChannel, data []byte, mediaType, filename string) (string, error) {
	fileID, err := s.uploadFile(ctx, a, data, mediaType, filename)
	if err != nil {
		return "", err
	}
	if err := s.waitActive(ctx, a, fileID); err != nil {
		return "", err
	}
	return fileID, nil
}

// uploadFile 调 POST {apiBase}/files 上传文件，返回响应里的 file_id。
func (s *Service) uploadFile(ctx context.Context, a arkChannel, data []byte, mediaType, filename string) (string, error) {
	if filename == "" {
		filename = "upload.bin"
	}
	// 构造 multipart/form-data：purpose=user_data + file=<字节>。
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("purpose", "user_data"); err != nil {
		return "", fmt.Errorf("multimodal-mcp: 写 purpose 字段失败: %w", err)
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return "", fmt.Errorf("multimodal-mcp: 创建 file 字段失败: %w", err)
	}
	// mediaType（识别层传入的 MIME）暂不参与 multipart 头改写——CreateFormFile 固定
	// application/octet-stream，文件名扩展名即可让方舟识别类型；此处保留参数用于
	// 潜在扩展（如后续需要显式 Content-Type 时）。
	_ = mediaType
	if _, err := part.Write(data); err != nil {
		return "", fmt.Errorf("multimodal-mcp: 写文件字节失败: %w", err)
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("multimodal-mcp: 关闭 multipart 失败: %w", err)
	}

	uploadURL := strings.TrimRight(a.apiBase, "/") + "/files"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, &body)
	if err != nil {
		return "", fmt.Errorf("multimodal-mcp: 构造上传请求失败: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+a.apiKey)

	resp, err := arkHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("multimodal-mcp: 上传请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("multimodal-mcp: 上传失败，HTTP %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	var ar arkUploadResp
	if err := json.Unmarshal(raw, &ar); err != nil {
		return "", fmt.Errorf("multimodal-mcp: 解析上传响应失败: %w", err)
	}
	if ar.ID == "" {
		return "", errors.New("multimodal-mcp: 上传响应缺少 file_id")
	}
	return ar.ID, nil
}

// waitActive 轮询 GET {apiBase}/files/{file_id}，直到状态 active；超时返回错误。
func (s *Service) waitActive(ctx context.Context, a arkChannel, fileID string) error {
	retrieveURL := strings.TrimRight(a.apiBase, "/") + "/files/" + url.PathEscape(fileID)
	deadline := time.Now().Add(uploadPollTimeout)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("multimodal-mcp: 轮询文件 %s 状态超时（%s）", fileID, uploadPollTimeout)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, retrieveURL, nil)
		if err != nil {
			return fmt.Errorf("multimodal-mcp: 构造轮询请求失败: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+a.apiKey)
		resp, err := arkHTTPClient.Do(req)
		if err != nil {
			return fmt.Errorf("multimodal-mcp: 轮询请求失败: %w", err)
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("multimodal-mcp: 轮询状态失败，HTTP %d: %s", resp.StatusCode, truncate(string(raw), 300))
		}
		var rr arkRetrieveResp
		if err := json.Unmarshal(raw, &rr); err != nil {
			return fmt.Errorf("multimodal-mcp: 解析轮询响应失败: %w", err)
		}
		switch rr.Status {
		case arkStatusActive:
			return nil
		case arkStatusProcessing:
			// 继续等待
		default:
			return fmt.Errorf("multimodal-mcp: 文件 %s 状态异常: %q", fileID, rr.Status)
		}
		select {
		case <-time.After(uploadPollInterval):
		case <-ctx.Done():
			return fmt.Errorf("multimodal-mcp: 轮询取消: %w", ctx.Err())
		}
	}
}

// truncate 截断长文本，避免错误信息里塞入超长响应体。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
