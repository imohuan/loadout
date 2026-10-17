package workbuddypanel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"
)

var defaultLogger = slog.Default()

// ===== 数据结构（前端契约，字段名勿改） =====

// CreditPackage 单个资源包（按来源聚合展示）。
type CreditPackage struct {
	Name     string         `json:"name"`
	Packages int            `json:"packages"`
	Remain   float64        `json:"remain"`
	Size     float64        `json:"size"`
	Expiry   []CreditExpiry `json:"expiry"`
}

// CreditExpiry 到期分桶。
type CreditExpiry struct {
	End    string  `json:"end"`
	Remain float64 `json:"remain"`
}

// WbModel 模型倍率（来自 /console/enterprises/personal/models）。
type WbModel struct {
	ID                string  `json:"id"`
	Name              string  `json:"name"`
	Credits           string  `json:"credits"`
	Rate              float64 `json:"rate"`
	Description       string  `json:"description,omitempty"`
	Context           int     `json:"context,omitempty"`
	MaxOutput         int     `json:"max_output,omitempty"`
	SupportsImages    bool    `json:"supports_images,omitempty"`
	SupportsTool      bool    `json:"supports_tool,omitempty"`
	SupportsReasoning bool    `json:"supports_reasoning,omitempty"`
	OnlyReasoning     bool    `json:"only_reasoning,omitempty"`
	IsDefault         bool    `json:"is_default,omitempty"`
	ReasoningEffort   string  `json:"reasoning_effort,omitempty"`
}

// AccountData 单个渠道 Key 的缓存面板数据。
type AccountData struct {
	ChannelID string          `json:"channel_id"`
	Label     string          `json:"label"`
	Remain    float64         `json:"remain"`
	Total     float64         `json:"total"`
	Groups    []CreditPackage `json:"groups"`
	Models    []WbModel       `json:"models"`
	SyncedAt  string          `json:"synced_at"`
	LastError string          `json:"last_error,omitempty"`
	// Channel 渠道级健康状态（channel_states，与「模型状态」页同源）。
	Channel ChannelHealth `json:"channel"`
	// ModelStates 该 Key 下每个模型的真实健康状态（model_states 表）。
	ModelStates map[string]ModelState `json:"model_states"`
	// 消耗（本地聚合，非缓存）。
	SpentToday float64 `json:"spent_today"`
	Spent7d    float64 `json:"spent_7d"`
	SpentMonth float64 `json:"spent_month"`
	SpentTotal float64 `json:"spent_total"`
	Requests   int64   `json:"requests"`
	// 上次远程流水同步时间：剩余积分 = 缓存快照 − 这之后的本地消耗。
	UsageSyncedAt string `json:"usage_synced_at,omitempty"`
}

// PanelData GET /api/workbuddy-panel/data 响应。
type PanelData struct {
	Accounts     []AccountData         `json:"accounts"`
	UsageDaily   map[string][]UsageDay `json:"usage_daily"`
	UsageByModel []UsageModel          `json:"usage_by_model"`
	// UsageLogs 消耗明细（远程基线 + 刷新后本地，统一列表，按时间倒序，最多 500 条）。
	UsageLogs []UsageLog `json:"usage_logs"`
}

// UsageLog 一条消耗明细。Source: remote（上游基线）/ local（本地拦截）。
type UsageLog struct {
	RequestID  string  `json:"request_id"`
	AccountID  string  `json:"account_id"`
	Account    string  `json:"account"`
	Model      string  `json:"model"`
	Credit     float64 `json:"credit"`
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	CachedTokens     int `json:"cached_tokens"`
	CreatedAt  string  `json:"created_at"`
	Source     string  `json:"source"`
}

// UsageDay 每日消耗（本地聚合）。
type UsageDay struct {
	Date     string             `json:"date"`
	Requests int64              `json:"requests"`
	Credit   float64            `json:"credit"`
	Models   map[string]float64 `json:"models"`
}

