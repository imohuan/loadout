package mcphub

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	// 对齐生产 core/db.Open：单连接 + busy_timeout，避免并发写触发 SQLITE_BUSY。
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return &Service{db: db}
}

func TestStatsTableExists(t *testing.T) {
	s := newTestService(t)
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM mcp_invocations").Scan(&count); err != nil {
		t.Fatalf("mcp_invocations missing: %v", err)
	}
}

func TestStatsEmpty(t *testing.T) {
	s := newTestService(t)
	got, err := s.Stats(context.Background(), 30, 5)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if got == nil || len(got.Trend) != 30 {
		t.Fatalf("empty trend expected 30 zero points, got %+v", got)
	}
	for _, p := range got.Trend {
		if p.Count != 0 {
			t.Fatalf("empty stats count expected 0, got %+v", p)
		}
	}
	if len(got.RankAggregates) != 0 || len(got.RankTools) != 0 {
		t.Fatalf("empty ranks expected, got %+v", got)
	}
}

func TestRecordAndQueryStats(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	for _, d := range []struct {
		kind, target, tool, server string
	}{
		{"single", "DEMO", "read_file", "fs"},
		{"single", "DEMO", "read_file", "fs"},
		{"group", "search", "web_search", "exa"},
		{"$smart", "", "ws_exa", "exa"},
		{"single", "DEMO", "exec", "shell"},
	} {
		target := d.target
		var targetPtr *string
		if d.kind != "$smart" {
			targetPtr = &target
		} else {
			empty := ""
			targetPtr = &empty
		}
		if err := s.RecordInvocation(ctx, InvocationRecord{
			StartedAt:       "2026-07-20T10:00:00Z",
			FinishedAt:      ptrStr("2026-07-20T10:00:01Z"),
			AggregateKind:   d.kind,
			AggregateTarget: targetPtr,
			ToolName:        d.tool,
			ServerName:      d.server,
			Result:          "success",
			DurationMS:      1000,
		}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	got, err := s.Stats(ctx, 30, 5)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if len(got.RankAggregates) != 3 {
		t.Fatalf("rank_aggregates expected 3, got %d", len(got.RankAggregates))
	}
	if got.RankAggregates[0].Kind != "single" || got.RankAggregates[0].Calls != 3 {
		t.Fatalf("rank top expected single/3, got %+v", got.RankAggregates[0])
	}
	if len(got.RankTools) != 4 {
		t.Fatalf("rank_tools expected 4, got %d", len(got.RankTools))
	}
}

// TestStatsWithEmptyServerName 回归：失败调用路径 ServerName="" 经
// NULLIF(?, ”) 落库为 NULL，Stats() 的 rank_tools 查询 Scan 到 NULL
// 必须被 COALESCE 兜底成空串，不得报错。
func TestStatsWithEmptyServerName(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	if err := s.RecordInvocation(ctx, InvocationRecord{
		StartedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		AggregateKind: "single",
		ToolName:      "hidden_tool",
		ServerName:    "",
		Result:        "not_found",
		HTTPStatus:    500,
		DurationMS:    30,
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	got, err := s.Stats(ctx, 30, 5)
	if err != nil {
		t.Fatalf("stats with NULL server_name: %v", err)
	}
	if len(got.RankTools) != 1 {
		t.Fatalf("rank_tools expected 1, got %d: %+v", len(got.RankTools), got.RankTools)
	}
	r := got.RankTools[0]
	if r.ToolName != "hidden_tool" || r.ServerName != "" || r.Calls != 1 {
		t.Fatalf("rank row expected hidden_tool/''/1, got %+v", r)
	}
}
