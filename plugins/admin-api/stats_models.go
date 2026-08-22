package adminapi

import (
	"context"
	"database/sql"
	"net/http"
	"sort"
	"strconv"
	"time"

	"loadout/plugins/contracts"
)

// ===== 模型维度统计（GET /api/stats/models）=====

// ModelStats 是 GET /api/stats/models 的 JSON 契约（与前端类型对齐，勿改字段名）。
type ModelStats struct {
	Summary   ModelSummary       `json:"summary"`
	HitRate   HitRate            `json:"hit_rate"`
	Trend     []ModelTrendDay    `json:"trend"`
	Calendar  []ModelCalendarDay `json:"calendar"`
	ModelDist []ModelDistItem    `json:"model_dist"`
}

// ModelSummary 汇总指标。
type ModelSummary struct {
	Requests         int     `json:"requests"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	CachedTokens     int     `json:"cached_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	SuccessRate      float64 `json:"success_rate"`
	AvgDurationMS    float64 `json:"avg_duration_ms"`
	Failed           int     `json:"failed"`
}

// HitRate 缓存命中率。
type HitRate struct {
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
	Total  float64 `json:"total"`
}

// ModelTrendDay 每日趋势点。
type ModelTrendDay struct {
	Date             string `json:"date"`
	Requests         int    `json:"requests"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	CachedTokens     int    `json:"cached_tokens"`
	TotalTokens      int    `json:"total_tokens"`
}

// ModelCalendarDay 日历热力图点（只含当日总 token 量）。
type ModelCalendarDay struct {
	Date   string `json:"date"`
	Tokens int    `json:"tokens"`
}

// ModelDistItem 模型消耗分布。
type ModelDistItem struct {
	Model            string `json:"model"`
	Calls            int    `json:"calls"`
	Tokens           int    `json:"tokens"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	CachedTokens     int    `json:"cached_tokens"`
}

// handleStatsModels 返回模型维度统计。数据源是 route_requests 表（route-log 已埋点，
// 无需新埋点）。routeLog.List 的 Limit 被钳制在 500 内，30 天全量拉不全，
// 故直接 SQL 查询共享 db。
func (s *Service) handleStatsModels(w http.ResponseWriter, r *http.Request) {
	if s.sqlDB == nil {
		writeError(w, http.StatusServiceUnavailable, "route-log 未装配")
		return
	}
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 365 {
			days = n
		}
	}
	// tz 可由前端按浏览器时区传入（如 "Asia/Shanghai"）；缺省用服务器本地时区，
	// 保证 "今天 00:00" 与用户视角一致——避免 UTC 0:00–8:00（GMT+8 区）的请求被算成"昨天"。
	loc := time.Local
	if v := r.URL.Query().Get("tz"); v != "" {
		if l, err := time.LoadLocation(v); err == nil {
			loc = l
		}
	}
	now := time.Now().In(loc)
	logs, err := listRouteRequests(r.Context(), s.sqlDB, days, loc, now)
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, aggregateModelStats(logs, days, loc, now))
}

// listRouteRequests 拉取最近 days 天（含今天）的 route_requests 全量，按 started_at 升序。
// cutoff 按 loc 内的"今天 00:00 - (days-1) 天"算，转 UTC 后传给 SQL——保证同一天边界
// 与前端日历一致：0:00–8:00（GMT+8 区）的请求仍然归到本地"今天"而不是 UTC 昨天。
func listRouteRequests(ctx context.Context, database *sql.DB, days int, loc *time.Location, now time.Time) ([]contracts.RouteRequestView, error) {
	startLocal := dayStart(now, loc).AddDate(0, 0, -(days - 1))
	cutoff := startLocal.UTC()
	rows, err := database.QueryContext(ctx, `SELECT request_id, requested_model, COALESCE(virtual_model, ''), started_at, finished_at, result, COALESCE(final_model, ''), COALESCE(final_channel_id, ''), COALESCE(http_status, 0), COALESCE(duration_ms, 0), error_message, COALESCE(stream, 0), COALESCE(prompt_tokens, 0), COALESCE(completion_tokens, 0), COALESCE(cached_tokens, 0) FROM route_requests WHERE started_at >= ? ORDER BY started_at`, cutoff.Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// 初始化为空切片，保证空列表 JSON 序列化为 []。
	out := make([]contracts.RouteRequestView, 0)
	for rows.Next() {
		view, err := scanRouteRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, view)
	}
	return out, rows.Err()
}

// scanRouteRequest 与 route-log 包内的 scanRequest 同构（该函数未导出，故在此复制）。
type routeRequestScanner interface{ Scan(...any) error }

func scanRouteRequest(sc routeRequestScanner) (contracts.RouteRequestView, error) {
	var view contracts.RouteRequestView
	var started string
	var finished sql.NullString
	var duration int64
	var stream int
	var promptTokens, completionTokens, cachedTokens int
	if err := sc.Scan(&view.RequestID, &view.RequestedModel, &view.VirtualModel, &started, &finished, &view.Result, &view.FinalModel, &view.FinalChannelID, &view.HTTPStatus, &duration, &view.ErrorMessage, &stream, &promptTokens, &completionTokens, &cachedTokens); err != nil {
		return view, err
	}
	view.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
	if finished.Valid {
		if parsed, err := time.Parse(time.RFC3339Nano, finished.String); err == nil {
			view.FinishedAt = &parsed
		}
	}
	view.Duration = contracts.DurationMS(time.Duration(duration) * time.Millisecond)
	view.Stream = stream != 0
	view.PromptTokens = promptTokens
	view.CompletionTokens = completionTokens
	view.CachedTokens = cachedTokens
	return view, nil
}