// UsageModel 按模型聚合消耗。
type UsageModel struct {
	Model    string  `json:"model"`
	Requests int64   `json:"requests"`
	Credit   float64 `json:"credit"`
}

// RefreshResult POST /api/workbuddy-panel/refresh 响应。
type RefreshResult struct {
	Synced int      `json:"synced"`
	Failed []string `json:"failed"`
}

// ===== 上游客户端 =====

type codebuddyClient struct {
	hc *http.Client
}

func newCodebuddyClient() *codebuddyClient {
	return &codebuddyClient{hc: &http.Client{Timeout: 30 * time.Second}}
}

// call 上游 JSON 请求；401 返回 HTML，必须先判状态码。
func (c *codebuddyClient) call(ctx context.Context, baseURL, apiKey, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytesReader(raw)
	}
	// URL 规范化收口：base_url 可能带 /v2 等路径前缀，而全部上游接口都挂在
	// host 根路径上，统一在此剥掉，调用方无需各自处理（models/usage 同理）。
	host := baseURL
	if u, perr := urlParse(baseURL); perr == nil {
		host = u
	}
	req, err := http.NewRequestWithContext(ctx, method, host+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-CodeBuddy-Request", "1")
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var env struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("解析响应失败: %w", err)
	}
	if env.Code != 0 {
		return fmt.Errorf("code=%d msg=%s", env.Code, env.Msg)
	}
	if out != nil && len(env.Data) > 0 {
		return json.Unmarshal(env.Data, out)
	}
	return nil
}

// resourceAccount 上游资源包行。
type resourceAccount struct {
	PackageName         string  `json:"PackageName"`
	CapacityRemain      float64 `json:"CapacityRemain"`
	CapacityUsed        float64 `json:"CapacityUsed"`
	CapacitySize        float64 `json:"CapacitySize"`
	CycleCapacityRemain float64 `json:"CycleCapacityRemain"`
	CycleCapacityUsed   float64 `json:"CycleCapacityUsed"`
	CycleCapacitySize   float64 `json:"CycleCapacitySize"`
	CycleEndTime        string  `json:"CycleEndTime"`
}

// pkgRemainSize 单包口径：Cycle 优先，否则 Capacity。
func pkgRemainSize(a resourceAccount) (remain, size float64) {
	if a.CycleCapacitySize > 0 {
		remain, size = a.CycleCapacityRemain, a.CycleCapacitySize
	} else {
		remain, size = a.CapacityRemain, a.CapacitySize
	}
	if remain < 0 {
		remain = 0
	}
	if size < remain {
		size = remain
	}
	return remain, size
}

// fetchCredits 拉一个 Key 的积分构成。
func (c *codebuddyClient) fetchCredits(ctx context.Context, baseURL, apiKey string) (remain, total float64, groups []CreditPackage, err error) {
	body := map[string]any{
		"PageNumber": 1, "PageSize": 100, "ProductCode": "p_tcaca", "Status": []int{0, 3},
		"PackageEndTimeRangeBegin": formatSlash(time.Now()),
		"PackageEndTimeRangeEnd":   formatSlash(time.Now().AddDate(100, 0, 0)),
	}
	var data struct {
		Response struct {
			Data struct {
				Accounts []resourceAccount `json:"Accounts"`
			} `json:"Data"`
		} `json:"Response"`
	}
	if err = c.call(ctx, baseURL, apiKey, "POST", "/billing/meter/get-user-resource", body, &data); err != nil {
		return
	}
	byName := map[string]*CreditPackage{}
	for _, a := range data.Response.Data.Accounts {
		r, sz := pkgRemainSize(a)
		remain += r
		total += sz
		g, ok := byName[a.PackageName]
		if !ok {
			g = &CreditPackage{Name: a.PackageName}
			byName[a.PackageName] = g
			groups = append(groups, *g)
		}
		idx := len(groups) - 1
		groups[idx].Packages++
		groups[idx].Remain += r
		groups[idx].Size += sz
		if a.CycleEndTime != "" {
			groups[idx].Expiry = append(groups[idx].Expiry, CreditExpiry{End: a.CycleEndTime, Remain: r})
		}
	}
	for i := range groups {
		sort.Slice(groups[i].Expiry, func(a, b int) bool { return groups[i].Expiry[a].End < groups[i].Expiry[b].End })
	}
	sort.Slice(groups, func(a, b int) bool { return groups[a].Remain > groups[b].Remain })
	return remain, total, groups, nil
}

