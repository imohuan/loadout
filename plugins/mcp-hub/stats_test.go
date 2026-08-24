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

// seedInvocations 插入 6 条不同 kind/auth/server 的记录，供 ListInvocations 过滤测试。
func seedInvocations(t *testing.T, s *Service) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	for i, d := range []struct {
		kind, target, tool, server, auth, result string
	}{
		{"single", "DEMO", "read_file", "fs", "mcp-key", "success"},
		{"single", "DEMO", "read_file", "fs", "mcp-key", "success"},
		{"group", "search", "web_search", "exa", "session", "success"},
		{"$smart", "", "ws_exa", "exa", "public", "success"},
		{"single", "DEMO", "exec", "shell", "mcp-key", "error"},
		{"group", "search", "web_search", "exa", "session", "not_found"},
	} {
		target := d.target
		var targetPtr *string
		if d.kind != "$smart" {
			targetPtr = &target
		} else {
			empty := ""
			targetPtr = &empty
		}
		started := base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339Nano)
		if err := s.RecordInvocation(ctx, InvocationRecord{
			StartedAt:       started,
			AggregateKind:   d.kind,
			AggregateTarget: targetPtr,
			ToolName:        d.tool,
			ServerName:      d.server,
			Result:          d.result,
			DurationMS:      100 + i,
			InputJSON:       `{"i":` + string(rune('0'+i)) + `}`,
			AuthKind:        d.auth,
		}); err != nil {
			t.Fatalf("seed record %d: %v", i, err)
		}
	}
}

func TestListInvocationsPagination(t *testing.T) {
	s := newTestService(t)
	seedInvocations(t, s)
	ctx := context.Background()

	// 无过滤，page=1 size=2：返回 2 条，total=6，按时间倒序（最新在前）。
	page, err := s.ListInvocations(ctx, InvocationQuery{Page: 1, Size: 2})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if page.Total != 6 {
		t.Fatalf("total expected 6, got %d", page.Total)
	}
	if len(page.Items) != 2 {
		t.Fatalf("items expected 2, got %d", len(page.Items))
	}
	// 最新一条是 i=5 的 group/web_search/not_found。
	if page.Items[0].ToolName != "web_search" || page.Items[0].Result != "not_found" {
		t.Fatalf("first item expected web_search/not_found, got %+v", page.Items[0])
	}

	// 第二页：i=3,2 两条。
	page2, err := s.ListInvocations(ctx, InvocationQuery{Page: 2, Size: 2})
	if err != nil {
		t.Fatalf("list page2: %v", err)
	}
	if len(page2.Items) != 2 || page2.Total != 6 {
		t.Fatalf("page2 expected 2 items/total 6, got %d/%d", len(page2.Items), page2.Total)
	}
	if page2.Items[0].ToolName != "ws_exa" {
		t.Fatalf("page2 first expected ws_exa, got %+v", page2.Items[0])
	}
}

func TestListInvocationsFilters(t *testing.T) {
	s := newTestService(t)
	seedInvocations(t, s)
	ctx := context.Background()

	cases := []struct {
		name string
		q    InvocationQuery
		want int
	}{
		{"kind=single", InvocationQuery{Kind: "single"}, 3},
		{"kind=$smart", InvocationQuery{Kind: "$smart"}, 1},
		{"kind=group", InvocationQuery{Kind: "group"}, 2},
		{"auth=mcp-key", InvocationQuery{Auth: "mcp-key"}, 3},
		{"auth=session", InvocationQuery{Auth: "session"}, 2},
		{"auth=public", InvocationQuery{Auth: "public"}, 1},
		{"tool LIKE", InvocationQuery{Tool: "web"}, 2},
		{"tool LIKE full", InvocationQuery{Tool: "web_search"}, 2},
		{"server=fs", InvocationQuery{Server: "fs"}, 2},
		{"kind+auth", InvocationQuery{Kind: "single", Auth: "mcp-key"}, 3},
		{"kind+auth none", InvocationQuery{Kind: "group", Auth: "mcp-key"}, 0},
	}
	for _, c := range cases {
		page, err := s.ListInvocations(ctx, c.q)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if page.Total != c.want {
			t.Fatalf("%s: total expected %d, got %d", c.name, c.want, page.Total)
		}
	}
}

// TestListInvocationsLikeEscape 回归：LIKE 通配符注入被转义，% _ 按字面匹配。
func TestListInvocationsLikeEscape(t *testing.T) {
	s := newTestService(t)
	seedInvocations(t, s)
	ctx := context.Background()
	// "%" 字面量匹配不到任何 tool_name（转义后按字面匹配），而非匹配全部。
	page, err := s.ListInvocations(ctx, InvocationQuery{Tool: "%"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if page.Total != 0 {
		t.Fatalf("literal %% expected 0 matches, got %d", page.Total)
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
