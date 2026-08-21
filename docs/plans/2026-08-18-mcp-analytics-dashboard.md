# MCP + 模型 统计分析面板 — 实施计划

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Don't change the design without checking with the user; refer to `docs/plans/2026-08-18-mcp-analytics-dashboard-design.md` for context.

**Goal:** 在 OverviewView 加两块统计面板：
1. MCP 调用统计（趋势图 + 聚合服务 Top5 + 工具 Top5）
2. 模型使用情况（5 指标卡 + 命中率环形 + 5 次级卡 + 成功率条 + 趋势折线 + 日历热力图 + 模型分布环形）

**Architecture:** mcp-hub 注入 db（共享 `core/db`），新增 `mcp_invocations` 表 + `Service.RecordInvocation` 异步方法 + `Service.Stats()` 聚合；`admin-api` 注册 `GET /api/stats/mcp`（MCP 维度）+ `GET /api/stats/models`（模型维度，查现有 route-log 表）；前端改造 OverviewView，新增 `McpStatsPanel` 与 `ModelStatsPanel` 两个面板。

**Tech Stack:**
- 后端: Go 1.21+, `database/sql` + SQLite(WAL)
- 前端: Vue 3 + shadcn-vue-cdn + Tailwind v4 + ECharts(`echarts` + `vue-echarts`)

**重要:** 每完成一个任务就 commit。所有命令在仓库根 `D:/Code/Git/loadout` 下运行。

---

## 任务 1: mcp-hub 注入 db + schema 迁移

**Files:**
- Modify: `plugins/mcp-hub/plugin.go` — Manifest.Inject 加 `"db"`
- Modify: `plugins/mcp-hub/service.go` — `NewService` 加 `*sql.DB` 参数
- Create: `plugins/mcp-hub/stats.go` — migration + `Stats()` 占位
- Create: `plugins/mcp-hub/stats_test.go` — 表存在性 + 空 Stats 返回

### Step 1: 写失败测试

`plugins/mcp-hub/stats_test.go`:

```go
package mcphub

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
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
	got, err := s.Stats(30, 5)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if got == nil || len(got.Trend) != 0 || len(got.RankAggregates) != 0 || len(got.RankTools) != 0 {
		t.Fatalf("empty stats expected, got %+v", got)
	}
}
```

**注意**: 若 `modernc.org/sqlite` 不在 go.mod，改用项目现有 sqlite driver（查看 `core/db` 用什么 driver，保持一致；Go 测试里可直接用同一 driver 名）。

### Step 2: 跑测试确认失败

```bash
cd plugins/mcp-hub && go test -run 'TestStatsTableExists|TestStatsEmpty' ./...
```

Expected: FAIL（`migrate` / `Stats` 未定义，或 `Service{db}` 缺字段）。

### Step 3: 实现 `stats.go`

`plugins/mcp-hub/stats.go`:

```go
package mcphub

import (
	"context"
	"database/sql"
)

// McpStats 是 /api/stats/mcp 的 JSON 契约(同时给前端复用)。
type McpStats struct {
	Trend          []McpTrendPoint     `json:"trend"`
	RankAggregates []McpAggregateRank `json:"rank_aggregates"`
	RankTools      []McpToolRank       `json:"rank_tools"`
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

// Stats 占位实现,后续任务会替换为完整 SQL。
func (s *Service) Stats(_ context.Context, _, _ int) (*McpStats, error) {
	return &McpStats{
		Trend:          []McpTrendPoint{},
		RankAggregates: []McpAggregateRank{},
		RankTools:      []McpToolRank{},
	}, nil
}
```

### Step 4: 让 Service 支持 db 字段

修改 `plugins/mcp-hub/service.go`，Service struct 加 `db *sql.DB`，`NewService` 接受并初始化（看现有签名而定，若 `NewService(*store.Store, *slog.Logger)` 改为加一个 `*sql.DB` 参数）。更新所有现有 `NewService(...)` 调用点（plugin.go 及其他测试）。

修改 `plugins/mcp-hub/plugin.go`:

```go
func (p *mcpHubPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:    "mcp-hub",
		Version: "0.1.0",
		Inject:  []string{"store", "logger", "db"},  // 增 db
		Provide: []string{"mcp-hub"},
	}
}

func (p *mcpHubPlugin) Apply(ctx plugin.Context) error {
	st := ctx.Get("store").(*store.Store)
	lg := ctx.Get("logger").(*slog.Logger)
	db := ctx.Get("db").(*sql.DB)
	svc := NewService(st, lg, db)
	if err := migrate(db); err != nil {
		return fmt.Errorf("mcp-hub: migrate: %w", err)
	}
	ctx.Set("mcp-hub", svc)
	return nil
}
```

**注意**: 若 core/plugin 容器里没有 "db" 服务名，先看 `plugins/admin-api/plugin.go` 怎么拿 db（`require[*sql.DB](ctx, "db")`）→ 容器里确实有 `"db"` 这个 key。确认 `core/plugin` 的 Manifest 支持 `Inject: []string{"db"}` 即可。

### Step 5: 跑测试通过

```bash
cd plugins/mcp-hub && go test -run 'TestStatsTableExists|TestStatsEmpty' ./...
```

Expected: PASS。

### Step 6: commit

```bash
git add plugins/mcp-hub/plugin.go plugins/mcp-hub/service.go plugins/mcp-hub/stats.go plugins/mcp-hub/stats_test.go
git commit -m "feat(mcp-hub): 注入 db + 创建 mcp_invocations schema + Stats() 占位"
```

---

## 任务 2: mcp-hub RecordInvocation + Stats() 完整实现