// fetchModels 拉模型倍率列表。
func (c *codebuddyClient) fetchModels(ctx context.Context, baseURL, apiKey string) ([]WbModel, error) {
	return c.fetchModelsHost(ctx, baseURL, apiKey)
}

// ===== 刷新与数据聚合 =====

// wbChannels 返回所有 workbuddy 渠道 Key（解密后 API Key），附带上次流水同步时间。
func (s *Service) wbChannels(ctx context.Context) ([]struct {
	ID, Name, BaseURL, APIKey string
	UsageSyncedAt             string
}, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.id, COALESCE(c.name,''), COALESCE(c.base_url,''), COALESCE(c.api_key_cipher,''), COALESCE(wc.usage_synced_at,'')
		FROM channels c LEFT JOIN wb_panel_credits wc ON wc.channel_id = c.id
		WHERE c.manual_enabled = 1 ORDER BY c.position, c.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
		out := []struct {
			ID, Name, BaseURL, APIKey string
			UsageSyncedAt             string
		}{}
	for rows.Next() {
		var id, name, baseURL, cipher, usageSynced string
		if err := rows.Scan(&id, &name, &baseURL, &cipher, &usageSynced); err != nil {
			return nil, err
		}
		if !wbBaseURLPattern.MatchString(baseURL) {
			continue
		}
		apiKey := ""
		if cipher != "" {
			if plain, derr := s.st.Decrypt(cipher); derr == nil {
				apiKey = plain
			} else {
				name = name + "(Key解密失败)"
			}
		}
		out = append(out, struct {
			ID, Name, BaseURL, APIKey string
			UsageSyncedAt             string
		}{id, name, baseURL, apiKey, usageSynced})
	}
	return out, rows.Err()
}


type wbChannel struct {
	ID, Name, BaseURL, APIKey string
	UsageSyncedAt             string
}

