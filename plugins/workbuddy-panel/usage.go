package workbuddypanel

import (
	"context"
	"time"
)

// usageRecord 上游流水接口的单条记录。
type usageRecord struct {
	RequestID   string  `json:"requestId"`
	Credit      float64 `json:"credit"`
	Model       string  `json:"model"`
	RequestTime string  `json:"requestTime"`
}

// usagePage 流水接口响应 data。
type usagePage struct {
	Total int           `json:"total"`
	Data  []usageRecord `json:"data"`
}

// fetchUsagePage 拉一页流水（窗口 ≤ 31 天，pageSize 500）。
func (c *codebuddyClient) fetchUsagePage(ctx context.Context, baseURL, apiKey string, start, end time.Time, pageNum int) (*usagePage, error) {
	body := map[string]any{
		"startTime": formatDash(start),
		"endTime":   formatDash(end),
		"pageNum":   pageNum,
		"pageSize":  500,
	}
	var data usagePage
	if err := c.call(ctx, baseURL, apiKey, "POST", "/billing/meter/get-user-request-usage", body, &data); err != nil {
		return nil, err
	}
	return &data, nil
}

// fetchUsage 拉取 [start, end] 的全部流水，按 31 天窗口拆分（超出会静默 total=0）。
// 结束时间 clamp 到当前时间（未来时间 / 起止倒置也会静默 total=0）。
func (c *codebuddyClient) fetchUsage(ctx context.Context, baseURL, apiKey string, start, end time.Time) ([]usageRecord, error) {
	now := time.Now()
	if end.After(now) {
		end = now
	}
	if start.After(end) {
		start = end
	}
	const win = 30 * 24 * time.Hour
	out := []usageRecord{}
	cursor := start
	for {
		chunkEnd := cursor.Add(win)
		if chunkEnd.After(end) {
			chunkEnd = end
		}
		page := 1
		for {
			p, err := c.fetchUsagePage(ctx, baseURL, apiKey, cursor, chunkEnd, page)
			if err != nil {
				return out, err
			}
			out = append(out, p.Data...)
			pages := (p.Total + 499) / 500
			if page >= pages || len(p.Data) == 0 {
				break
			}
			page++
		}
		if !chunkEnd.Before(end) {
			break
		}
		cursor = chunkEnd
	}
	return out, nil
}

// saveUsageRecords 把远程流水写入 wb_panel_usage_remote（按 request_id 去重），返回新增条数。
func (s *Service) saveUsageRecords(channelID string, records []usageRecord) int {
	inserted := 0
	for _, r := range records {
		if r.Credit == 0 || r.RequestTime == "" {
			continue
		}
		// 空 requestId 不被部分唯一索引拦截，会每次刷新重复插入，必须跳过。
		if r.RequestID == "" {
			s.lg.Warn("workbuddy-panel: 流水记录缺少 requestId，跳过", "channel", channelID, "model", r.Model)
			continue
		}
		t, err := time.ParseInLocation("2006-01-02 15:04:05", r.RequestTime, time.Local)
		if err != nil {
			s.lg.Warn("workbuddy-panel: 流水时间解析失败，跳过", "channel", channelID, "request_id", r.RequestID, "request_time", r.RequestTime)
			continue
		}
		_, err = s.db.Exec(`INSERT OR IGNORE INTO wb_panel_usage_remote(request_id, channel_id, model, credit, created_at) VALUES (?, ?, ?, ?, ?)`,
			r.RequestID, channelID, r.Model, r.Credit, t.UTC().Format(time.RFC3339Nano))
		if err == nil {
			inserted++
		}
	}
	return inserted
}