**Files:**
- Modify: `plugins/mcp-hub/stats.go` — 新增 `RecordInvocation` + 完整 `Stats` 查询

### Step 1: 加失败测试

`plugins/mcp-hub/stats_test.go` 末尾追加（文件头加 `import "context"`）:

```go
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
		{"single", "test", "exec", "shell"},
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

func ptrStr(s string) *string { return &s }
```

### Step 2: 跑测试确认失败

```bash
cd plugins/mcp-hub && go test -run 'TestRecordAndQueryStats' ./...
```

Expected: FAIL（`RecordInvocation` / `InvocationRecord` 未定义）。

### Step 3: 实现完整 `RecordInvocation` + `Stats`

替换 `plugins/mcp-hub/stats.go` 的占位部分:

```go
package mcphub

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type InvocationRecord struct {
	StartedAt       string
	FinishedAt      *string
	AggregateKind   string  // 'single' / 'group' / '$smart'
	AggregateTarget *string
	ToolName        string
	ServerName      string
	Result          string
	HTTPStatus      int
	DurationMS      int
	ErrorMessage    string
}

// RecordInvocation 同步插入一行。设计要求"调用必须不阻断业务请求",
// 调用者应当放在 goroutine 里跑并 recover + log。
func (s *Service) RecordInvocation(ctx context.Context, r InvocationRecord) error {
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

	// trend: 30 天整天返回,缺日期 count=0
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
			return nil, err
		}
		seen[p.Date] = p.Count
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
			return nil, err
		}
		if a.Kind == "$smart" {
			a.Target = nil
		} else {
			t := targetStr
			a.Target = &t
		}
		out.RankAggregates = append(out.RankAggregates, a)
	}
	rows.Close()

	// rank_tools: GROUP BY tool_name, server_name; 同名多 server 取第一个(calls 最大者先出现)
	rows, err = s.db.QueryContext(ctx,
		`SELECT tool_name, server_name, COUNT(*) AS c
		 FROM mcp_invocations WHERE started_at >= ?
		 GROUP BY tool_name, server_name ORDER BY c DESC LIMIT ?`, cutoff, top*3)
	if err != nil {
		return nil, fmt.Errorf("mcp-hub: tools query: %w", err)
	}
	seen2 := map[string]bool{}
	for rows.Next() {
		var r McpToolRank
		if err := rows.Scan(&r.ToolName, &r.ServerName, &r.Calls); err != nil {
			rows.Close()
			return nil, err
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
	rows.Close()

	return out, nil
}
```

### Step 4: 跑测试通过

```bash
cd plugins/mcp-hub && go test -v ./...
```

Expected: PASS。

### Step 5: commit

```bash
git add plugins/mcp-hub/stats.go plugins/mcp-hub/stats_test.go
git commit -m "feat(mcp-hub): RecordInvocation + 完整 Stats 聚合查询"
```

---

## 任务 3: mcp-hub Invoke() 路径末尾调用埋点

**Files:**
- Modify: `plugins/mcp-hub/service.go` — 在 Invoke() 末尾 goroutine + recover + log

**背景**: 目标是在 `Invoke()` 处理路径的 return 之前触发一次 `RecordInvocation`。但**不要改变 Invoke 的现有签名/逻辑**，只在合适位置加 hook。若 Invoke 内部是多处 return，优先用 `defer` 收集结果后再记录，或包装一次结果结构。

### Step 1: 打开 service.go 找到 Invoke 方法

```bash
cd plugins/mcp-hub && grep -n "func (s \*Service) Invoke" service.go
```

读 Invoke 完整代码，确认：
- 入参里能拿到 tool_name / aggregate_kind / aggregate_target / server_name / result / http_status / error_message 的字段名
- start 时间点

### Step 2: 在 Invoke 里加埋点

模式（以实际代码对齐）:

```go
func (s *Service) Invoke(ctx context.Context, req InvokeRequest) (InvokeResponse, error) {
	startAt := time.Now().UTC().Format(time.RFC3339Nano)
	startTime := time.Now()
	// ...现有逻辑...
	resp, err := s.doSomething(req)
	// 埋点:异步,失败不阻断
	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.lg.Error("mcp-hub: invoke record panic", "err", r)
			}
		}()
		result := "success"
		errMsg := ""
		httpStatus := 200
		if err != nil {
			result = classifyResult(err) // not_found / timeout / error 等,看实际错误类型
			errMsg = err.Error()
			httpStatus = 500
		}
		_ = s.RecordInvocation(context.Background(), InvocationRecord{
			StartedAt:       startAt,
			FinishedAt:      ptrStr(time.Now().UTC().Format(time.RFC3339Nano)),
			AggregateKind:   req.AggregateKind,     // 对齐实际字段名
			AggregateTarget: req.AggregateTarget,
			ToolName:        req.ToolName,
			ServerName:      req.ServerName,
			Result:          result,
			HTTPStatus:      httpStatus,
			DurationMS:      int(time.Since(startTime).Milliseconds()),
			ErrorMessage:    errMsg,
		})
	}()
	return resp, err
}
```

**重要**: `classifyResult` 简单实现——若错误包含 "not found"/"not_found" → `not_found`；包含 "timeout" → `timeout`；包含 "denied"/"forbidden" → `denied`；否则 `error`。放 stats.go 里。

### Step 3: 编译验证

```bash
cd plugins/mcp-hub && go build ./...
```

Expected: 无错误。

### Step 4: 跑全部 mcp-hub 测试

```bash
cd plugins/mcp-hub && go test ./...
```

Expected: PASS。

### Step 5: commit

```bash
git add plugins/mcp-hub/service.go plugins/mcp-hub/stats.go
git commit -m "feat(mcp-hub): Invoke 路径异步埋点 mcp_invocations"
```