// RefreshAll 手动刷新：按配置并发度拉各 Key 的积分与流水并写缓存。
//
// 闸门说明（谁会被跳过）：
//   - 只跳过「已禁用」的 Key（channel_states.status == disabled）。冷却中 / 可用 /
//     手动关闭一律照常拉取——冷却只是临时退避，积分余额仍然需要刷新。
//   - 模型列表与账号无关（所有 Key 返回同一份），只拉一次，各 Key 复用。
func (s *Service) RefreshAll(ctx context.Context) (RefreshResult, error) {
	result := RefreshResult{Failed: []string{}}
	chs, err := s.wbChannels(ctx)
	if err != nil {
		return result, err
	}
	// 读一次健康状态：只把「已禁用」当作不存在。
	_, chHealth, herr := s.loadAllHealth(ctx)
	if herr != nil {
		s.lg.Warn("workbuddy-panel: 刷新时读取健康状态失败，按全部可用处理", "err", herr)
	}

	// 模型列表：与账号无关，只拉一次，其余复用。
	// 依次尝试候选 Key：某个 Key 过期/失效（拉取报错）就换下一个，
	// 直到成功或全部失败。候选顺序 = 渠道顺序，且跳过已禁用的 Key。
	var sharedModels []WbModel
	var modelsErr error
	for _, ch := range chs {
		if ch.APIKey == "" {
			continue
		}
		if h, ok := chHealth[ch.ID]; ok && h.Status == "disabled" {
			continue // 已禁用的 Key 不作为模型列表候选
		}
		sharedModels, modelsErr = s.client.fetchModels(ctx, ch.BaseURL, ch.APIKey)
		if modelsErr == nil {
			break
		}
		s.lg.Warn("workbuddy-panel: 拉取模型列表失败，换下一个 Key 重试", "channel", ch.Name, "err", modelsErr)
	}
	if modelsErr != nil {
		s.lg.Warn("workbuddy-panel: 拉取模型列表失败", "err", modelsErr)
	}
	mdlJSON, _ := json.Marshal(sharedModels)

	// 过滤出要处理的 Key。
	targets := make([]wbChannel, 0, len(chs))
	for _, ch := range chs {
		if h, ok := chHealth[ch.ID]; ok && h.Status == "disabled" {
			continue // 已禁用：视为不存在，不打上游
		}
		if ch.APIKey == "" {
			result.Failed = append(result.Failed, ch.Name+"(未配置 Key)")
			continue
		}
		targets = append(targets, wbChannel(ch))
	}

	// 并发拉取：并发度可在面板设置里配置（默认 3），上限 10 防止把上游打爆/账号被风控。
	conc := s.refreshConcurrency(ctx)
	sem := make(chan struct{}, conc)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, ch := range targets {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			name, errMsg := s.refreshOne(ctx, ch, mdlJSON)
			mu.Lock()
			defer mu.Unlock()
			if errMsg != "" {
				result.Failed = append(result.Failed, name+": "+errMsg)
			} else {
				result.Synced++
			}
		}()
	}
	wg.Wait()
	return result, nil
}

// refreshOne 刷新单个 Key：积分 + 流水，写缓存。返回渠道名与错误摘要（空 = 成功）。
func (s *Service) refreshOne(ctx context.Context, ch wbChannel, mdlJSON []byte) (string, string) {
		remain, total, groups, cerr := s.client.fetchCredits(ctx, ch.BaseURL, ch.APIKey)
		// 历史消耗流水：增量拉取。首次拉近 30 天；之后只拉上次同步时间之后
		// 的部分（回退 5 分钟重叠防边界丢条）；远程表为空时回退拉 30 天。
		start := time.Now().AddDate(0, 0, -30)
		if ch.UsageSyncedAt != "" {
			if t, perr := time.Parse(time.RFC3339Nano, ch.UsageSyncedAt); perr == nil {
				start = t.Add(-5 * time.Minute)
			}
			var remoteCnt int
			_ = s.db.QueryRow(`SELECT COUNT(*) FROM wb_panel_usage_remote WHERE channel_id = ?`, ch.ID).Scan(&remoteCnt)
			if remoteCnt == 0 {
				start = time.Now().AddDate(0, 0, -30) // 远程基线丢失，重拉 30 天
			}
		}
		records, uerr2 := s.client.fetchUsage(ctx, ch.BaseURL, ch.APIKey, start, time.Now())
		if uerr2 != nil {
			s.lg.Warn("workbuddy-panel: 拉取历史消耗失败", "channel", ch.Name, "err", uerr2)
		}
		_ = s.saveUsageRecords(ch.ID, records)
		// 分割线只在流水完整拉取成功时推进；失败时保持旧值，下次刷新会
		// 用旧分割线重拉（重叠去重），避免失败窗口内的远程记录永久丢失。
		newSync := ch.UsageSyncedAt
		if uerr2 == nil {
			newSync = time.Now().UTC().Format(time.RFC3339Nano)
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		lastErr := ""
		if cerr != nil {
			lastErr = cerr.Error()
		}
		pkgJSON, _ := json.Marshal(groups)
		_, uerr := s.db.Exec(`INSERT INTO wb_panel_credits(channel_id, label, remain, total, packages_json, models_json, synced_at, last_error, usage_synced_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(channel_id) DO UPDATE SET label=excluded.label, remain=excluded.remain, total=excluded.total, packages_json=excluded.packages_json, models_json=excluded.models_json, synced_at=excluded.synced_at, last_error=excluded.last_error, usage_synced_at=excluded.usage_synced_at`,
			ch.ID, ch.Name, remain, total, string(pkgJSON), string(mdlJSON), now, lastErr, newSync)
		if uerr != nil {
			return ch.Name, uerr.Error()
		}
		if lastErr == "" && uerr2 != nil {
			lastErr = "usage: " + uerr2.Error()
		}
		return ch.Name, lastErr
	}

// refreshConcurrency 读面板设置里的并发数（1-10，默认 3）。
func (s *Service) refreshConcurrency(ctx context.Context) int {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT concurrency FROM wb_panel_settings WHERE id = 1`).Scan(&n); err != nil {
		return 3
	}
	if n < 1 {
		return 1
	}
	if n > 10 {
		return 10
	}
	return n
}

// HandleSettings GET：返回刷新并发设置。
func (s *Service) HandleSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]int{"concurrency": s.refreshConcurrency(r.Context())})
}

// HandleSaveSettings POST：保存刷新并发设置。
func (s *Service) HandleSaveSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Concurrency int `json:"concurrency"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Concurrency < 1 {
		writeError(w, http.StatusBadRequest, "并发数必须 ≥ 1")
		return
	}
	if req.Concurrency > 10 {
		req.Concurrency = 10
	}
	if _, err := s.db.Exec(`INSERT INTO wb_panel_settings(id, concurrency) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET concurrency = excluded.concurrency`, req.Concurrency); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"concurrency": req.Concurrency})
}

