package adminapi

import (
	"math"
	"testing"
	"time"

	"loadout/plugins/contracts"
)

// mkView 构造一条 RouteRequestView 测试数据。
func mkView(result, finalModel string, duration time.Duration, prompt, completion, cached int, startedAt time.Time) contracts.RouteRequestView {
	return contracts.RouteRequestView{
		RequestID:        "req-" + result + finalModel,
		RequestedModel:   "requested-" + finalModel,
		StartedAt:        startedAt,
		Result:           result,
		FinalModel:       finalModel,
		Duration:         contracts.DurationMS(duration),
		PromptTokens:     prompt,
		CompletionTokens: completion,
		CachedTokens:     cached,
	}
}

// fixedNowUTC 全部测试共用固定基准时间（UTC 2026-08-15 06:00），
// 避免依赖墙钟，便于断言 date key / trend 终点。
var fixedNowUTC = time.Date(2026, 8, 15, 6, 0, 0, 0, time.UTC)

func almostEqual(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// TestAggregateModelStatsEmpty 空数据：summary 全 0、success_rate=0、hit_rate 全 0、
// trend 补满 days 天全 0、calendar 空、model_dist 空。
func TestAggregateModelStatsEmpty(t *testing.T) {
	for name, logs := range map[string][]contracts.RouteRequestView{
		"nil":   nil,
		"empty": {},
	} {
		t.Run(name, func(t *testing.T) {
			out := aggregateModelStats(logs, 30, time.UTC, fixedNowUTC)
			s := out.Summary
			if s.Requests != 0 || s.PromptTokens != 0 || s.CompletionTokens != 0 ||
				s.CachedTokens != 0 || s.TotalTokens != 0 || s.Failed != 0 {
				t.Fatalf("summary 应全 0，got %+v", s)
			}
			if s.SuccessRate != 0 || s.AvgDurationMS != 0 {
				t.Fatalf("success_rate/avg_duration_ms 应为 0，got %+v", s)
			}
			if out.HitRate.Input != 0 || out.HitRate.Output != 0 || out.HitRate.Total != 0 {
				t.Fatalf("hit_rate 应全 0，got %+v", out.HitRate)
			}
			if len(out.Trend) != 30 {
				t.Fatalf("trend 长度应补满 30，got %d", len(out.Trend))
			}
			for _, day := range out.Trend {
				if day.Requests != 0 || day.TotalTokens != 0 {
					t.Fatalf("空数据 trend 天应全 0，got %+v", day)
				}
			}
			if len(out.Calendar) != 0 {
				t.Fatalf("calendar 应为空，got %+v", out.Calendar)
			}
			if len(out.ModelDist) != 0 {
				t.Fatalf("model_dist 应为空，got %+v", out.ModelDist)
			}
		})
	}
}

// TestAggregateModelStatsBasic 基本聚合：success + 非 success、不同日期、不同 final_model、
// 有 cached_tokens → 校验 summary、hit_rate、model_dist 排序。
func TestAggregateModelStatsBasic(t *testing.T) {
	now := fixedNowUTC
	yesterday := now.AddDate(0, 0, -1)
	logs := []contracts.RouteRequestView{
		mkView("success", "gpt-4o", 120*time.Millisecond, 1000, 200, 400, now),
		mkView("error", "gpt-4o-mini", 80*time.Millisecond, 500, 100, 50, yesterday),
		mkView("success", "gpt-4o", 60*time.Millisecond, 100, 0, 0, now),
	}

	out := aggregateModelStats(logs, 30, time.UTC, now)

	s := out.Summary
	if s.Requests != 3 || s.PromptTokens != 1600 || s.CompletionTokens != 300 ||
		s.CachedTokens != 450 || s.TotalTokens != 1900 || s.Failed != 1 {
		t.Fatalf("summary 数值不符，got %+v", s)
	}
	if !almostEqual(s.SuccessRate, 2.0/3.0) {
		t.Fatalf("success_rate 应为 2/3，got %v", s.SuccessRate)
	}
	if !almostEqual(s.AvgDurationMS, 260.0/3.0) {
		t.Fatalf("avg_duration_ms 应为 260/3，got %v", s.AvgDurationMS)
	}
	if !almostEqual(out.HitRate.Input, 450.0/1600.0) {
		t.Fatalf("hit_rate.input 应为 450/1600，got %v", out.HitRate.Input)
	}
	if !almostEqual(out.HitRate.Total, 450.0/1900.0) {
		t.Fatalf("hit_rate.total 应为 450/1900，got %v", out.HitRate.Total)
	}
	if out.HitRate.Output != 0 {
		t.Fatalf("hit_rate.output 应为 0，got %v", out.HitRate.Output)
	}

	if len(out.ModelDist) != 2 {
		t.Fatalf("model_dist 应有 2 项，got %+v", out.ModelDist)
	}
	// token 消耗降序：gpt-4o(1300) > gpt-4o-mini(600)
	first, second := out.ModelDist[0], out.ModelDist[1]
	if first.Model != "gpt-4o" || first.Tokens != 1300 || first.Calls != 2 ||
		first.PromptTokens != 1100 || first.CompletionTokens != 200 || first.CachedTokens != 400 {
		t.Fatalf("model_dist[0] 不符，got %+v", first)
	}
	if second.Model != "gpt-4o-mini" || second.Tokens != 600 || second.Calls != 1 {
		t.Fatalf("model_dist[1] 不符，got %+v", second)
	}

	if len(out.Calendar) != 2 {
		t.Fatalf("calendar 应有 2 天，got %+v", out.Calendar)
	}
	if out.Calendar[0].Date > out.Calendar[1].Date {
		t.Fatalf("calendar 应按日期升序，got %+v", out.Calendar)
	}
	if len(out.Trend) != 30 {
		t.Fatalf("trend 长度应为 30，got %d", len(out.Trend))
	}
}

// TestAggregateModelStatsDenominatorZero 分母 0：prompt_tokens=0 且 completion_tokens=0
// （但有 cached_tokens>0）→ hit_rate 各值不 panic、返回 0 或合理值。
func TestAggregateModelStatsDenominatorZero(t *testing.T) {
	logs := []contracts.RouteRequestView{
		mkView("success", "gpt-4o", 100*time.Millisecond, 0, 0, 100, fixedNowUTC),
	}

	out := aggregateModelStats(logs, 30, time.UTC, fixedNowUTC)

	if out.HitRate.Input != 0 || out.HitRate.Output != 0 || out.HitRate.Total != 0 {
		t.Fatalf("分母为 0 时 hit_rate 应全 0，got %+v", out.HitRate)
	}
	if out.Summary.TotalTokens != 0 {
		t.Fatalf("total_tokens 应为 0，got %v", out.Summary.TotalTokens)
	}
	if out.Summary.SuccessRate != 1 {
		t.Fatalf("success_rate 应为 1，got %v", out.Summary.SuccessRate)
	}
}

// TestAggregateModelStatsFinalModelFallback final_model 空兜底：
// FinalModel="" 但有 RequestedModel → model_dist 用 requested_model；都空 → unknown。
func TestAggregateModelStatsFinalModelFallback(t *testing.T) {
	now := fixedNowUTC
	logs := []contracts.RouteRequestView{
		{RequestID: "r1", RequestedModel: "gpt-4o", StartedAt: now, Result: "success",
			Duration: contracts.DurationMS(10 * time.Millisecond), PromptTokens: 100, CompletionTokens: 10},
		{RequestID: "r2", RequestedModel: "", StartedAt: now, Result: "success",
			Duration: contracts.DurationMS(10 * time.Millisecond), PromptTokens: 50, CompletionTokens: 5},
	}

	out := aggregateModelStats(logs, 30, time.UTC, now)

	if len(out.ModelDist) != 2 {
		t.Fatalf("model_dist 应有 2 项，got %+v", out.ModelDist)
	}
	if out.ModelDist[0].Model != "gpt-4o" {
		t.Fatalf("FinalModel 空时应兜底用 requested_model，got %+v", out.ModelDist[0])
	}
	if out.ModelDist[1].Model != "unknown" {
		t.Fatalf("都空时应为 unknown，got %+v", out.ModelDist[1])
	}
}

// TestAggregateModelStatsTrendFill trend 补满：只有 1 天数据，days=30 → trend 长度 30，其余天全 0。
func TestAggregateModelStatsTrendFill(t *testing.T) {
	now := fixedNowUTC
	logs := []contracts.RouteRequestView{
		mkView("success", "gpt-4o", 100*time.Millisecond, 100, 20, 30, now),
	}

	out := aggregateModelStats(logs, 30, time.UTC, now)

	if len(out.Trend) != 30 {
		t.Fatalf("trend 长度应为 30，got %d", len(out.Trend))
	}
	var daysWithData int
	for _, day := range out.Trend {
		if day.Requests > 0 {
			daysWithData++
			if day.Requests != 1 || day.PromptTokens != 100 || day.CompletionTokens != 20 ||
				day.CachedTokens != 30 || day.TotalTokens != 120 {
				t.Fatalf("有数据天数值不符，got %+v", day)
			}
		} else if day.TotalTokens != 0 || day.PromptTokens != 0 {
			t.Fatalf("补满天应全 0，got %+v", day)
		}
	}
	if daysWithData != 1 {
		t.Fatalf("应有且仅有 1 天有数据，got %d", daysWithData)
	}
}

// TestAggregateModelStatsLocalDayKey 跨时区归类：UTC 18 日 18:00 = 北京时间 19 日 02:00，
// 应当按 location=Asia/Shanghai 归到 2026-08-19，不应归到 2026-08-18。
// 复现 bug：原实现硬编 UTC，导致 0:00–8:00（GMT+8）的请求都被算到前一天。
func TestAggregateModelStatsLocalDayKey(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skipf("本机没有 Asia/Shanghai 时区数据，跳过：%v", err)
	}

	// now 固定在 UTC 2026-08-18 18:00 = 北京时间 2026-08-19 02:00 CST，
	// 让"今天"在 shanghai 下是 2026-08-19，避免依赖墙钟。
	nowUTC := time.Date(2026, 8, 18, 18, 0, 0, 0, time.UTC)
	nowInShanghai := nowUTC.In(shanghai) // 2026-08-19 02:00 CST

	// 4 条请求，UTC 时间都在 2026-08-18 16:00–18:30 区间；
	// 对应北京时间 2026-08-19 00:00–02:30，全部应当归到 2026-08-19。
	mkUTC := func(h, m int) time.Time {
		return time.Date(2026, 8, 18, h, m, 0, 0, time.UTC)
	}
	logs := []contracts.RouteRequestView{
		mkView("success", "gpt-5.6-luna", 1*time.Second, 1000, 200, 0, mkUTC(16, 0)),
		mkView("success", "gpt-5.6-luna", 1*time.Second, 500, 100, 0, mkUTC(16, 30)),
		mkView("success", "doubao-seed", 1*time.Second, 1500, 300, 0, mkUTC(17, 30)),
		mkView("success", "doubao-seed", 1*time.Second, 297, 82, 0, mkUTC(18, 30)),
	}

	out := aggregateModelStats(logs, 30, shanghai, nowInShanghai)

	if len(out.Calendar) != 1 {
		t.Fatalf("calendar 应只有 1 天（本地 2026-08-19），got %+v", out.Calendar)
	}
	if got := out.Calendar[0]; got.Date != "2026-08-19" || got.Tokens != 3979 {
		t.Fatalf("calendar[0] 应为 2026-08-19 / 3979 tokens，got %+v", got)
	}

	if len(out.Trend) != 30 {
		t.Fatalf("trend 长度应为 30，got %d", len(out.Trend))
	}
	last := out.Trend[len(out.Trend)-1]
	if last.Date != "2026-08-19" || last.TotalTokens != 3979 {
		t.Fatalf("trend 最后一天应为 2026-08-19 / 3979 tokens，got %+v", last)
	}
}

