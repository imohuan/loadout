package adminapi

import (
	"net/http"
	"strconv"
	"strings"

	failure "loadout/plugins/failure-rules"
)

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
		s.writeServerError(w, err)
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
		s.writeServerError(w, err)
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
	hit := s.health.VerifyFailureRule(body.Rule, body.Sample)
	writeJSON(w, http.StatusOK, map[string]any{"hit": hit})
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