---

## 任务 4: admin-api 注册 GET /api/stats/mcp

**Files:**
- Modify: `plugins/admin-api/service.go` — Service struct 加 mcpHub 引用 + Routes() 注册 + handler
- Create: `plugins/admin-api/stats_test.go` — 集成测试

### Step 1: 写失败测试

`plugins/admin-api/stats_test.go`（复用 admin_api_test.go 的 helper: `newTestAPI` / `loginAndCookie` / `apiReq` / `cookie`）:

```go
package adminapi

import (
	"net/http"
	"strings"
	"testing"
)

func TestStatsMcpEndpoint(t *testing.T) {
	ts, _ := newTestAPI(t)
	defer ts.Close()
	cookie := loginAndCookie(t, ts)

	// 直接往 db 塞一行(若有 db 句柄可用;否则跳过,接口空数组也算过)
	resp, data := apiReq(t, ts, http.MethodGet, "/api/stats/mcp?days=30&top=5", nil, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, data)
	}
	for _, key := range []string{`"trend"`, `"rank_aggregates"`, `"rank_tools"`} {
		if !strings.Contains(data, key) {
			t.Fatalf("missing key %s in %s", key, data)
		}
	}
}
```

**注意**: `newTestAPI` 返回的实例要能拿到 mcp-hub Service（test harness 里已装配）。若 harness 没装配 mcp-hub，给 test 单独 fake。若太复杂，把断言放宽为"接口返回 200 且含三键"（空数据也过）。

### Step 2: 跑测试确认失败

```bash
cd plugins/admin-api && go test -run TestStatsMcpEndpoint ./...
```

Expected: FAIL（404）。

### Step 3: 实现

在 `plugins/admin-api/service.go` 的 Service struct 里确保有 mcpHub 引用（看现有 struct 是否有 `hub *mcphub.Service` 或类似字段——`plugin.go` 里已经 `require[*mcphub.Service](ctx, "mcp-hub")` 且传给 NewService）。若 NewService 已接收 hub 并存字段，直接用它。

`Routes()` 里新增:

```go
{
    Method:  http.MethodGet,
    Path:    "/api/stats/mcp",
    Handler: s.handleStatsMcp,
},
```

Handler:

```go
func (s *Service) handleStatsMcp(w http.ResponseWriter, r *http.Request) {
	if s.hub == nil {
		writeError(w, http.StatusServiceUnavailable, "mcp-hub 未装配")
		return
	}
	days, top := 30, 5
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 365 {
			days = n
		}
	}
	if v := r.URL.Query().Get("top"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 50 {
			top = n
		}
	}
	stats, err := s.hub.Stats(r.Context(), days, top)
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}
```

### Step 4: 跑测试通过

```bash
cd plugins/admin-api && go test -run TestStatsMcpEndpoint ./...
```

Expected: PASS。

### Step 5: 跑全部 admin-api 测试

```bash
cd plugins/admin-api && go test ./...
```

Expected: 全部 PASS。

### Step 6: commit

```bash
git add plugins/admin-api/service.go plugins/admin-api/stats_test.go
git commit -m "feat(admin-api): GET /api/stats/mcp 返回 McpStats JSON"
```

---

## 任务 5: admin-api 注册 GET /api/stats/models（模型维度，查 route-log）

**Files:**
- Modify: `plugins/admin-api/service.go` — Routes() + handler
- Modify: `plugins/admin-api/stats_test.go` — 加模型端点测试

### Step 1: 写失败测试

`plugins/admin-api/stats_test.go` 末尾追加:

```go
func TestStatsModelsEndpoint(t *testing.T) {
	ts, _ := newTestAPI(t)
	defer ts.Close()
	cookie := loginAndCookie(t, ts)

	resp, data := apiReq(t, ts, http.MethodGet, "/api/stats/models?days=30", nil, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, data)
	}
	for _, key := range []string{`"summary"`, `"hit_rate"`, `"trend"`, `"calendar"`, `"model_dist"`} {
		if !strings.Contains(data, key) {
			t.Fatalf("missing key %s in %s", key, data)
		}
	}
}
```

### Step 2: 跑测试确认失败

```bash
cd plugins/admin-api && go test -run TestStatsModelsEndpoint ./...
```

Expected: FAIL（404）。

### Step 3: 实现 handler

在 service.go 加（用现有 `s.routeLog` 字段，contracts.RouteLog 接口; 若接口没有聚合方法，需要扩 contracts 或在 handler 里直接查 sql.DB——**优先看 route-log 是否有现成 List() 全量拉出来内存聚合的可行性，MVP 数据量小可接受**。若 List 支持 limit<=0 或很大，拉 30 天全量内存聚合即可，避免动 contracts）:

```go
func (s *Service) handleStatsModels(w http.ResponseWriter, r *http.Request) {
	if s.routeLog == nil {
		writeError(w, http.StatusServiceUnavailable, "route-log 未装配")
		return
	}
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 365 {
			days = n
		}
	}
	// 方案 1(推荐,MVP):拉 30 天日志内存聚合
	//   1) List(ctx, filter{StartedAfter: cutoff, Limit: 很大}) 取全量
	//   2) 遍历聚合 summary/hit_rate/trend/calendar/model_dist
	// 方案 2(性能好):直接对 route_requests 写 SQL(需暴露 db 或扩展 contracts)
	stats := aggregateModelStats(logs)
	writeJSON(w, http.StatusOK, stats)
}
```