// TestAggregateModelStatsTrendFillEmpty 时区不影响空数据下的 trend 长度。
func TestAggregateModelStatsTrendFillEmpty(t *testing.T) {
	shanghai, _ := time.LoadLocation("Asia/Shanghai")
	if shanghai == nil {
		t.Skip("缺 Asia/Shanghai 时区数据，跳过")
	}
	out := aggregateModelStats(nil, 30, shanghai, fixedNowUTC)
	if len(out.Trend) != 30 {
		t.Fatalf("trend 应补满 30，got %d", len(out.Trend))
	}
}

// TestAggregateModelStatsAvgDurationExcludesRunning 平均耗时：
// 含 result="running" 的记录不计入均值；全 running → 返回 0。
func TestAggregateModelStatsAvgDurationExcludesRunning(t *testing.T) {
	t.Run("mixed", func(t *testing.T) {
		now := fixedNowUTC
		logs := []contracts.RouteRequestView{
			mkView("success", "gpt-4o", 100*time.Millisecond, 100, 10, 0, now),
			mkView("running", "gpt-4o", 500*time.Millisecond, 200, 20, 0, now),
		}
		out := aggregateModelStats(logs, 30, time.UTC, now)
		if !almostEqual(out.Summary.AvgDurationMS, 100) {
			t.Fatalf("running 记录不应计入均值，应为 100，got %v", out.Summary.AvgDurationMS)
		}
	})
	t.Run("all-running", func(t *testing.T) {
		logs := []contracts.RouteRequestView{
			mkView("running", "gpt-4o", 500*time.Millisecond, 100, 10, 0, fixedNowUTC),
		}
		out := aggregateModelStats(logs, 30, time.UTC, fixedNowUTC)
		if out.Summary.AvgDurationMS != 0 {
			t.Fatalf("全 running 时均值应为 0，got %v", out.Summary.AvgDurationMS)
		}
	})
}
