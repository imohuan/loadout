package mcphub

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"loadout/core/store"
	"loadout/plugins/types"
)

// newTraceTestService 构造带 db + store + logger 的 Service，用于验证
// Invoke → recordInvocation → mcp_invocations 落库链路，以及 parseAggregate 路由解析。
// store 预置一个上游（github）与一个分组（group1），使 parseAggregate 能识别 single/group。
// db 对齐生产 core/db.Open 的配置：单连接 + busy_timeout，避免并发写触发 SQLITE_BUSY。
func newTraceTestService(t *testing.T) *Service {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
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

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if err := st.Write(types.FileMCPServers, []types.MCPServer{
		{ID: "srv-github", Name: "github", Enabled: true},
	}); err != nil {
		t.Fatalf("write mcp_servers: %v", err)
	}
	if err := st.Write(types.FileGroups, []types.Group{{Name: "group1"}}); err != nil {
		t.Fatalf("write groups: %v", err)
	}

	lg := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &Service{
		st:             st,
		lg:             lg,
		db:             db,
		repoDir:        t.TempDir(),
		builtinTools:   map[string]map[string]*ToolEntry{},
		builtinServers: map[string]types.MCPServer{},
	}
}

// invocationRow 是 mcp_invocations 落库后读取的最小字段集。
type invocationRow struct {
	ToolName      string
	ServerName    string
	Result        string
	AggregateKind string
}

// waitForInvocation 轮询 mcp_invocations 表直到出现一行（recordInvocation 是异步 goroutine 写入）。
// 超时给足 15s（150×100ms）：CI/并行测试下异步 goroutine 调度可能延迟，5s 内偶发超时。
func waitForInvocation(t *testing.T, s *Service) invocationRow {
	t.Helper()
	var row invocationRow
	var serverName sql.NullString
	for i := 0; i < 150; i++ {
		err := s.db.QueryRow(`SELECT tool_name, server_name, result, aggregate_kind
			FROM mcp_invocations ORDER BY id LIMIT 1`).
			Scan(&row.ToolName, &serverName, &row.Result, &row.AggregateKind)
		if err == nil {
			row.ServerName = serverName.String
			return row
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("query mcp_invocations: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("等待 mcp_invocations 落库超时（150×100ms）")
	return row
}

// TestInvokeRecordsInvocation 验证 Invoke 通过 recordInvocation 异步把一行写入 mcp_invocations。
// 用技能工具走成功路径（无需真实上游），断言 tool_name/result/server_name/aggregate_kind。
func TestInvokeRecordsInvocation(t *testing.T) {
	t.Run("成功路径写入 success 记录", func(t *testing.T) {
		s := newTraceTestService(t)
		s.tools = []ToolEntry{
			{Name: "web-design", RawName: "web-design", Description: "网页设计", Category: "skill", Source: "skills", IsSkill: true},
		}

		out, err := s.Invoke(context.Background(), "/mcp/$smart", map[string]any{"tool": "web-design"})
		if err != nil {
			t.Fatalf("Invoke 失败: %v", err)
		}
		if !strings.Contains(out, "web-design") {
			t.Fatalf("技能 invoke 结果应包含工具名，实际: %s", out)
		}

		row := waitForInvocation(t, s)
		if row.ToolName != "web-design" {
			t.Fatalf("tool_name = %q，期望 web-design", row.ToolName)
		}
		if row.Result != "success" {
			t.Fatalf("result = %q，期望 success", row.Result)
		}
		if row.ServerName != "skills" {
			t.Fatalf("server_name = %q，期望 skills", row.ServerName)
		}
		if row.AggregateKind != "$smart" {
			t.Fatalf("aggregate_kind = %q，期望 $smart", row.AggregateKind)
		}
	})

	t.Run("失败路径写入 error 记录", func(t *testing.T) {
		s := newTraceTestService(t)
		s.tools = []ToolEntry{
			{Name: "web-design", RawName: "web-design", Description: "网页设计", Category: "skill", Source: "skills", IsSkill: true},
		}

		if _, err := s.Invoke(context.Background(), "/mcp/$smart", map[string]any{"tool": "nope"}); err == nil {
			t.Fatal("调用不存在的工具应返回错误")
		}

		row := waitForInvocation(t, s)
		if row.ToolName != "nope" {
			t.Fatalf("tool_name = %q，期望 nope（args 里的原始值）", row.ToolName)
		}
		if row.Result != "error" {
			t.Fatalf("result = %q，期望 error（工具不可见消息不匹配 not_found 关键词）", row.Result)
		}
	})
}

// TestClassifyResult 验证错误分类：not_found / timeout / denied / 其他 error。
func TestClassifyResult(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"not found 英文", errors.New("tool not found"), "not_found"},
		{"not_found 下划线", errors.New("mcp: not_found"), "not_found"},
		{"unknown tool", errors.New("unknown tool: foo"), "not_found"},
		{"timeout", errors.New("context deadline exceeded: rpc timeout"), "timeout"},
		{"denied", errors.New("access denied"), "denied"},
		{"forbidden", errors.New("rpc error: forbidden"), "denied"},
		{"permission", errors.New("permission denied for tool"), "denied"},
		{"其他错误", errors.New("boom: 上游挂了"), "error"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyResult(c.err); got != c.want {
				t.Fatalf("classifyResult(%v) = %q，期望 %q", c.err, got, c.want)
			}
		})
	}
}

