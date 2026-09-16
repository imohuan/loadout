package adminapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loadout/core/config"
	"loadout/core/db"
	"loadout/core/store"
	"loadout/plugins/admin-auth"
	"loadout/plugins/gateway-keys"
	mcphub "loadout/plugins/mcp-hub"
	routelog "loadout/plugins/route-log"
	"loadout/plugins/skills"
	unifyai "loadout/plugins/unifyai"

	_ "modernc.org/sqlite"
)

// mcpInvocationsSchema 与 mcp-hub 包内建表 SQL 保持一致（测试装置用；含 v2 三列）。
const mcpInvocationsSchema = `CREATE TABLE IF NOT EXISTS mcp_invocations (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  started_at        TEXT    NOT NULL,
  finished_at       TEXT,
  aggregate_kind    TEXT    NOT NULL,
  aggregate_target  TEXT,
  tool_name         TEXT    NOT NULL,
  server_name       TEXT,
  result            TEXT    NOT NULL,
  http_status       INTEGER,
  duration_ms       INTEGER NOT NULL,
  error_message     TEXT,
  input_json        TEXT,
  output_json       TEXT,
  auth_kind         TEXT
);`

// newStatsTestServer 装配带真实 mcp-hub 与 route-log 的完整管理后台服务：
// 用 core/db.Open 建全量 SQLite schema（含 route_requests/route_attempts），
// 额外建 mcp_invocations 表供 /api/stats/mcp 使用。
// 返回服务器、hub、db 句柄与初始密码。
func newStatsTestServer(t *testing.T) (*httptest.Server, *mcphub.Service, *sql.DB, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}

	old := config.AdminPasswordFile
	config.AdminPasswordFile = filepath.Join(dir, "admin-password")
	t.Cleanup(func() { config.AdminPasswordFile = old })

	authSvc := adminauth.NewService(st, slog.Default())
	if _, err := authSvc.EnsureFirstRun(); err != nil {
		t.Fatalf("EnsureFirstRun: %v", err)
	}
	pw, err := os.ReadFile(config.AdminPasswordFile)
	if err != nil {
		t.Fatalf("读取初始密码: %v", err)
	}

	keys := gatewaykeys.NewManager(st)
	skillSvc := skills.NewService(st, slog.Default(), t.TempDir(), t.TempDir())

	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "loadout.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if _, err := sqlDB.Exec(mcpInvocationsSchema); err != nil {
		t.Fatalf("建 mcp_invocations 表: %v", err)
	}

	hub := mcphub.NewService(st, slog.Default(), sqlDB)
	routeLogSvc := routelog.NewService(sqlDB, slog.Default())
	// 注入真实 routing 仓储：admin-api 写 SQLite，mcp-hub 从同一仓储读，两边数据源一致。
	routing, err := db.NewRepository(sqlDB)
	if err != nil {
		t.Fatalf("db.NewRepository: %v", err)
	}
	svc := NewService(st, slog.Default(), authSvc, keys, skillSvc, hub, unifyai.NewService(slog.Default()))
	svc.SetRoutingServices(sqlDB, routing, nil, routeLogSvc)

	ts := httptest.NewServer(svc.Handler())
	t.Cleanup(ts.Close)
	// 关 hub 释放日志写句柄（Windows 下 t.TempDir cleanup 会因占用失败）。
	t.Cleanup(func() { _ = hub.Close() })
	return ts, hub, sqlDB, string(pw)
}