**具体实现**：`aggregateModelStats(logs []contracts.RouteRequestView) *ModelStats` 放 `plugins/admin-api/stats_models.go`，纯函数易测。聚合逻辑:
- summary: requests=len, prompt_tokens/completion_tokens/cached_tokens/total_tokens 累加, success_rate=success/len, avg_duration_ms=AVG(duration_ms)(仅非 running), failed=非 success 数
- hit_rate: input=cached/prompt(若无 prompt 则 0), output=0, total=cached/(prompt+completion)
- trend: map[date] += tokens,补 30 天
- calendar: map[date] += tokens(同 trend 数据源)
- model_dist: map[final_model] 累加,按 tokens 降序

返回结构类型 `ModelStats` 同设计文档 §5.2 的 JSON 契约。

### Step 4: 跑测试通过

```bash
cd plugins/admin-api && go test -run 'TestStatsModelsEndpoint|TestStatsMcpEndpoint' ./...
```

Expected: PASS。

### Step 5: 跑全部 admin-api 测试

```bash
cd plugins/admin-api && go test ./...
```

Expected: 全部 PASS。

### Step 6: commit

```bash
git add plugins/admin-api/service.go plugins/admin-api/stats_models.go plugins/admin-api/stats_test.go
git commit -m "feat(admin-api): GET /api/stats/models 聚合 route-log 返回模型使用情况"
```

---

## 任务 6: 前端装包 + 类型 + API client

**Files:**
- Modify: `frontend/package.json` — echarts / vue-echarts
- Modify: `frontend/src/lib/types.ts` — McpStats + ModelStats 全套类型
- Modify: `frontend/src/lib/api.ts` — getMcpStats + getModelStats

### Step 1: 装包

```bash
cd frontend && pnpm add echarts vue-echarts
```

Expected: package.json + pnpm-lock.yaml 自动更新。

### Step 2: 加类型

`frontend/src/lib/types.ts` 末尾追加（两个面板全部类型）:

```ts
// ---- MCP 维度 ----
export interface McpTrendPoint { date: string; count: number }
export interface McpAggregateRank { kind: 'single' | 'group' | '$smart'; target: string | null; calls: number }
export interface McpToolRank { tool_name: string; server_name: string; calls: number }
export interface McpStats {
  trend: McpTrendPoint[]
  rank_aggregates: McpAggregateRank[]
  rank_tools: McpToolRank[]
}

// ---- 模型维度 ----
export interface ModelSummary {
  requests: number
  prompt_tokens: number
  completion_tokens: number
  cached_tokens: number
  total_tokens: number
  success_rate: number
  avg_duration_ms: number
  failed: number
}
export interface ModelHitRate { input: number; output: number; total: number }
export interface ModelTrendPoint {
  date: string
  requests: number
  prompt_tokens: number
  completion_tokens: number
  cached_tokens: number
  total_tokens: number
}
export interface ModelCalendarPoint { date: string; tokens: number }
export interface ModelDistPoint {
  model: string
  calls: number
  tokens: number
  prompt_tokens: number
  completion_tokens: number
  cached_tokens: number
}
export interface ModelStats {
  summary: ModelSummary
  hit_rate: ModelHitRate
  trend: ModelTrendPoint[]
  calendar: ModelCalendarPoint[]
  model_dist: ModelDistPoint[]
}
```

### Step 3: 加 API client

`frontend/src/lib/api.ts` 末尾追加（对齐现有 api() 形态）:

```ts
import type { McpStats, ModelStats } from './types'

export const getMcpStats = (opts: { days?: number; top?: number } = {}) => {
  const params = new URLSearchParams()
  if (opts.days) params.set('days', String(opts.days))
  if (opts.top) params.set('top', String(opts.top))
  const qs = params.toString()
  return api<McpStats>(`/api/stats/mcp${qs ? '?' + qs : ''}`)
}

export const getModelStats = (opts: { days?: number } = {}) => {
  const params = new URLSearchParams()
  if (opts.days) params.set('days', String(opts.days))
  const qs = params.toString()
  return api<ModelStats>(`/api/stats/models${qs ? '?' + qs : ''}`)
}
```

### Step 4: 构建验证

```bash
cd frontend && pnpm exec vue-tsc --noEmit
```

Expected: 无类型错误。

### Step 5: commit

```bash
git add frontend/package.json frontend/pnpm-lock.yaml frontend/src/lib/types.ts frontend/src/lib/api.ts
git commit -m "feat(frontend): echarts 依赖 + Mcp/Model Stats 类型 + API client"
```

---

## 任务 7: 前端 composables

**Files:**
- Create: `frontend/src/composables/useMcpStats.ts`
- Create: `frontend/src/composables/useModelStats.ts`

### Step 1: useMcpStats.ts

```ts
import { onMounted, ref } from 'vue'
import { getMcpStats } from '@/lib/api'
import type { McpStats } from '@/lib/types'

export function useMcpStats() {
  const stats = ref<McpStats | null>(null)
  const loading = ref(false)
  const error = ref<string | null>(null)

  async function refresh(opts: { days?: number; top?: number } = {}) {
    loading.value = true
    error.value = null
    try {
      stats.value = await getMcpStats(opts)
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e)
      stats.value = null
    } finally {
      loading.value = false
    }
  }

  onMounted(() => {
    void refresh()
  })

  return { stats, loading, error, refresh }
}
```

### Step 2: useModelStats.ts（同构，调 getModelStats）

### Step 3: 类型检查

```bash
cd frontend && pnpm exec vue-tsc --noEmit
```

Expected: 无错误。

### Step 4: commit

```bash
git add frontend/src/composables/useMcpStats.ts frontend/src/composables/useModelStats.ts
git commit -m "feat(frontend): useMcpStats + useModelStats composables"
```