// TestParseAggregate 验证端点 → (kind, target) 路由解析：$smart / 单 MCP / 分组 / 未知路径兜底。
func TestParseAggregate(t *testing.T) {
	s := newTraceTestService(t)
	cases := []struct {
		endpoint   string
		wantKind   string
		wantTarget string
	}{
		{"/mcp/$smart", "$smart", ""},
		{"/mcp/github", "single", "github"},
		{"/mcp/group1", "group", "group1"},
		{"/mcp/unknown", "", "unknown"},
		{"", "", ""},
	}
	for _, c := range cases {
		kind, target := s.parseAggregate(c.endpoint)
		if kind != c.wantKind || target != c.wantTarget {
			t.Fatalf("parseAggregate(%q) = (%q, %q)，期望 (%q, %q)", c.endpoint, kind, target, c.wantKind, c.wantTarget)
		}
	}
}

// TestParseAggregateBuiltin 验证：内置 server 端点（如 /mcp/multimodal）被识别为
// "builtin" 而非空 kind，否则前端 MCP 工具调用类型列显示「—」。
func TestParseAggregateBuiltin(t *testing.T) {
	s := newTraceTestService(t)
	if err := s.RegisterBuiltinServer(context.Background(),
		types.MCPServer{ID: "builtin-mm", Name: "multimodal", Transport: types.TransportHTTP, Enabled: true, Builtin: true},
		nil); err != nil {
		t.Fatalf("RegisterBuiltinServer: %v", err)
	}
	kind, target := s.parseAggregate("/mcp/multimodal")
	if kind != "builtin" || target != "multimodal" {
		t.Fatalf("parseAggregate(/mcp/multimodal) = (%q, %q)，期望 (builtin, multimodal)", kind, target)
	}
}

// TestCallEntryDirectRecordsInvocation 验证「单 MCP / 分组端点直接暴露工具」路径
// （exposedTools handler → callEntry）同样埋点：这是此前遗漏的调用路径。
func TestCallEntryDirectRecordsInvocation(t *testing.T) {
	s := newTraceTestService(t)
	// 失败路径：ServerID 无对应 upstream → callEntry 报错但仍应落一行 error 记录。
	entry := ToolEntry{Name: "read_file", ServerID: "srv-missing", RawName: "read_file", Source: "github"}
	if _, err := s.callEntry(context.Background(), entry, map[string]any{}, "/mcp/github"); err == nil {
		t.Fatal("预期 upstream 不存在报错")
	}

	row := waitForInvocation(t, s)
	if row.ToolName != "read_file" {
		t.Fatalf("tool_name = %q，期望 read_file", row.ToolName)
	}
	if row.Result != "error" {
		t.Fatalf("result = %q，期望 error", row.Result)
	}
	if row.AggregateKind != "single" {
		t.Fatalf("aggregate_kind = %q，期望 single（端点 /mcp/github 匹配上游名）", row.AggregateKind)
	}

	// 防双记：只有一行
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM mcp_invocations").Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Fatalf("mcp_invocations 行数 = %d，期望 1（callEntry 埋点不得与 invokeWith 双记）", count)
	}
}

// TestInvokeSingleRow 验证 $smart invoke 成功路径恰好落一行（invokeWith 不再
// 自行埋点、由 callEntry 统一埋点，避免双记）。
func TestInvokeSingleRow(t *testing.T) {
	s := newTraceTestService(t)
	s.tools = []ToolEntry{
		{Name: "web-design", RawName: "web-design", Description: "网页设计", Category: "skill", Source: "skills", IsSkill: true},
	}
	if _, err := s.Invoke(context.Background(), "/mcp/$smart", map[string]any{"tool": "web-design"}); err != nil {
		t.Fatalf("Invoke 失败: %v", err)
	}
	_ = waitForInvocation(t, s)
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM mcp_invocations").Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Fatalf("mcp_invocations 行数 = %d，期望 1（成功 invoke 只记一行）", count)
	}
}