// aggregateModelStats 把 route_requests 日志聚合为模型维度统计（纯函数，便于单测）。
// days 用于 trend 补满（缺日期全 0）；calendar 不补 0，只含当日有请求的日子。
// loc 用于 day key 格式化与 trend 序列生成，使"今天"边界与用户视角一致；now 由 caller
// 注入（handler 传 time.Now()，测试传固定值），便于断言不依赖墙钟。
func aggregateModelStats(logs []contracts.RouteRequestView, days int, loc *time.Location, now time.Time) *ModelStats {
	out := &ModelStats{
		Summary:   ModelSummary{},
		HitRate:   HitRate{},
		Trend:     []ModelTrendDay{},
		Calendar:  []ModelCalendarDay{},
		ModelDist: []ModelDistItem{},
	}
	n := len(logs)
	if n == 0 {
		out.Trend = fillTrend(nil, days, loc, now)
		return out
	}

	var prompt, completion, cached, successCount, durationCount int
	var durationSum int64
	trend := make(map[string]*ModelTrendDay)
	calendar := make(map[string]int)
	dist := make(map[string]*ModelDistItem)

	for i := range logs {
		row := logs[i]
		prompt += row.PromptTokens
		completion += row.CompletionTokens
		cached += row.CachedTokens
		if row.Result == "success" {
			successCount++
		}
		if row.Result != "running" {
			durationCount++
			durationSum += row.Duration.Milliseconds()
		}

		date := row.StartedAt.In(loc).Format("2006-01-02")
		day := trend[date]
		if day == nil {
			day = &ModelTrendDay{Date: date}
			trend[date] = day
		}
		day.Requests++
		day.PromptTokens += row.PromptTokens
		day.CompletionTokens += row.CompletionTokens
		day.CachedTokens += row.CachedTokens
		day.TotalTokens += row.PromptTokens + row.CompletionTokens
		calendar[date] += row.PromptTokens + row.CompletionTokens

		model := row.FinalModel
		if model == "" {
			model = row.RequestedModel // 空 final_model 用 requested_model 兜底
		}
		if model == "" {
			model = "unknown"
		}
		item := dist[model]
		if item == nil {
			item = &ModelDistItem{Model: model}
			dist[model] = item
		}
		item.Calls++
		item.PromptTokens += row.PromptTokens
		item.CompletionTokens += row.CompletionTokens
		item.CachedTokens += row.CachedTokens
		item.Tokens += row.PromptTokens + row.CompletionTokens
	}

	out.Summary = ModelSummary{
		Requests:         n,
		PromptTokens:     prompt,
		CompletionTokens: completion,
		CachedTokens:     cached,
		TotalTokens:      prompt + completion,
		SuccessRate:      ratio(successCount, n),
		AvgDurationMS:    avg(durationSum, durationCount),
		Failed:           n - successCount,
	}
	out.HitRate = HitRate{
		Input:  ratio(cached, prompt),
		Output: 0,
		Total:  ratio(cached, prompt+completion),
	}
	out.Trend = fillTrend(trend, days, loc, now)
	out.Calendar = sortCalendar(calendar)
	out.ModelDist = sortModelDist(dist)
	return out
}

// fillTrend 生成最近 days 天的日期序列，缺日期全 0（保证折线图横轴连续）。
// 起点 = loc 内"今天 00:00 - (days-1) 天"，终点 = loc 内"今天 00:00"，
// 使 X 轴末日始终是 loc 视角的"今天"，与日历组件 isToday 高亮一致。
func fillTrend(seen map[string]*ModelTrendDay, days int, loc *time.Location, now time.Time) []ModelTrendDay {
	start := dayStart(now, loc).AddDate(0, 0, -(days - 1))
	out := make([]ModelTrendDay, 0, days)
	for i := 0; i < days; i++ {
		date := start.AddDate(0, 0, i).Format("2006-01-02")
		if day, ok := seen[date]; ok {
			out = append(out, *day)
		} else {
			out = append(out, ModelTrendDay{Date: date})
		}
	}
	return out
}

// dayStart 返回 t 所在时区"当天 00:00:00"的 time.Time（loc 内零点）。
func dayStart(t time.Time, loc *time.Location) time.Time {
	in := t.In(loc)
	return time.Date(in.Year(), in.Month(), in.Day(), 0, 0, 0, 0, loc)
}

// sortCalendar 按日期升序输出日历点，不补 0（前端热力图只看有数据的日子）。
func sortCalendar(seen map[string]int) []ModelCalendarDay {
	out := make([]ModelCalendarDay, 0, len(seen))
	for date, tokens := range seen {
		out = append(out, ModelCalendarDay{Date: date, Tokens: tokens})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out
}

// sortModelDist 按 token 消耗降序输出模型分布，其次调用次数降序、模型名升序。
func sortModelDist(seen map[string]*ModelDistItem) []ModelDistItem {
	out := make([]ModelDistItem, 0, len(seen))
	for _, item := range seen {
		out = append(out, *item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tokens != out[j].Tokens {
			return out[i].Tokens > out[j].Tokens
		}
		if out[i].Calls != out[j].Calls {
			return out[i].Calls > out[j].Calls
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// ratio 安全除法：分母为 0 时返回 0。
func ratio(num, den int) float64 {
	if den == 0 {
		return 0
	}
	return float64(num) / float64(den)
}

// avg 安全求平均：计数为 0 时返回 0。
func avg(sum int64, count int) float64 {
	if count == 0 {
		return 0
	}
	return float64(sum) / float64(count)
}
