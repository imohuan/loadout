package workbuddypanel

import (
	"context"
	"sort"
	"time"
)

// 消耗聚合口径：每个账号以上次远程同步时间为分割线（usage_synced_at）：
//   时间 ≤ 分割线 → 只认远程表 wb_panel_usage_remote（上游权威基线）；
//   时间 > 分割线 → 只认本地表 wb_panel_usage（拦截实时新增）。
// 两边绝不重叠，不会双计。

type usageAgg struct {
	daily   map[string][]UsageDay
	byModel []UsageModel
	today   map[string]float64
	seven   map[string]float64
	month   map[string]float64
	total   map[string]float64
	reqs    map[string]int64
// 每账号在 usage_synced_at 之后的本地消耗（用于实时扣减剩余积分）。
	sinceLocal map[string]float64

	// 共享聚合中间态（两张来源表共同累加）。
	dayMap   map[string]map[string]*UsageDay
	modelMap map[string]*UsageModel
}

func (s *Service) aggregateUsage(ctx context.Context) (*usageAgg, error) {
	agg := &usageAgg{
		daily: map[string][]UsageDay{}, byModel: []UsageModel{},
		today: map[string]float64{}, seven: map[string]float64{},
		month: map[string]float64{}, total: map[string]float64{}, reqs: map[string]int64{},
		sinceLocal: map[string]float64{},
		dayMap:   map[string]map[string]*UsageDay{},
		modelMap: map[string]*UsageModel{},
	}
	bounds, err := s.usageSyncBounds(ctx)
	if err != nil {
		return agg, err
	}
	// 先远程（基线），再本地（增量）。
	if err := s.aggregateTable(ctx, "wb_panel_usage_remote", bounds, true, agg); err != nil {
		return agg, err
	}
	if err := s.aggregateTable(ctx, "wb_panel_usage", bounds, false, agg); err != nil {
		return agg, err
	}
	for chID, days := range agg.dayMap {
		for _, day := range days {
			agg.daily[chID] = append(agg.daily[chID], *day)
		}
		sort.Slice(agg.daily[chID], func(a, b int) bool { return agg.daily[chID][a].Date < agg.daily[chID][b].Date })
	}
	for _, m := range agg.modelMap {
		agg.byModel = append(agg.byModel, *m)
	}
	sort.Slice(agg.byModel, func(a, b int) bool { return agg.byModel[a].Credit > agg.byModel[b].Credit })
	return agg, nil
}

// usageSyncBounds 每个账号的远程同步分割线（UTC）；从未同步过的账号不在 map 里。
func (s *Service) usageSyncBounds(ctx context.Context) (map[string]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT channel_id, usage_synced_at FROM wb_panel_credits WHERE usage_synced_at != ''")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var chID, synced string
		if err := rows.Scan(&chID, &synced); err != nil {
			continue
		}
		if t, perr := time.Parse(time.RFC3339Nano, synced); perr == nil {
			out[chID] = t
		}
	}
	return out, rows.Err()
}

// aggregateTable 聚合一张来源表进 agg。isRemote=true 只取 ≤ 分割线的行，false 只取 > 分割线的行。
// 账号没有分割线时：远程表整表跳过（没同步过就没有权威基线），本地表整表计入。
func (s *Service) aggregateTable(ctx context.Context, table string, bounds map[string]time.Time, isRemote bool, agg *usageAgg) error {
	rows, err := s.db.QueryContext(ctx, "SELECT channel_id, model, credit, created_at FROM " + table + " ORDER BY created_at")
	if err != nil {
		return err
	}
	defer rows.Close()
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	weekAgo := now.AddDate(0, 0, -7)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	for rows.Next() {
		var chID, model, createdAt string
		var credit float64
		if err := rows.Scan(&chID, &model, &credit, &createdAt); err != nil {
			continue
		}
		bound, hasBound := bounds[chID]
		t, perr := time.Parse(time.RFC3339Nano, createdAt)
		if perr != nil {
			continue
		}
		if isRemote {
			if !hasBound || t.After(bound) {
				continue // 超出基线窗口的远程行留给本地表负责
			}
		} else {
			if hasBound && !t.After(bound) {
				continue // 分割线之前的本地行已被远程基线覆盖
			}
			if hasBound {
				agg.sinceLocal[chID] += credit // 刷新之后的本地消耗，用于实时扣减剩余积分
			}
		}
		date := t.Format("2006-01-02")
		if agg.dayMap[chID] == nil {
			agg.dayMap[chID] = map[string]*UsageDay{}
		}
		day := agg.dayMap[chID][date]
		if day == nil {
			day = &UsageDay{Date: date, Models: map[string]float64{}}
			agg.dayMap[chID][date] = day
		}
		day.Requests++
		day.Credit += credit
		day.Models[model] += credit
		mm := agg.modelMap[model]
		if mm == nil {
			mm = &UsageModel{Model: model}
			agg.modelMap[model] = mm
		}
		mm.Requests++
		mm.Credit += credit
		agg.total[chID] += credit
		agg.reqs[chID]++
		// 今天/本月 = 本地日界，与前端口径一致。
		if t.After(todayStart) {
			agg.today[chID] += credit
		}
		if t.After(weekAgo) {
			agg.seven[chID] += credit
		}
		if t.After(monthStart) {
			agg.month[chID] += credit
		}
	}
	return rows.Err()
}
