package aggregate

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// 失败规则映射表
var failureRules = []struct {
	pattern  *regexp.Regexp
	strategy types.FailureStrategy
}{
	// HTTP 状态码规则
	{regexp.MustCompile(`(?i)429|rate.?limit`), types.FailureStrategy{Action: "cooldown", CooldownTime: 5, Reason: "rate_limit_exceeded"}},
	{regexp.MustCompile(`(?i)503|service.unavailable|overload`), types.FailureStrategy{Action: "cooldown", CooldownTime: 2, Reason: "service_unavailable"}},
	{regexp.MustCompile(`(?i)401|unauthorized|invalid.api.key`), types.FailureStrategy{Action: "disable", Reason: "invalid_api_key"}},
	{regexp.MustCompile(`(?i)402|insufficient.quota|balance|payment`), types.FailureStrategy{Action: "disable", Reason: "insufficient_quota"}},
	{regexp.MustCompile(`(?i)context.length|too.long|maximum.context`), types.FailureStrategy{Action: "skip", Reason: "context_length_exceeded"}},

	// 通用错误
	{regexp.MustCompile(`(?i)timeout`), types.FailureStrategy{Action: "cooldown", CooldownTime: 1, Reason: "timeout"}},
	{regexp.MustCompile(`(?i)connection.refused|network`), types.FailureStrategy{Action: "cooldown", CooldownTime: 1, Reason: "network_error"}},
}

// analyzeFailure 分析失败原因并返回处理策略（旧 chat 管线版）。
func (s *Service) analyzeFailure(failure *modelgateway.FailurePayload) types.FailureStrategy {
	return s.analyzeFailureText(failure.StatusCode, failure.ErrorBody, errorTextOf(failure.Error))
}

// analyzeProxyFailure 分析失败原因并返回处理策略（透明代理版）。
func (s *Service) analyzeProxyFailure(failure *modelgateway.ProxyFailurePayload) types.FailureStrategy {
	return s.analyzeFailureText(failure.StatusCode, failure.ErrorBody, errorTextOf(failure.Error))
}

func errorTextOf(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// analyzeFailureText 按状态码+错误体+错误文本匹配策略的公共核心。
func (s *Service) analyzeFailureText(statusCode int, errorBody, errText string) types.FailureStrategy {
	// 1. 规则匹配
	combinedText := fmt.Sprintf("%d %s %s", statusCode, errText, errorBody)

	for _, rule := range failureRules {
		if rule.pattern.MatchString(combinedText) {
			s.lg.Debug("匹配到规则", "pattern", rule.pattern.String(), "action", rule.strategy.Action)
			return rule.strategy
		}
	}

	// 2. 解析 OpenAI 标准错误格式
	if errorBody != "" {
		var errResp struct {
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
				Code    string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(errorBody), &errResp); err == nil {
			errorType := strings.ToLower(errResp.Error.Type + " " + errResp.Error.Code + " " + errResp.Error.Message)

			for _, rule := range failureRules {
				if rule.pattern.MatchString(errorType) {
					s.lg.Debug("匹配到 OpenAI 错误类型", "type", errResp.Error.Type, "action", rule.strategy.Action)
					return rule.strategy
				}
			}
		}
	}

	// 3. AI 兜底（未知错误）
	s.lg.Info("未匹配到规则，使用 AI 分析", "status", statusCode, "error", errText)
	return s.analyzeByAI(statusCode, errText)
}

// analyzeByAI 使用小模型分析未知错误（AI 兜底）。
func (s *Service) analyzeByAI(statusCode int, errText string) types.FailureStrategy {
	// TODO: 实现 AI 分析
	// 1. 构造 prompt
	// 2. 调用 gemini-flash 或 gpt-4o-mini
	// 3. 解析返回的 JSON 策略

	// 暂时返回默认策略：临时冷却
	s.lg.Warn("AI 分析功能尚未实现，使用默认策略")
	return types.FailureStrategy{
		Action:       "cooldown",
		CooldownTime: 3,
		Reason:       "unknown_error",
	}
}
