package aggregate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"loadout/core/config"
	"loadout/plugins/types"
)

// StartHealthChecker 启动后台健康检查定时任务
func (s *Service) StartHealthChecker() func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.lg.Info("[aggregate] 后台健康检查已启动", "间隔", "3分钟")
	go func() {
		defer close(done)
		ticker := time.NewTicker(3 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.checkAndRecoverModels()
			}
		}
	}()
	return func() { cancel(); <-done }
}

// checkAndRecoverModels 检查并恢复处于冷却状态的模型
func (s *Service) checkAndRecoverModels() {
	s.healthMu.Lock()
	modelsToCheck := make(map[string]*types.ModelHealth)
	for key, health := range s.healthMap {
		if health.Status == "cooling" {
			modelsToCheck[key] = health
		}
	}
	s.healthMu.Unlock()

	if len(modelsToCheck) == 0 {
		s.lg.Debug("[aggregate] 无需检查的模型")
		return
	}

	s.lg.Info("[aggregate] 开始健康检查", "待检查模型数", len(modelsToCheck))

	now := time.Now()
	for key, health := range modelsToCheck {
		// 检查冷却是否结束
		if health.DisabledUntil != nil {
			disabledUntil, err := time.Parse(time.RFC3339, *health.DisabledUntil)
			if err != nil {
				s.lg.Warn("[aggregate] 解析冷却时间失败", "model", key, "err", err)
				continue
			}

			if now.Before(disabledUntil) {
				// 冷却期未结束
				s.lg.Debug("[aggregate] 冷却期未结束，跳过", "model", key, "until", *health.DisabledUntil)
				continue
			}
		}

		// 发送 test 请求
		s.lg.Info("[aggregate] 测试模型可用性", "model", key)
		if ok, checkErr := s.testModelResult(key); ok {
			s.healthMu.Lock()
			if h := s.healthMap[key]; h != nil {
				h.Status = "available"
				h.FailCount = 0
				h.DisabledUntil = nil
				h.LastError = ""
				h.LastChecked = time.Now().Format(time.RFC3339)
			}
			s.healthMu.Unlock()
			s.lg.Info("[aggregate] 模型已恢复", "model", key)
			s.saveHealth()
		} else {
			// 延长冷却时间
			s.healthMu.Lock()
			if h := s.healthMap[key]; h != nil {
				newTime := now.Add(5 * time.Minute).Format(time.RFC3339)
				h.DisabledUntil = &newTime
				if checkErr != nil {
					h.LastError = checkErr.Error()
				} else if h.LastError == "" {
					h.LastError = "健康检查失败：上游请求未成功"
				}
				h.LastChecked = time.Now().Format(time.RFC3339)
			}
			s.healthMu.Unlock()
			s.lg.Warn("[aggregate] 模型恢复失败，延长冷却", "model", key, "new_until", now.Add(5*time.Minute).Format(time.RFC3339))
			s.saveHealth()
		}
	}
}

// testModel 测试模型是否可用（发送一个简单的 hello 请求）
func (s *Service) testModel(modelKey string) bool {
	ok, _ := s.testModelResult(modelKey)
	return ok
}

// testModelResult 测试模型并返回可展示的失败原因。
func (s *Service) testModelResult(modelKey string) (bool, error) {
	// 解析 modelKey: "model@channel_id"
	parts := strings.SplitN(modelKey, "@", 2)
	if len(parts) != 2 {
		s.lg.Error("[aggregate] 无效的 modelKey 格式", "key", modelKey)
		return false, fmt.Errorf("无效的模型标识")
	}
	modelName := parts[0]
	channelID := parts[1]

	s.lg.Debug("[aggregate] 开始测试模型", "model", modelName, "channel", channelID)

	// 读取渠道配置（routing 装配时走 DB，否则回退 JSON）。
	var ch *types.Channel
	if s.routing != nil {
		channels, err := s.routing.ListChannels(context.Background())
		if err != nil {
			s.lg.Error("[aggregate] 读取渠道配置失败", "err", err)
			return false, fmt.Errorf("读取渠道配置失败：%w", err)
		}
		for i := range channels {
			if channels[i].ID != channelID {
				continue
			}
			if !channels[i].ManualEnabled {
				s.lg.Debug("[aggregate] 渠道不可用", "channel", channelID)
				return false, fmt.Errorf("渠道不可用：%s", channelID)
			}
			ch = &types.Channel{
				ID:           channels[i].ID,
				Name:         channels[i].Name,
				BaseURL:      channels[i].BaseURL,
				APIKeyCipher: channels[i].APIKeyCipher,
				Enabled:      channels[i].ManualEnabled,
			}
			break
		}
		if ch == nil {
			s.lg.Debug("[aggregate] 渠道不可用", "channel", channelID)
			return false, fmt.Errorf("渠道不可用：%s", channelID)
		}
	} else {
		var channels []types.Channel
		if err := s.st.Read(types.FileChannels, &channels); err != nil {
			s.lg.Error("[aggregate] 读取渠道配置失败", "err", err)
			return false, fmt.Errorf("读取渠道配置失败：%w", err)
		}
		for i := range channels {
			if channels[i].ID == channelID {
				ch = &channels[i]
				break
			}
		}
		if ch == nil || !ch.Enabled {
			s.lg.Debug("[aggregate] 渠道不可用", "channel", channelID)
			return false, fmt.Errorf("渠道不可用：%s", channelID)
		}
	}

	// 构造测试请求
	testPayload := map[string]any{
		"model": modelName,
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
		"max_tokens": 5,
	}

	body, err := json.Marshal(testPayload)
	if err != nil {
		s.lg.Error("[aggregate] 序列化测试请求失败", "err", err)
		return false, fmt.Errorf("序列化测试请求失败：%w", err)
	}

	// 发送 HTTP 请求
	upstreamURL := strings.TrimRight(ch.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequest(http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		s.lg.Error("[aggregate] 构造测试请求失败", "err", err)
		return false, fmt.Errorf("构造测试请求失败：%w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// 解密 APIKey
	if ch.APIKeyCipher != "" {
		key, err := s.st.Decrypt(ch.APIKeyCipher)
		if err != nil {
			s.lg.Error("[aggregate] 解密 APIKey 失败", "err", err)
			return false, fmt.Errorf("解密 API Key 失败：%w", err)
		}
		req.Header.Set("Authorization", "Bearer "+key)
	}

	client := &http.Client{Timeout: config.UpstreamTimeout}
	resp, err := client.Do(req)
	if err != nil {
		s.lg.Debug("[aggregate] 测试请求失败", "model", modelKey, "err", err)
		return false, fmt.Errorf("请求失败：%w", err)
	}
	defer resp.Body.Close()

	// 检查响应状态
	success := resp.StatusCode >= 200 && resp.StatusCode < 300
	if !success {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		detail := strings.TrimSpace(string(body))
		s.lg.Debug("[aggregate] 测试请求返回错误", "model", modelKey, "status", resp.StatusCode, "body", detail)
		if detail != "" {
			return false, fmt.Errorf("上游返回 HTTP %d：%s", resp.StatusCode, detail)
		}
		return false, fmt.Errorf("上游返回 HTTP %d", resp.StatusCode)
	} else {
		s.lg.Debug("[aggregate] 测试请求成功", "model", modelKey, "status", resp.StatusCode)
	}
	return success, nil
}
