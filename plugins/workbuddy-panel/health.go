package workbuddypanel

import (
	"context"
	"database/sql"
	"time"
)

// ModelState 单个「渠道 Key + 模型」的真实健康状态（读 model_states 表）。
type ModelState struct {
	Model              string `json:"model"`
	ManualEnabled      bool   `json:"manual_enabled"`
	Status             string `json:"status"` // available / cooling / disabled
	DisabledUntil      string `json:"disabled_until,omitempty"`
	FailureClass       string `json:"failure_class,omitempty"`
	LastError          string `json:"last_error,omitempty"`
	FailCount          int    `json:"fail_count"`
	LastSuccessAt      string `json:"last_success_at,omitempty"`
	EffectiveAvailable bool   `json:"effective_available"`
	Label              string `json:"label"` // 短标签：可用 / 冷却中 / 额度用尽 / 手动关闭 / 已禁用
}

// ChannelHealth 渠道级健康状态（channel_states 表）。
type ChannelHealth struct {
	Status             string `json:"status"`
	DisabledUntil      string `json:"disabled_until,omitempty"`
	FailureClass       string `json:"failure_class,omitempty"`
	LastError          string `json:"last_error,omitempty"`
	EffectiveAvailable bool   `json:"effective_available"`
}

// loadAllHealth 一次性读出全部渠道的模型健康状态（单查询，避免逐 Key 查询与网关写竞争 SQLite 锁）。
// 返回：channel_id → (model → state)，channel_id → channel 级状态。
func (s *Service) loadAllHealth(ctx context.Context) (map[string]map[string]ModelState, map[string]ChannelHealth, error) {
	out := map[string]map[string]ModelState{}
	chOut := map[string]ChannelHealth{}

	// 渠道级：channels LEFT JOIN channel_states。
	chRows, err := s.db.QueryContext(ctx, `SELECT c.id, c.manual_enabled, COALESCE(cs.status,''), COALESCE(cs.disabled_until,''), COALESCE(cs.last_failure_class,''), COALESCE(cs.last_error,'') FROM channels c LEFT JOIN channel_states cs ON cs.channel_id = c.id`)
	if err != nil {
		return out, chOut, err
	}
	defer chRows.Close()
	chManual := map[string]bool{}
	for chRows.Next() {
		var id string
		var manual int
		var status, until, class, lastErr sql.NullString
		if err := chRows.Scan(&id, &manual, &status, &until, &class, &lastErr); err != nil {
			continue
		}
		chManual[id] = manual == 1
		ch := ChannelHealth{
			Status: status.String, DisabledUntil: until.String,
			FailureClass: class.String, LastError: lastErr.String,
		}
		ch.EffectiveAvailable = chManual[id] && usableHealth(ch.Status, ch.DisabledUntil)
		chOut[id] = ch
	}

	// 模型级：model_states 全表（只有失败过的模型才有行；无行 = 从未失败 = 可用）。
	rows, err := s.db.QueryContext(ctx, `SELECT channel_id, model, manual_enabled, status, COALESCE(disabled_until,''), COALESCE(last_failure_class,''), COALESCE(last_error,''), fail_count, COALESCE(last_success_at,'') FROM model_states`)
	if err != nil {
		return out, chOut, err
	}
	defer rows.Close()
	for rows.Next() {
		var chID string
		var st ModelState
		var manual int
		var status, until, class, lastErr, lastOK sql.NullString
		if err := rows.Scan(&chID, &st.Model, &manual, &status, &until, &class, &lastErr, &st.FailCount, &lastOK); err != nil {
			continue
		}
		st.ManualEnabled = manual == 1
		st.Status = status.String
		st.DisabledUntil = until.String
		st.FailureClass = class.String
		st.LastError = lastErr.String
		st.LastSuccessAt = lastOK.String
		st.EffectiveAvailable = chManual[chID] && st.ManualEnabled && usableHealth(st.Status, st.DisabledUntil)
		st.Label = healthLabel(st)
		if out[chID] == nil {
			out[chID] = map[string]ModelState{}
		}
		out[chID][st.Model] = st
	}
	return out, chOut, rows.Err()
}

// usableHealth 与 model-health.usableStatus 同口径。
func usableHealth(status, disabledUntil string) bool {
	if status == "" || status == "available" {
		return true
	}
	if status != "cooling" || disabledUntil == "" {
		return false
	}
	until, err := time.Parse(time.RFC3339Nano, disabledUntil)
	if err != nil {
		return false
	}
	return !time.Now().Before(until)
}

// healthLabel 短标签，语义与前端 modelStatus.keyStatusBrief 对齐：
// 手动关闭 → 额度用尽 → 冷却中 → 已禁用 → 可用。
func healthLabel(st ModelState) string {
	if !st.ManualEnabled {
		return "手动关闭"
	}
	cls := st.FailureClass
	if containsAny(cls, "quota", "daily") {
		return "额度用尽"
	}
	if st.Status == "cooling" {
		return "冷却中"
	}
	if st.Status == "disabled" {
		return "已禁用"
	}
	return "可用"
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if sub != "" && len(s) >= len(sub) {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
