package mcphub

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// McpStats 是 /api/stats/mcp 的 JSON 契约(同时给前端复用)。
type McpStats struct {
	Trend          []McpTrendPoint    `json:"trend"`
	RankAggregates []McpAggregateRank `json:"rank_aggregates"`
	RankTools      []McpToolRank      `json:"rank_tools"`
}

type McpTrendPoint struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

type McpAggregateRank struct {
	Kind   string  `json:"kind"`
	Target *string `json:"target"`
	Calls  int     `json:"calls"`
}

type McpToolRank struct {
	ToolName   string `json:"tool_name"`
	ServerName string `json:"server_name"`
	Calls      int    `json:"calls"`
}

const mcpSchema = `CREATE TABLE IF NOT EXISTS mcp_invocations (
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
  error_message     TEXT
);
CREATE INDEX IF NOT EXISTS idx_mcp_started   ON mcp_invocations(started_at);
CREATE INDEX IF NOT EXISTS idx_mcp_tool      ON mcp_invocations(tool_name, started_at);
CREATE INDEX IF NOT EXISTS idx_mcp_aggregate ON mcp_invocations(aggregate_target, started_at);
`

// migrate 在 *sql.DB 上幂等地建表 + 索引。失败应阻断插件启动。
func migrate(db *sql.DB) error {
	_, err := db.Exec(mcpSchema)
	return err
}

// InvocationRecord 是一次 MCP 工具调用的记录,由 RecordInvocation 写入 mcp_invocations。
type InvocationRecord struct {
	StartedAt       string
	FinishedAt      *string
	AggregateKind   string // 'single' / 'group' / '$smart'
	AggregateTarget *string
	ToolName        string
	ServerName      string
	Result          string // 'success' / 'error' / 'not_found' / 'timeout' / 'denied'
	HTTPStatus      int
	DurationMS      int
	ErrorMessage    string
}

