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