---

## 任务 8: MCP 面板组件（TrendChart + AggregateRank + ToolRank + McpStatsPanel）

**Files:**
- Create: `frontend/src/components/McpStatsPanel/TrendChart.vue`
- Create: `frontend/src/components/McpStatsPanel/AggregateRank.vue`
- Create: `frontend/src/components/McpStatsPanel/ToolRank.vue`
- Create: `frontend/src/components/McpStatsPanel/McpStatsPanel.vue`

### Step 1: TrendChart.vue

```vue
<script setup lang="ts">
import { computed } from 'vue'
import VChart from 'vue-echarts'
import { use } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { LineChart } from 'echarts/charts'
import { GridComponent, TooltipComponent } from 'echarts/components'
import type { McpTrendPoint } from '@/lib/types'

use([CanvasRenderer, LineChart, GridComponent, TooltipComponent])

const props = defineProps<{ data: McpTrendPoint[]; loading?: boolean }>()

const option = computed(() => ({
  grid: { left: 40, right: 16, top: 16, bottom: 32 },
  tooltip: { trigger: 'axis' as const },
  xAxis: {
    type: 'category' as const,
    data: props.data.map((p) => p.date.slice(5)),
    axisLine: { lineStyle: { color: '#cbd5e1' } },
    axisLabel: { color: '#64748b', fontSize: 11 },
  },
  yAxis: {
    type: 'value' as const,
    splitLine: { lineStyle: { color: '#e2e8f0' } },
    axisLabel: { color: '#64748b', fontSize: 11 },
  },
  series: [{
    type: 'line' as const,
    data: props.data.map((p) => p.count),
    smooth: true,
    symbol: 'circle',
    symbolSize: 6,
    lineStyle: { color: '#f59e0b', width: 2 },
    itemStyle: { color: '#f59e0b' },
    areaStyle: {
      color: {
        type: 'linear' as const, x: 0, y: 0, x2: 0, y2: 1,
        colorStops: [
          { offset: 0, color: 'rgba(245, 158, 11, 0.45)' },
          { offset: 1, color: 'rgba(245, 158, 11, 0.05)' },
        ],
      },
    },
  }],
}))
</script>

<template>
  <div class="h-64">
    <div v-if="loading && data.length === 0" class="flex h-full items-center justify-center text-sm text-muted-foreground">加载中…</div>
    <div v-else-if="data.length === 0" class="flex h-full items-center justify-center text-sm text-muted-foreground">暂无数据</div>
    <VChart v-else :option="option" autoresize />
  </div>
</template>
```

### Step 2: AggregateRank.vue

```vue
<script setup lang="ts">
import { computed } from 'vue'
import { RiFlowChart } from '@remixicon/vue'
import type { McpAggregateRank } from '@/lib/types'

const props = defineProps<{ items: McpAggregateRank[]; loading?: boolean }>()
const rows = computed(() =>
  props.items.slice(0, 5).map((it, i) => ({
    rank: i + 1,
    name: it.target ?? '$smart',
    kind: it.kind,
    calls: it.calls,
    badgeLabel: `${it.calls} 次调用`,
  })),
)
</script>

<template>
  <Card class="rounded-md">
    <CardHeader>
      <CardTitle class="flex items-center gap-2 text-base"><RiFlowChart size="18" />聚合服务调用排行</CardTitle>
      <CardDescription>近 30 天</CardDescription>
    </CardHeader>
    <CardContent class="space-y-3">
      <div v-if="loading && rows.length === 0" class="py-8 text-center text-sm text-muted-foreground">加载中…</div>
      <div v-else-if="rows.length === 0" class="py-8 text-center text-sm text-muted-foreground">暂无调用</div>
      <div v-for="row in rows" :key="`${row.kind}-${row.name}`" class="flex items-center gap-3 rounded-md border px-3 py-2">
        <span class="w-12 shrink-0 text-xs uppercase text-muted-foreground">TOP {{ row.rank }}</span>
        <div class="min-w-0 flex-1">
          <div class="truncate font-medium">{{ row.name }}</div>
          <div class="text-xs text-muted-foreground">{{ row.kind === '$smart' ? '智能路由' : '上游服务' }}</div>
        </div>
        <Badge variant="secondary" class="shrink-0 rounded-md bg-orange-500 text-white hover:bg-orange-500">{{ row.badgeLabel }}</Badge>
      </div>
    </CardContent>
  </Card>
</template>
```

### Step 3: ToolRank.vue（同构，标题"工具调用排行"，RiToolsLine，显示 server_name）

### Step 4: McpStatsPanel.vue 容器

```vue
<script setup lang="ts">
import { RiLineChartLine } from '@remixicon/vue'
import { useMcpStats } from '@/composables/useMcpStats'
import TrendChart from './TrendChart.vue'
import AggregateRank from './AggregateRank.vue'
import ToolRank from './ToolRank.vue'

const { stats, loading, refresh } = useMcpStats()
</script>

<template>
  <Card class="rounded-md">
    <CardHeader>
      <CardTitle class="flex items-center gap-2 text-base"><RiLineChartLine size="18" />MCP 调用统计</CardTitle>
      <CardDescription>聚合网关近 30 天使用情况</CardDescription>
    </CardHeader>
    <CardContent class="space-y-6">
      <TrendChart :data="stats?.trend ?? []" :loading="loading" />
      <div class="grid gap-4 lg:grid-cols-2">
        <AggregateRank :items="stats?.rank_aggregates ?? []" :loading="loading" />
        <ToolRank :items="stats?.rank_tools ?? []" :loading="loading" />
      </div>
    </CardContent>
  </Card>
</template>
```

