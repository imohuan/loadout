package adminapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	failure "loadout/plugins/failure-rules"
)

// writeRuleWriteError 规则写入失败的统一收口。
//
// 用户输入问题（ErrInvalidRule）回 400 + 具体原因，让前端能直接提示
// 「正则不合法：…」；其余才是 500。
func (s *Service) writeRuleWriteError(w http.ResponseWriter, err error) {
	var invalid failure.ErrInvalidRule
	if errors.As(err, &invalid) {
		writeError(w, http.StatusBadRequest, invalid.Error())
		return
	}
	s.writeServerError(w, err)
}

// ==== 失败规则引擎 HTTP API（前端「失败规则」页） ====

// handleFailureRulesList GET /api/failure-rules
func (s *Service) handleFailureRulesList(w http.ResponseWriter, r *http.Request) {
	if s.health == nil {
		writeError(w, http.StatusServiceUnavailable, "model-health 未装配")
		return
	}
	rules, err := s.health.ListFailureRules(r.Context())
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	if rules == nil {
		rules = []failure.Rule{}
	}
	writeJSON(w, http.StatusOK, rules)
}

// handleFailureRuleCreate POST /api/failure-rules
func (s *Service) handleFailureRuleCreate(w http.ResponseWriter, r *http.Request) {
	if s.health == nil {
		writeError(w, http.StatusServiceUnavailable, "model-health 未装配")
		return
	}
	var in failure.RuleInput
	if !decodeJSON(w, r, &in) {
		return
	}
	rule, err := s.health.CreateFailureRule(r.Context(), in)
	if err != nil {
		s.writeRuleWriteError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rule)
}

// handleFailureRuleUpdate PUT /api/failure-rules/{id}
func (s *Service) handleFailureRuleUpdate(w http.ResponseWriter, r *http.Request) {
	if s.health == nil {
		writeError(w, http.StatusServiceUnavailable, "model-health 未装配")
		return
	}
	var in failure.RuleInput
	if !decodeJSON(w, r, &in) {
		return
	}
	rule, err := s.health.UpdateFailureRule(r.Context(), r.PathValue("id"), in)
	if err != nil {
		s.writeRuleWriteError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rule)
}

// handleFailureRuleDelete DELETE /api/failure-rules/{id}
func (s *Service) handleFailureRuleDelete(w http.ResponseWriter, r *http.Request) {
	if s.health == nil {
		writeError(w, http.StatusServiceUnavailable, "model-health 未装配")
		return
	}
	if err := s.health.DeleteFailureRule(r.Context(), r.PathValue("id")); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleFailureRulePatch PATCH /api/failure-rules/{id}（启停 / 确认草稿）
func (s *Service) handleFailureRulePatch(w http.ResponseWriter, r *http.Request) {
	if s.health == nil {
		writeError(w, http.StatusServiceUnavailable, "model-health 未装配")
		return
	}
	var body struct {
		Enabled *bool `json:"enabled,omitempty"`
		Confirm bool  `json:"confirm,omitempty"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	id := r.PathValue("id")
	if body.Confirm {
		if err := s.health.ConfirmFailureRule(r.Context(), id); err != nil {
			s.writeServerError(w, err)
			return
		}
	}
	if body.Enabled != nil {
		if err := s.health.SetFailureRuleEnabled(r.Context(), id, *body.Enabled); err != nil {
			s.writeServerError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleFailureRuleVerify POST /api/failure-rules/verify（样本校验 dry-run）
func (s *Service) handleFailureRuleVerify(w http.ResponseWriter, r *http.Request) {
	if s.health == nil {
		writeError(w, http.StatusServiceUnavailable, "model-health 未装配")
		return
	}
	var body struct {
		Rule   failure.Rule     `json:"rule"`
		Sample failure.Evidence `json:"sample"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	// 返回完整明细：命中与否 + 命中后动作的参数（恢复时间/冷却秒数）。
	// 用户要求：样本校验里能看到「提取的时间用到了动作中」——恢复到几点。
	detail := s.health.VerifyFailureRuleDetail(body.Rule, body.Sample)
	writeJSON(w, http.StatusOK, detail)
}

// handleFailureRulesRestoreDefaults POST /api/failure-rules-defaults/restore
// 把 14 条内置默认规则恢复成出厂状态（用户删掉/改坏后一键还原）。
// 用户自建的 rule-*/ai-* 规则不受影响。
func (s *Service) handleFailureRulesRestoreDefaults(w http.ResponseWriter, r *http.Request) {
	if s.health == nil {
		writeError(w, http.StatusServiceUnavailable, "model-health 未装配")
		return
	}
	n, err := s.health.RestoreDefaultFailureRules(r.Context())
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "restored": n})
}

// handleRuleDecisionsList GET /api/rule-decisions?limit=50（AI/规则路由日志）
func (s *Service) handleRuleDecisionsList(w http.ResponseWriter, r *http.Request) {
	if s.health == nil {
		writeError(w, http.StatusServiceUnavailable, "model-health 未装配")
		return
	}
	limit := 50
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = v
	}
	items, err := s.health.ListRuleDecisions(r.Context(), limit)
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// handleProviderFrameworks GET /api/provider-frameworks
// 返回规则作用域选择器数据：去重框架列表 + 平台（base_url 组）清单。
func (s *Service) handleProviderFrameworks(w http.ResponseWriter, r *http.Request) {
	channels, err := s.listDBChannels(r.Context())
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	seenURL := map[string]bool{}
	type platform struct {
		BaseURL   string `json:"base_url"`
		Name      string `json:"name"`
		Framework string `json:"framework"`
	}
	platforms := []platform{}
	frameworkSet := map[string]bool{}
	for _, ch := range channels {
		u := strings.TrimRight(ch.BaseURL, "/")
		if u == "" || seenURL[u] {
			continue
		}
		seenURL[u] = true
		fw := strings.TrimSpace(ch.Framework)
		if fw != "" {
			frameworkSet[fw] = true
		}
		platforms = append(platforms, platform{BaseURL: u, Name: ch.ChannelName, Framework: fw})
	}
	frameworks := make([]string, 0, len(frameworkSet))
	for fw := range frameworkSet {
		frameworks = append(frameworks, fw)
	}
	writeJSON(w, http.StatusOK, map[string]any{"frameworks": frameworks, "platforms": platforms})
}