// TestStatsMcpEndpoint 验证 GET /api/stats/mcp 返回 200，且响应包含
// trend / rank_aggregates / rank_tools 三键，并能读到 mcp_invocations 里的数据。
// 插两行同 tool 记录并断言 rank_tools 聚合出 calls=2。
func TestStatsMcpEndpoint(t *testing.T) {
	ts, hub, _, pw := newStatsTestServer(t)
	cookie := login(t, ts, pw)

	now := time.Now().UTC().Format(time.RFC3339Nano)
	for i := 0; i < 2; i++ {
		if err := hub.RecordInvocation(context.Background(), mcphub.InvocationRecord{
			StartedAt:     now,
			AggregateKind: "single",
			ToolName:      "search_code",
			ServerName:    "github",
			Result:        "success",
			DurationMS:    120,
		}); err != nil {
			t.Fatalf("RecordInvocation: %v", err)
		}
	}

	resp, data := apiReq(t, ts, http.MethodGet, "/api/stats/mcp?days=30&top=5", nil, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, data)
	}
	for _, key := range []string{`"trend"`, `"rank_aggregates"`, `"rank_tools"`} {
		if !strings.Contains(string(data), key) {
			t.Fatalf("missing key %s in %s", key, data)
		}
	}
	var payload struct {
		RankTools []struct {
			ToolName   string `json:"tool_name"`
			ServerName string `json:"server_name"`
			Calls      int    `json:"calls"`
		} `json:"rank_tools"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	calls := 0
	for _, r := range payload.RankTools {
		if r.ToolName == "search_code" {
			calls = r.Calls
		}
	}
	if calls != 2 {
		t.Fatalf("search_code 期望 2 次调用，实际 %d（响应：%s）", calls, data)
	}
}

// TestStatsMcpNullServerName 回归：失败调用路径 serverName="" 落库为 NULL，
// rank_tools 查询 Scan NULL 到 string 会报错导致 /api/stats/mcp 500。
// 这里用空 ServerName 插一行，断言接口仍返回 200（覆盖 COALESCE 修复）。
func TestStatsMcpNullServerName(t *testing.T) {
	ts, hub, _, pw := newStatsTestServer(t)
	cookie := login(t, ts, pw)

	if err := hub.RecordInvocation(context.Background(), mcphub.InvocationRecord{
		StartedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		AggregateKind: "single",
		ToolName:      "hidden_tool",
		ServerName:    "",
		Result:        "not_found",
		HTTPStatus:    500,
		DurationMS:    30,
	}); err != nil {
		t.Fatalf("RecordInvocation: %v", err)
	}

	resp, data := apiReq(t, ts, http.MethodGet, "/api/stats/mcp?days=30&top=5", nil, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), `"hidden_tool"`) {
		t.Fatalf("rank_tools 缺少 hidden_tool: %s", data)
	}
}

// TestStatsModelsEndpoint 验证 GET /api/stats/models 返回 200，响应包含
// summary/hit_rate/trend/calendar/model_dist 五键，并能聚合 route_requests 里的数据。
func TestStatsModelsEndpoint(t *testing.T) {
	ts, _, sqlDB, pw := newStatsTestServer(t)
	cookie := login(t, ts, pw)

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := sqlDB.Exec(`INSERT INTO route_requests (request_id, requested_model, final_model, started_at, finished_at, result, duration_ms, prompt_tokens, completion_tokens, cached_tokens) VALUES (?, ?, ?, ?, ?, 'success', 120, 1000, 200, 400)`,
		"req-1", "gpt-4o", "gpt-4o", now, now); err != nil {
		t.Fatalf("插入 route_requests: %v", err)
	}

	resp, data := apiReq(t, ts, http.MethodGet, "/api/stats/models?days=30", nil, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, data)
	}
	for _, key := range []string{`"summary"`, `"hit_rate"`, `"trend"`, `"calendar"`, `"model_dist"`} {
		if !strings.Contains(string(data), key) {
			t.Fatalf("missing key %s in %s", key, data)
		}
	}
}

// TestStatsModelsMergeArchivedOnClear 回归：清空转发日志时必须先把统计数据
// 归档保存（route_stats_archive），之后的 /api/stats/models 要把归档数据与
// 现场日志一起算——归档桶进 summary、trend、calendar 与 model_dist，概览不归零。
func TestStatsModelsMergeArchivedOnClear(t *testing.T) {
	ts, _, sqlDB, pw := newStatsTestServer(t)
	cookie := login(t, ts, pw)

	// 清空前的日志：2 条成功请求，今天与昨天各一条。
	today := time.Now().Format("2006-01-02")
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	insertReq := func(id, started string) {
		t.Helper()
		if _, err := sqlDB.Exec("INSERT INTO route_requests (request_id, requested_model, final_model, started_at, finished_at, result, duration_ms, prompt_tokens, completion_tokens, cached_tokens) VALUES (?, ?, ?, ?, ?, ?, 120, 100, 20, 5)", id, "gpt-4o", "gpt-4o", started, started, "success"); err != nil {
			t.Fatalf("插入 route_requests: %v", err)
		}
	}
	insertReq("req-arch-1", time.Now().UTC().Format(time.RFC3339Nano))
	insertReq("req-arch-2", time.Now().UTC().AddDate(0, 0, -1).Format(time.RFC3339Nano))

	// 清空：真实 DELETE，但统计先归档。
	resp, data := apiReq(t, ts, http.MethodDelete, "/api/route-logs", nil, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE /api/route-logs: %d %s", resp.StatusCode, data)
	}
	// 日志真的没了。
	var count int
	if err := sqlDB.QueryRow("SELECT COUNT(*) FROM route_requests").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("清空后 route_requests 应为 0 条，实际 %d", count)
	}
	// 归档表有一条快照。
	if err := sqlDB.QueryRow("SELECT COUNT(*) FROM route_stats_archive").Scan(&count); err != nil {
		t.Fatalf("查 route_stats_archive: %v", err)
	}
	if count != 1 {
		t.Fatalf("清空后 route_stats_archive 应有 1 条快照，实际 %d", count)
	}

	// 统计接口：归档数据仍然在。
	resp, data = apiReq(t, ts, http.MethodGet, "/api/stats/models?days=7", nil, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/stats/models: %d %s", resp.StatusCode, data)
	}
	var payload ModelStats
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("解析统计响应: %v", err)
	}
	if payload.Summary.Requests != 2 {
		t.Fatalf("归档后 summary.requests = %d, want 2（响应：%s）", payload.Summary.Requests, data)
	}
	if payload.Summary.PromptTokens != 200 {
		t.Fatalf("归档后 summary.prompt_tokens = %d, want 200", payload.Summary.PromptTokens)
	}
	seenDates := map[string]bool{}
	for _, day := range payload.Trend {
		if day.Requests > 0 {
			seenDates[day.Date] = true
		}
	}
	if !seenDates[today] || !seenDates[yesterday] {
		t.Fatalf("trend 缺少归档日期 %v/%v: %+v", today, yesterday, payload.Trend)
	}
	if len(payload.ModelDist) != 1 || payload.ModelDist[0].Model != "gpt-4o" || payload.ModelDist[0].Calls != 2 {
		t.Fatalf("model_dist 未含归档数据: %+v", payload.ModelDist)
	}
	tokensByDate := map[string]int{}
	for _, day := range payload.Calendar {
		tokensByDate[day.Date] = day.Tokens
	}
	// 每天 120 = prompt 100 + completion 20（每天 1 条，日历口径不含 cached）。
	if tokensByDate[today] != 120 || tokensByDate[yesterday] != 120 {
		t.Fatalf("calendar 归档 token 不对: %+v（每天应为 120）", tokensByDate)
	}
}

// TestStatsModelsArchivedPersistsAcrossWindows：归档日期在查询窗口之外时，
// 其汇总数与模型分布仍计入（不丢数据），trend/calendar 只显示窗口内日期。
func TestStatsModelsArchivedPersistsAcrossWindows(t *testing.T) {
	ts, _, sqlDB, pw := newStatsTestServer(t)
	cookie := login(t, ts, pw)

	// 归档一条 40 天前的请求（超出 7 天窗口）。
	old := time.Now().UTC().AddDate(0, 0, -40).Format("2006-01-02")
	archiveJSON := "[{\"date\": \"" + old + "\", \"requests\": 1, \"prompt_tokens\": 50, \"completion_tokens\": 10, \"cached_tokens\": 0}]"
	if _, err := sqlDB.Exec("INSERT INTO route_stats_archive (id, archived_at, snapshot_json) VALUES (?, ?, ?)", "arch-1", time.Now().UTC().Format(time.RFC3339Nano), archiveJSON); err != nil {
		t.Fatalf("预置归档: %v", err)
	}

	resp, data := apiReq(t, ts, http.MethodGet, "/api/stats/models?days=7", nil, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/stats/models: %d %s", resp.StatusCode, data)
	}
	var payload ModelStats
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("解析统计响应: %v", err)
	}
	if payload.Summary.Requests != 1 || payload.Summary.PromptTokens != 50 {
		t.Fatalf("窗口外归档应计入 summary: %+v（响应 %s）", payload.Summary, data)
	}
	if len(payload.ModelDist) != 1 {
		t.Fatalf("窗口外归档应计入 model_dist: %+v", payload.ModelDist)
	}
	for _, day := range payload.Trend {
		if day.Requests != 0 {
			t.Fatalf("窗口外日期不应出现在 trend: %+v", day)
		}
	}
}