### Step 5: 类型检查

```bash
cd frontend && pnpm exec vue-tsc --noEmit
```

Expected: 无错误。

### Step 6: commit

```bash
git add frontend/src/components/McpStatsPanel/
git commit -m "feat(frontend): MCP 统计面板(趋势图 + 双 Top5)"
```

---

## 任务 9: 模型面板子组件（7 个）

**Files:**
- Create: `frontend/src/components/ModelStatsPanel/ModelSummaryCards.vue`
- Create: `frontend/src/components/ModelStatsPanel/ModelHitRateCard.vue`
- Create: `frontend/src/components/ModelStatsPanel/ModelSecondaryCards.vue`
- Create: `frontend/src/components/ModelStatsPanel/ModelSuccessBar.vue`
- Create: `frontend/src/components/ModelStatsPanel/ModelTrendChart.vue`
- Create: `frontend/src/components/ModelStatsPanel/ModelCalendar.vue`
- Create: `frontend/src/components/ModelStatsPanel/ModelDistChart.vue`

### Step 1: ModelSummaryCards.vue — 5 指标卡

```vue
<script setup lang="ts">
import { computed } from 'vue'
import type { ModelStats } from '@/lib/types'

const props = defineProps<{ stats: ModelStats | null }>()
const cards = computed(() => {
  const s = props.stats?.summary
  if (!s) return []
  return [
    { label: '消耗积分', value: formatTokens(s.total_tokens), sub: '以 Token 计(本地无积分)', icon: 'coins' },
    { label: '输入', value: formatTokens(s.prompt_tokens) },
    { label: '输出', value: formatTokens(s.completion_tokens) },
    { label: '总 Token', value: formatTokens(s.total_tokens) },
    { label: '请求数量', value: s.requests.toLocaleString() },
  ]
})
function formatTokens(n: number) {
  if (n >= 1e9) return (n / 1e9).toFixed(2) + 'B'
  if (n >= 1e6) return (n / 1e6).toFixed(2) + 'M'
  if (n >= 1e3) return (n / 1e3).toFixed(1) + 'K'
  return String(n)
}
</script>

<template>
  <div class="grid gap-3 sm:grid-cols-2 xl:grid-cols-5">
    <div v-for="c in cards" :key="c.label" class="rounded-md border bg-card p-4">
      <div class="text-xs text-muted-foreground">{{ c.label }}</div>
      <div class="mt-1 text-xl font-semibold">{{ c.value }}</div>
      <div v-if="c.sub" class="mt-1 text-xs text-muted-foreground">{{ c.sub }}</div>
    </div>
  </div>
</template>
```

### Step 2: ModelHitRateCard.vue — 命中率环形

```vue
<script setup lang="ts">
import { computed } from 'vue'
import VChart from 'vue-echarts'
import { use } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { PieChart } from 'echarts/charts'
import { TooltipComponent } from 'echarts/components'
import type { ModelStats } from '@/lib/types'

use([CanvasRenderer, PieChart, TooltipComponent])

const props = defineProps<{ stats: ModelStats | null }>()

const option = computed(() => {
  const h = props.stats?.hit_rate
  const total = h?.total ?? 0
  return {
    tooltip: { trigger: 'item' as const },
    series: [{
      type: 'pie' as const,
      radius: ['62%', '82%'],
      avoidLabelOverlap: false,
      label: { show: false },
      data: [
        { value: Math.round(total * 100), name: '命中', itemStyle: { color: '#22c55e' } },
        { value: Math.round((1 - total) * 100), name: '未命中', itemStyle: { color: '#e2e8f0' } },
      ],
    }],
  }
})
const detail = computed(() => {
  const h = props.stats?.hit_rate
  if (!h) return []
  return [
    { label: '输入命中', rate: h.input },
    { label: '输出命中', rate: h.output },
    { label: '总命中', rate: h.total },
  ]
})
</script>

<template>
  <Card class="rounded-md">
    <CardHeader>
      <CardTitle class="text-base">命中率统计</CardTitle>
    </CardHeader>
    <CardContent class="flex items-center gap-6">
      <div class="h-36 w-36 shrink-0"><VChart :option="option" autoresize /></div>
      <div class="flex-1 space-y-2">
        <div v-for="d in detail" :key="d.label" class="flex items-center justify-between text-sm">
          <span class="text-muted-foreground">{{ d.label }}</span>
          <span class="font-medium">{{ (d.rate * 100).toFixed(1) }}%</span>
        </div>
      </div>
    </CardContent>
  </Card>
</template>
```

### Step 3: ModelSecondaryCards.vue — 5 次级卡（总请求数/总Token/总消耗/成功率/平均耗时）

```vue
<script setup lang="ts">
import { computed } from 'vue'
import type { ModelStats } from '@/lib/types'

const props = defineProps<{ stats: ModelStats | null }>()
const cards = computed(() => {
  const s = props.stats?.summary
  if (!s) return []
  return [
    { label: '总请求数', value: s.requests.toLocaleString() },
    { label: '总 Token', value: formatTokens(s.total_tokens) },
    { label: '总消耗', value: formatTokens(s.total_tokens), sub: '以 Token 计' },
    { label: '成功率', value: (s.success_rate * 100).toFixed(1) + '%' },
    { label: '平均耗时', value: s.avg_duration_ms.toFixed(0) + 'ms' },
  ]
})
function formatTokens(n: number) {
  if (n >= 1e6) return (n / 1e6).toFixed(2) + 'M'
  if (n >= 1e3) return (n / 1e3).toFixed(1) + 'K'
  return String(n)
}
</script>

<template>
  <div class="grid gap-3 sm:grid-cols-2 xl:grid-cols-5">
    <div v-for="c in cards" :key="c.label" class="rounded-md border bg-card p-4">
      <div class="text-xs text-muted-foreground">{{ c.label }}</div>
      <div class="mt-1 text-xl font-semibold">{{ c.value }}</div>
      <div v-if="c.sub" class="mt-1 text-xs text-muted-foreground">{{ c.sub }}</div>
    </div>
  </div>
</template>
```