// RecordInvocation 同步插入一行。调用者应放 goroutine 里跑并 recover + log,
// 避免阻塞业务请求。
func (s *Service) RecordInvocation(ctx context.Context, r InvocationRecord) error {
	if s.db == nil {
		// 单测等无 db 环境：静默跳过，不 panic。
		return nil
	}
	if r.StartedAt == "" {
		r.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO mcp_invocations
		(started_at, finished_at, aggregate_kind, aggregate_target, tool_name,
		 server_name, result, http_status, duration_ms, error_message)
		VALUES (?, ?, ?, NULLIF(?, ''), ?, NULLIF(?, ''), ?, NULLIF(?, 0), ?, NULLIF(?, ''))`,
		r.StartedAt, r.FinishedAt, r.AggregateKind,
		targetString(r.AggregateTarget),
		r.ToolName, r.ServerName, r.Result, r.HTTPStatus, r.DurationMS, r.ErrorMessage,
	)
	if err != nil {
		return fmt.Errorf("mcp-hub: insert invocation: %w", err)
	}
	return nil
}

func targetString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// recordInvocation 异步把一次 invoke 调用写入 mcp_invocations：goroutine + recover，
// 失败只记日志，绝不阻塞或改变业务请求路径。
func (s *Service) recordInvocation(startAt string, startTime time.Time, endpoint string, err error, toolName, serverName string) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				if s.lg != nil {
					s.lg.Error("mcp-hub: invoke record panic", "err", r)
				}
			}
		}()
		result := "success"
		errMsg := ""
		httpStatus := 200
		if err != nil {
			result = classifyResult(err)
			errMsg = err.Error()
			httpStatus = 500
		}
		aggKind, aggTarget := s.parseAggregate(endpoint)
		var aggTargetPtr *string
		if aggTarget != "" {
			aggTargetPtr = ptrStr(aggTarget)
		}
		if err := s.RecordInvocation(context.Background(), InvocationRecord{
			StartedAt:       startAt,
			FinishedAt:      ptrStr(time.Now().UTC().Format(time.RFC3339Nano)),
			AggregateKind:   aggKind,
			AggregateTarget: aggTargetPtr,
			ToolName:        toolName,
			ServerName:      serverName,
			Result:          result,
			HTTPStatus:      httpStatus,
			DurationMS:      int(time.Since(startTime).Milliseconds()),
			ErrorMessage:    errMsg,
		}); err != nil {
			s.warn("mcphub: 记录调用统计失败", "err", err)
		}
	}()
}

// classifyResult 把调用错误分类为埋点结果：not_found / timeout / denied / error。
func classifyResult(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "not found"),
		strings.Contains(msg, "not_found"),
		strings.Contains(msg, "unknown tool"):
		return "not_found"
	case strings.Contains(msg, "timeout"):
		return "timeout"
	case strings.Contains(msg, "denied"),
		strings.Contains(msg, "forbidden"),
		strings.Contains(msg, "permission"):
		return "denied"
	default:
		return "error"
	}
}

// ptrStr 返回字符串指针（供 InvocationRecord 可选字段使用）。
func ptrStr(s string) *string { return &s }

// parseAggregate 从端点路径解析聚合路由信息：/mcp/$smart → ("$smart","")，
// /mcp/{mcp名} → ("single", 名)，/mcp/{分组名} → ("group", 名)；未知/解析失败给空 kind。
func (s *Service) parseAggregate(endpoint string) (kind, target string) {
	key := strings.TrimPrefix(endpoint, "/mcp/")
	if key == "$smart" {
		return "$smart", ""
	}
	if key == "" || s.st == nil {
		return "", key
	}
	if servers, err := s.readServers(); err == nil {
		for _, srv := range servers {
			if srv.Name == key {
				return "single", key
			}
		}
	}
	if groups, err := s.readGroups(); err == nil {
		for _, g := range groups {
			if g.Name == key {
				return "group", key
			}
		}
	}
	return "", key
}

// Stats 返回过去 days 天的 trend + 双 top 排行。top<=0 时默认 5。
func (s *Service) Stats(ctx context.Context, days, top int) (*McpStats, error) {
	if top <= 0 {
		top = 5
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339Nano)
	out := &McpStats{
		Trend:          []McpTrendPoint{},
		RankAggregates: []McpAggregateRank{},
		RankTools:      []McpToolRank{},
	}

	// trend: days 天整天返回,缺日期 count=0
	rows, err := s.db.QueryContext(ctx,
		`SELECT substr(started_at, 1, 10) AS day, COUNT(*) AS c
		 FROM mcp_invocations WHERE started_at >= ? GROUP BY day ORDER BY day`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("mcp-hub: trend query: %w", err)
	}
	seen := map[string]int{}
	for rows.Next() {
		var p McpTrendPoint
		if err := rows.Scan(&p.Date, &p.Count); err != nil {
			rows.Close()
			return nil, fmt.Errorf("mcp-hub: trend scan: %w", err)
		}
		seen[p.Date] = p.Count
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("mcp-hub: trend iterate: %w", err)
	}
	rows.Close()
	start, _ := time.Parse("2006-01-02", time.Now().UTC().AddDate(0, 0, -days+1).Format("2006-01-02"))
	for i := 0; i < days; i++ {
		d := start.AddDate(0, 0, i).Format("2006-01-02")
		out.Trend = append(out.Trend, McpTrendPoint{Date: d, Count: seen[d]})
	}

	// rank_aggregates
	rows, err = s.db.QueryContext(ctx,
		`SELECT aggregate_kind, COALESCE(aggregate_target, '$smart') AS target, COUNT(*) AS c
		 FROM mcp_invocations WHERE started_at >= ?
		 GROUP BY aggregate_kind, target ORDER BY c DESC LIMIT ?`, cutoff, top)
	if err != nil {
		return nil, fmt.Errorf("mcp-hub: aggregates query: %w", err)
	}
	for rows.Next() {
		var a McpAggregateRank
		var targetStr string
		if err := rows.Scan(&a.Kind, &targetStr, &a.Calls); err != nil {
			rows.Close()
			return nil, fmt.Errorf("mcp-hub: aggregates scan: %w", err)
		}
		// 防御:非 $smart 行若 target 为空(NULL 被 COALESCE 成 '$smart',
		// 或上游直接写入空串),也视为无 target。
		if a.Kind == "$smart" || targetStr == "" {
			a.Target = nil
		} else {
			t := targetStr
			a.Target = &t
		}
		out.RankAggregates = append(out.RankAggregates, a)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("mcp-hub: aggregates iterate: %w", err)
	}
	rows.Close()

	// rank_tools: 用窗口函数取每个 tool 调用数最大的 server 行,
	// 避免某 tool 分布在 4+ server 时占满 LIMIT 槽位漏掉其他 tool。
	// 需要 SQLite 3.25+(modernc.org/sqlite 内嵌版本满足)。
	rows, err = s.db.QueryContext(ctx,
		`SELECT tool_name, COALESCE(server_name, '') AS server_name, c FROM (
		   SELECT tool_name, server_name, COUNT(*) AS c,
		          ROW_NUMBER() OVER (PARTITION BY tool_name ORDER BY COUNT(*) DESC) AS rn
		   FROM mcp_invocations WHERE started_at >= ?
		   GROUP BY tool_name, server_name
		 ) WHERE rn = 1 ORDER BY c DESC LIMIT ?`, cutoff, top)
	if err != nil {
		return nil, fmt.Errorf("mcp-hub: tools query: %w", err)
	}
	seen2 := map[string]bool{}
	for rows.Next() {
		var r McpToolRank
		if err := rows.Scan(&r.ToolName, &r.ServerName, &r.Calls); err != nil {
			rows.Close()
			return nil, fmt.Errorf("mcp-hub: tools scan: %w", err)
		}
		if seen2[r.ToolName] {
			continue
		}
		seen2[r.ToolName] = true
		out.RankTools = append(out.RankTools, r)
		if len(seen2) >= top {
			break
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("mcp-hub: tools iterate: %w", err)
	}
	rows.Close()

	return out, nil
}