// HandleSaveAutoRefresh POST：保存自动刷新开关。
func (s *Service) HandleSaveAutoRefresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AutoRefresh int `json:"auto_refresh"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.AutoRefresh != 0 && req.AutoRefresh != 1 {
		writeError(w, http.StatusBadRequest, "auto_refresh 只能是 0 或 1")
		return
	}
	if _, err := s.db.Exec(`UPDATE wb_panel_settings SET auto_refresh = ? WHERE id = 1`, req.AutoRefresh); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"auto_refresh": req.AutoRefresh})
}

// autoRefreshEnabled 读自动刷新开关（读失败按开处理）。
func (s *Service) autoRefreshEnabled(ctx context.Context) bool {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT auto_refresh FROM wb_panel_settings WHERE id = 1`).Scan(&n); err != nil {
		return true
	}
	return n == 1
}

// HandleAutoRefreshState GET：返回自动刷新开关状态。
func (s *Service) HandleAutoRefreshState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"auto_refresh": s.autoRefreshEnabled(r.Context())})
}

// usageLogs 合并远程基线 + 刷新后本地的消耗明细，按时间倒序，最多 500 条。
// 口径与 aggregateUsage 一致：账号有分割线时，远程取 ≤ 分割线、本地取 > 分割线。
func (s *Service) usageLogs(ctx context.Context, labels map[string]string) []UsageLog {
	bounds, err := s.usageSyncBounds(ctx)
	if err != nil {
		return []UsageLog{}
	}
	logs := make([]UsageLog, 0, 256)
	appendFrom := func(table string, isRemote bool) {
		rows, err := s.db.QueryContext(ctx,
			"SELECT request_id, channel_id, model, credit, prompt_tokens, completion_tokens, cached_tokens, created_at FROM "+table+" ORDER BY created_at DESC LIMIT 1000")
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			var reqID, chID, model, createdAt string
			var credit float64
			var prompt, completion, cached int
			if err := rows.Scan(&reqID, &chID, &model, &credit, &prompt, &completion, &cached, &createdAt); err != nil {
				continue
			}
			bound, hasBound := bounds[chID]
			t, perr := time.Parse(time.RFC3339Nano, createdAt)
			if perr != nil {
				continue
			}
			if isRemote {
				if !hasBound || t.After(bound) {
					continue
				}
			} else if hasBound && !t.After(bound) {
				continue
			}
			logs = append(logs, UsageLog{
				RequestID: reqID, AccountID: chID, Account: labels[chID],
				Model: model, Credit: credit,
				PromptTokens: prompt, CompletionTokens: completion, CachedTokens: cached,
				CreatedAt: t.Local().Format("01-02 15:04:05"),
				Source: map[bool]string{true: "remote", false: "local"}[isRemote],
			})
		}
	}
	appendFrom("wb_panel_usage_remote", true)
	appendFrom("wb_panel_usage", false)
	// 时间倒序 + 截断，本地拦截的请求 id 也展示。
	sort.Slice(logs, func(a, b int) bool { return logs[a].CreatedAt > logs[b].CreatedAt })
	if len(logs) > 500 {
		logs = logs[:500]
	}
	return logs
}