### Step 4: ModelSuccessBar.vue — 成功率进度条

```vue
<script setup lang="ts">
import { computed } from 'vue'
import type { ModelStats } from '@/lib/types'

const props = defineProps<{ stats: ModelStats | null }>()
const rate = computed(() => props.stats?.summary.success_rate ?? 0)
const failed = computed(() => props.stats?.summary.failed ?? 0)
const total = computed(() => props.stats?.summary.requests ?? 0)
</script>

<template>
  <Card class="rounded-md">
    <CardHeader>
      <CardTitle class="text-base">请求结果</CardTitle>
    </CardHeader>
    <CardContent class="space-y-2">
      <div class="flex h-3 w-full overflow-hidden rounded-full bg-muted">
        <div class="bg-green-500 transition-all" :style="{ width: `${rate * 100}%` }" />
        <div class="bg-red-500 transition-all" :style="{ width: `${(1 - rate) * 100}%` }" />
      </div>
      <div class="flex justify-between text-xs text-muted-foreground">
        <span>成功 {{ total - failed }}</span>
        <span>失败 {{ failed }}</span>
      </div>
    </CardContent>
  </Card>
</template>
```

### Step 5: ModelTrendChart.vue — 每日消耗折线（total_tokens）

同 TrendChart.vue 结构，但数据用 `props.stats.trend`，series 显示 `total_tokens`（format 用 K/M/B），标题"每日 Token 消耗"。

### Step 6: ModelCalendar.vue — Token 消耗日历热力图

```vue
<script setup lang="ts">
import { computed } from 'vue'
import type { ModelCalendarPoint } from '@/lib/types'

const props = defineProps<{ calendar: ModelCalendarPoint[] }>()

// 按月份分组渲染月份网格(取最近 30 天,横轴为日期序号,底色随 tokens 深浅)
const max = computed(() => Math.max(1, ...props.calendar.map((c) => c.tokens)))
function color(tokens: number) {
  const ratio = tokens / max.value
  // 深浅橙: 0 -> #fff7ed, 1 -> #ea580c
  const alpha = 0.12 + ratio * 0.88
  return `rgba(234, 88, 12, ${alpha})`
}
</script>

<template>
  <Card class="rounded-md">
    <CardHeader><CardTitle class="text-base">Token 消耗日历</CardTitle><CardDescription>近 30 天(以 Token 计,本地无积分)</CardDescription></CardHeader>
    <CardContent>
      <div class="grid grid-cols-15 gap-1.5">
        <div
          v-for="(d, i) in calendar"
          :key="d.date"
          class="flex aspect-square items-center justify-center rounded text-[10px]"
          :style="{ backgroundColor: color(d.tokens) }"
          :title="`${d.date}: ${d.tokens.toLocaleString()} tokens`"
        >{{ d.date.slice(8) }}</div>
      </div>
    </CardContent>
  </Card>
</template>
```

### Step 7: ModelDistChart.vue — 模型分布环形 + 列表

```vue
<script setup lang="ts">
import { computed } from 'vue'
import VChart from 'vue-echarts'
import { use } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { PieChart } from 'echarts/charts'
import { TooltipComponent } from 'echarts/components'
import type { ModelDistPoint } from '@/lib/types'

use([CanvasRenderer, PieChart, TooltipComponent])

const props = defineProps<{ items: ModelDistPoint[] }>()

const palette = ['#6366f1', '#f59e0b', '#10b981', '#ef4444', '#8b5cf6', '#06b6d4', '#f43f5e', '#84cc16']
const option = computed(() => ({
  tooltip: { trigger: 'item' as const },
  legend: { bottom: 0, textStyle: { fontSize: 11 } },
  series: [{
    type: 'pie' as const,
    radius: ['40%', '68%'],
    data: props.items.map((d, i) => ({
      name: d.model,
      value: d.tokens,
      itemStyle: { color: palette[i % palette.length] },
    })),
  }],
}))
const rows = computed(() => props.items.map((d, i) => ({ rank: i + 1, ...d })))
</script>

<template>
  <Card class="rounded-md">
    <CardHeader><CardTitle class="text-base">模型消耗分布</CardTitle></CardHeader>
    <CardContent class="space-y-4">
      <div v-if="items.length === 0" class="py-8 text-center text-sm text-muted-foreground">暂无数据</div>
      <template v-else>
        <div class="h-56"><VChart :option="option" autoresize /></div>
        <div class="space-y-2">
          <div v-for="r in rows" :key="r.model" class="flex items-center gap-2 text-sm">
            <span class="w-8 shrink-0 text-xs text-muted-foreground">#{{ r.rank }}</span>
            <span class="flex-1 truncate">{{ r.model }}</span>
            <span class="text-xs text-muted-foreground">{{ r.calls }} 次</span>
            <span class="font-medium">{{ (r.tokens / 1000).toFixed(1) }}K tokens</span>
          </div>
        </div>
      </template>
    </CardContent>
  </Card>
</template>
```

### Step 8: 类型检查

```bash
cd frontend && pnpm exec vue-tsc --noEmit
```

Expected: 无错误。

### Step 9: commit

```bash
git add frontend/src/components/ModelStatsPanel/
git commit -m "feat(frontend): 模型使用情况子组件(指标卡/命中率/次级卡/成功率条/趋势/日历/分布)"
```

