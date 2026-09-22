package adminapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	failure "loadout/plugins/failure-rules"
)

// ==== 样本回放 / AI 生成规则（「回撤」）HTTP 接口 ====

// handleRuleSamplesList GET /api/rule-samples?source=&model=&limit=
func (s *Service) handleRuleSamplesList(w http.ResponseWriter, r *http.Request) {
	if s.health == nil {
		writeError(w, http.StatusServiceUnavailable, "model-health 未装配")
		return
	}
	limit := 500
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = v
	}
	items, err := s.health.ListRuleSamples(r.Context(), r.URL.Query().Get("source"), r.URL.Query().Get("model"), limit)
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// handleRuleSamplesImport POST /api/rule-samples/import  body: {limit}
// 把历史判定日志导入样本库（按指纹去重）。
func (s *Service) handleRuleSamplesImport(w http.ResponseWriter, r *http.Request) {
	if s.health == nil {
		writeError(w, http.StatusServiceUnavailable, "model-health 未装配")
		return
	}
	var body struct {
		Limit int `json:"limit"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	res, err := s.health.ImportRuleSamples(r.Context(), body.Limit)
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	// 回带 scanned/merged/truncated，让前端能解释「为什么只多了几条」。
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"imported":  res.Inserted,
		"total":     res.Total,
		"scanned":   res.Scanned,
		"merged":    res.Merged,
		"limit":     res.Limit,
		"truncated": res.Truncated,
	})
}

// handleRuleSampleCreate POST /api/rule-samples  新增构造样本。
func (s *Service) handleRuleSampleCreate(w http.ResponseWriter, r *http.Request) {
	if s.health == nil {
		writeError(w, http.StatusServiceUnavailable, "model-health 未装配")
		return
	}
	var sm failure.Sample
	if !decodeJSON(w, r, &sm) {
		return
	}
	created, err := s.health.CreateRuleSample(r.Context(), sm)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, created)
}

// handleRuleSamplePatch PATCH /api/rule-samples/{id} body: {expected_verdict, confirmed}
func (s *Service) handleRuleSamplePatch(w http.ResponseWriter, r *http.Request) {
	if s.health == nil {
		writeError(w, http.StatusServiceUnavailable, "model-health 未装配")
		return
	}
	var body struct {
		ExpectedVerdict string `json:"expected_verdict"`
		Confirmed       bool   `json:"confirmed"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := s.health.SetRuleSampleExpectation(r.Context(), r.PathValue("id"), body.ExpectedVerdict, body.Confirmed); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleRuleSampleDelete DELETE /api/rule-samples/{id}
func (s *Service) handleRuleSampleDelete(w http.ResponseWriter, r *http.Request) {
	if s.health == nil {
		writeError(w, http.StatusServiceUnavailable, "model-health 未装配")
		return
	}
	if err := s.health.DeleteRuleSample(r.Context(), r.PathValue("id")); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleRuleSamplesReplay POST /api/rule-samples/replay body: {ids: []}
// 批量回放：对样本跑规则，返回通过/未命中/不一致的结果表。
func (s *Service) handleRuleSamplesReplay(w http.ResponseWriter, r *http.Request) {
	if s.health == nil {
		writeError(w, http.StatusServiceUnavailable, "model-health 未装配")
		return
	}
	var body struct {
		IDs []string `json:"ids"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	sum, err := s.health.ReplayRuleSamples(r.Context(), body.IDs)
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

// handleRuleSampleAuthor POST /api/rule-samples/author body: {sample_id}
// 启动 AI 多轮生成规则（异步），立即返回会话供前端轮询进度。
func (s *Service) handleRuleSampleAuthor(w http.ResponseWriter, r *http.Request) {
	if s.health == nil {
		writeError(w, http.StatusServiceUnavailable, "model-health 未装配")
		return
	}
	var body struct {
		SampleID string `json:"sample_id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.SampleID) == "" {
		writeError(w, http.StatusBadRequest, "sample_id 必填")
		return
	}
	sess, err := s.health.AuthorRuleFromSample(r.Context(), body.SampleID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

// handleRuleAuthorSessions GET /api/rule-author-sessions?limit=50
func (s *Service) handleRuleAuthorSessions(w http.ResponseWriter, r *http.Request) {
	if s.health == nil {
		writeError(w, http.StatusServiceUnavailable, "model-health 未装配")
		return
	}
	limit := 50
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = v
	}
	items, err := s.health.ListRuleAuthorSessions(r.Context(), limit)
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// handleRuleAuthorSession GET /api/rule-author-sessions/{id}
func (s *Service) handleRuleAuthorSession(w http.ResponseWriter, r *http.Request) {
	if s.health == nil {
		writeError(w, http.StatusServiceUnavailable, "model-health 未装配")
		return
	}
	sess, err := s.health.GetRuleAuthorSession(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sess)
}
