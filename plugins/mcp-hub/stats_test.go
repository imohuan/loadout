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

// TestMigrateIdempotent 回归：migrate 连续调用两次（模拟老库升级 + 重复启动）
// 不得因 ALTER TABLE ADD COLUMN 重复执行而报错；v2 三列必须存在。
func TestMigrateIdempotent(t *testing.T) {
	s := newTestService(t)
	// 第二次 migrate：ensureColumns 应全部跳过（列已存在）。
	if err := migrate(s.db); err != nil {
		t.Fatalf("migrate second run: %v", err)
	}
	rows, err := s.db.Query(`PRAGMA table_info(mcp_invocations)`)
	if err != nil {
		t.Fatalf("pragma: %v", err)
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, typ        string
			dflt             sql.NullString
		)
		// PRAGMA 列序：cid,name,type,notnull,dflt_value,pk
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan pragma: %v", err)
		}
		cols[name] = true
	}
	for _, col := range []string{"input_json", "output_json", "auth_kind"} {
		if !cols[col] {
			t.Fatalf("column %s missing after migrate", col)
		}
	}
}

// TestRecordV2Fields 验证 v2 三列（input/output/auth）完整写入并可回读。
func TestRecordV2Fields(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	rec := InvocationRecord{
		StartedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		AggregateKind: "single",
		ToolName:      "read_file",
		ServerName:    "fs",
		Result:        "success",
		DurationMS:    42,
		InputJSON:     `{"path":"/tmp/x"}`,
		OutputJSON:    `[{"type":"text","text":"ok"}]`,
		AuthKind:      "mcp-key",
	}
	if err := s.RecordInvocation(ctx, rec); err != nil {
		t.Fatalf("record: %v", err)
	}
	var (
		input, output, auth string
	)
	if err := s.db.QueryRow(
		`SELECT COALESCE(input_json,''), COALESCE(output_json,''), COALESCE(auth_kind,'')
		 FROM mcp_invocations WHERE tool_name='read_file'`,
	).Scan(&input, &output, &auth); err != nil {
		t.Fatalf("select v2 fields: %v", err)
	}
	if input != rec.InputJSON || output != rec.OutputJSON || auth != rec.AuthKind {
		t.Fatalf("v2 fields mismatch: got (%q,%q,%q) want (%q,%q,%q)",
			input, output, auth, rec.InputJSON, rec.OutputJSON, rec.AuthKind)
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
	now := time.Now().UTC()
	for i, d := range []struct {
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
		started := now.Add(-time.Duration(i) * time.Minute).Format(time.RFC3339Nano)
		if err := s.RecordInvocation(ctx, InvocationRecord{
			StartedAt:       started,
			FinishedAt:      ptrStr(time.Now().UTC().Format(time.RFC3339Nano)),
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