---

## 任务 10: ModelStatsPanel 容器

**Files:**
- Create: `frontend/src/components/ModelStatsPanel/ModelStatsPanel.vue`

### Step 1: 实现

```vue
<script setup lang="ts">
import { RiCpuLine } from '@remixicon/vue'
import { useModelStats } from '@/composables/useModelStats'
import ModelSummaryCards from './ModelSummaryCards.vue'
import ModelHitRateCard from './ModelHitRateCard.vue'
import ModelSecondaryCards from './ModelSecondaryCards.vue'
import ModelSuccessBar from './ModelSuccessBar.vue'
import ModelTrendChart from './ModelTrendChart.vue'
import ModelCalendar from './ModelCalendar.vue'
import ModelDistChart from './ModelDistChart.vue'

const { stats, loading, refresh } = useModelStats()
</script>

<template>
  <Card class="rounded-md">
    <CardHeader>
      <CardTitle class="flex items-center gap-2 text-base"><RiCpuLine size="18" />模型使用情况</CardTitle>
      <CardDescription>模型网关近 30 天使用统计</CardDescription>
    </CardHeader>
    <CardContent class="space-y-6">
      <ModelSummaryCards :stats="stats" />
      <div class="grid gap-4 lg:grid-cols-2">
        <ModelHitRateCard :stats="stats" />
        <div class="space-y-4">
          <ModelSuccessBar :stats="stats" />
          <ModelSecondaryCards :stats="stats" />
        </div>
      </div>
      <div class="grid gap-4 lg:grid-cols-2">
        <ModelTrendChart :stats="stats" />
        <ModelCalendar :calendar="stats?.calendar ?? []" />
      </div>
      <ModelDistChart :items="stats?.model_dist ?? []" />
    </CardContent>
  </Card>
</template>
```

### Step 2: 类型检查

```bash
cd frontend && pnpm exec vue-tsc --noEmit
```

Expected: 无错误。

### Step 3: commit

```bash
git add frontend/src/components/ModelStatsPanel/ModelStatsPanel.vue
git commit -m "feat(frontend): ModelStatsPanel 容器(全套模型使用情况)"
```

---

## 任务 11: OverviewView 集成两个 Panel

**Files:**
- Modify: `frontend/src/views/OverviewView.vue`

### Step 1: 修改

`script` 加 import，`template` 在 4 卡之后、"开始配置/运行说明"两列之前插入两个 Panel:

```vue
<script setup lang="ts">
// ...原有 imports...
import McpStatsPanel from '@/components/McpStatsPanel/McpStatsPanel.vue'
import ModelStatsPanel from '@/components/ModelStatsPanel/ModelStatsPanel.vue'
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="概览" description="查看 Loadout 当前运行状态,并快速进入常用管理项。" />
    <LoadingBlock v-if="loading" />
    <template v-else>
      <div class="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <StatCard v-for="card in cards" :key="card.label" v-bind="card" />
      </div>
      <McpStatsPanel />
      <ModelStatsPanel />
      <div class="grid gap-4 lg:grid-cols-2">
        <!-- 原"开始配置" + "运行说明" -->
      </div>
    </template>
  </div>
</template>
```

### Step 2: 类型检查

```bash
cd frontend && pnpm exec vue-tsc --noEmit
```

Expected: 无错误。

### Step 3: commit

```bash
git add frontend/src/views/OverviewView.vue
git commit -m "feat(overview): 嵌入 MCP 统计 + 模型使用情况双面板"
```

---

## 任务 12: 端到端验证

### Step 1: 起后端

```bash
cd D:/Code/Git/loadout
go build -o loadout.exe ./apps/server
./loadout.exe &
```

### Step 2: 造数据

- MCP: 按 README 的 `X-Loadout-Key` 用法发几次 `/mcp/invoke`（含不存在的工具产生 not_found）
- 模型: 调一次 `/v1/chat/completions` 产生 route-log 记录（若需 API key 则用 keys 签发）

### Step 3: 验证接口

```bash
curl -b "session=..." "http://127.0.0.1:3000/api/stats/mcp?days=30&top=5"
curl -b "session=..." "http://127.0.0.1:3000/api/stats/models?days=30"
```

Expected: 非空 JSON。

### Step 4: 前端验证

```bash
cd frontend && pnpm dev
```

打开 http://127.0.0.1:5173 概览页：
- 原有 4 卡正常
- McpStatsPanel: 趋势图 + 双 Top5
- ModelStatsPanel: 5 卡 + 命中率 + 次级卡 + 成功率条 + 趋势 + 日历 + 分布

### Step 5: commit

```bash
cd D:/Code/Git/loadout
git log --oneline -12
git commit --allow-empty -m "chore: 端到端冒烟通过 - 双面板渲染 + stats 接口"
```

---

## 任务依赖图

```
1 (schema) ─→ 2 (Record+Stats) ─→ 3 (Invoke 埋点) ─→ 4 (stats/mcp 路由)
                                                       │
5 (stats/models 路由, 依赖 route-log 已有数据) ────────┤
                                                       ▼
                             6 (前端装包 + 类型 + API client)
                                                       │
                             7 (useMcpStats + useModelStats)
                                                       │
                   8 (MCP 面板 4 组件)     9 (模型面板 7 子组件)
                                                       │
                             10 (ModelStatsPanel 容器)
                                                       │
                             11 (OverviewView 集成)
                                                       │
                             12 (端到端验证)
```

任务 1-5 后端可并行(5 不依赖 1-4,只依赖 route-log 已有表);任务 6-11 前端串行。