// BuildData 组装面板数据：缓存 + 本地消耗聚合。
func (s *Service) BuildData(ctx context.Context) (*PanelData, error) {
	out := &PanelData{Accounts: []AccountData{}, UsageDaily: map[string][]UsageDay{}, UsageByModel: []UsageModel{}}
	// usage_synced_at 一起读出来：剩余积分要按它扣掉刷新之后的本地消耗。
	rows, err := s.db.QueryContext(ctx, `SELECT channel_id, label, remain, total, packages_json, models_json, synced_at, last_error, usage_synced_at FROM wb_panel_credits ORDER BY label`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var a AccountData
		var pkgJSON, mdlJSON, usageSynced string
		if err := rows.Scan(&a.ChannelID, &a.Label, &a.Remain, &a.Total, &pkgJSON, &mdlJSON, &a.SyncedAt, &a.LastError, &usageSynced); err != nil {
			continue
		}
		_ = json.Unmarshal([]byte(pkgJSON), &a.Groups)
		_ = json.Unmarshal([]byte(mdlJSON), &a.Models)
		a.UsageSyncedAt = usageSynced
		out.Accounts = append(out.Accounts, a)
	}
	// 消耗聚合（本地）。
	// model_states / channel_states 一次性批量读（避免 17 Key × 2 查询与网关写竞争锁）。
	statesByChannel, chHealth, serr := s.loadAllHealth(ctx)
	if serr != nil {
		s.lg.Warn("workbuddy-panel: 读取健康状态失败", "err", serr)
	}
	agg, err := s.aggregateUsage(ctx)
	if err == nil {
		out.UsageDaily = agg.daily
		out.UsageByModel = agg.byModel
		// 消耗明细：账号 id → 名称（远程表里没有名字，用缓存里的 label）。
		labels := make(map[string]string, len(out.Accounts))
		for _, a := range out.Accounts {
			labels[a.ChannelID] = a.Label
		}
		out.UsageLogs = s.usageLogs(ctx, labels)
		for i := range out.Accounts {
			id := out.Accounts[i].ChannelID
			out.Accounts[i].SpentToday = agg.today[id]
			out.Accounts[i].Spent7d = agg.seven[id]
			out.Accounts[i].SpentMonth = agg.month[id]
			out.Accounts[i].SpentTotal = agg.total[id]
			out.Accounts[i].Requests = agg.reqs[id]
			// 剩余积分 = 上次刷新快照 − 刷新之后（> usage_synced_at）的本地消耗。
			if out.Accounts[i].UsageSyncedAt != "" {
				if after, ok := agg.sinceLocal[id]; ok {
					out.Accounts[i].Remain -= after
					if out.Accounts[i].Remain < 0 {
						out.Accounts[i].Remain = 0
					}
				}
			}
			if st, ok := statesByChannel[id]; ok {
				out.Accounts[i].ModelStates = st
				out.Accounts[i].Channel = chHealth[id]
			} else {
				out.Accounts[i].ModelStates = map[string]ModelState{}
			}
		}
	}
	return out, nil
}

