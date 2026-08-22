# 转发日志实时 Token 估算 + 首字节时间戳 Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 转发日志的流式 attempt 在运行中实时展示「等待响应 → 输出中」两阶段耗时与已输出 token 估算值，流结束后被真实 usage 覆盖。

**Architecture:** 状态机不动（保持 running/success/failed 三态）。方案 = 后端给 `route_attempts` 加正式列 `first_byte_at`（收到上游响应头的时刻），流式转发中每 ~500ms 节流 UPSERT attempt 的估算 completion_tokens（字符数 → token 估算，零依赖），前端 running 行渲染「等待响应 Xs → 输出中 Ys · 已输出 N tok（估算）」。

**Tech Stack:** Go（SQLite 迁移 / model-gateway 流式转发 / route-log 存储）+ Vue 3（RouteLogTable 展示）。

**背景（为什么这样做）：** 用户反馈转发日志"上游 13.69s 已成功但请求卡 90s 进行中"，根因已修（proxyStream 缺 `[DONE]` 主动退出）。但用户进一步想要：运行中能看到"正在吐字"的实时反馈 + 等待/输出两阶段耗时。由于上游只在流结束才返回真实 usage，实时阶段只能本地估算（CJK ≈ 1 字/token，英文 ≈ 4 字符/token），精度 ±20% 仅用于展示。已评估 tiktoken-go 等第三方库：只对 OpenAI 系准、词表大、内存贵，结论是不引入。

**审核修正记录（2026-08-22，子代理 code review 后吸收）：**
- **P0-1** flushEst 无条件覆盖 `usage.CompletionTokens` 会覆盖流末 parseUsageLine 解析出的真实 usage → 加 `sawRealUsage` 标志，真实值命中后估算不再覆盖。
- **P1-1** 节流写库用 `pipe.HTTPRequest.Context()` 无 WithoutCancel 保护，客户端断开时 ExecContext 静默失败 → 仿 proxyFinishLog 用 `context.WithoutCancel`；顺带修 proxyStreamAttempt done=true 分支的同款隐患。
- **P1-2** Task 3 测试引用的 `newTestService`/`ptr` 不存在，route-log 测试实际用 `NewService(logDB(t), nil)` + `pointer` → 已改。
- **P1-3** Task 5 测试 `newTestService(t)` 返回 `(*Service, *store.Store)` 且 `svc.routeLog` 为 nil，断言无从写 → 需先 `db.OpenMemory` + `routelog.NewService` + `svc.SetRoutingServices(...)` 接线。
- **P2** 行号校准：proxyStreamAttempt 实为 696、流式分支实为 345、proxyStream 实为 367；Task 5 b) 标题改"签名不变"（已有 pipe 参数）；estimateTokens 计入行尾 `\n` 属可接受误差。

---

### Task 1: 迁移 v19 —— route_attempts 加 first_byte_at 列

**Files:**
- Modify: `core/db/migrate.go`（migrations 数组末尾，version 18 之后追加 version 19）
- Modify: `core/db/db_test.go`（迁移计数 18→19，incompatible 测试插 19→20）

**Step 1: 先改 db_test.go 让迁移数断言失败（TDD）**

```go
// db_test.go:52 附近
if count != 19 {
    t.Fatalf("schema_migrations count = %d, want 19", count)
}
// db_test.go:64 附近
// 程序当前有 19 条迁移，插入 version 20 才能模拟"比程序更新"的库。
```

**Step 2: 运行测试确认失败**

Run: `go test ./core/db/ -run TestMigrateIsIdempotent -count=1`
Expected: FAIL（count = 18, want 19）

**Step 3: 写迁移**

`core/db/migrate.go` 数组 `}, {` 追加：

```go
	}, {
		version: 19,
		name:    "route-attempts-first-byte-at",
		sql: `
-- 流式 attempt 收到上游响应头的时刻（TTFB），配合 started_at 前端可算
-- "等待响应 Xs"，配合当前时间/ finished_at 可算 "输出中 Ys"。
-- 运行中由 model-gateway 写入；流结束的 success UPSERT 用 COALESCE 保留旧值。
ALTER TABLE route_attempts ADD COLUMN first_byte_at TEXT;
`,
	}}
```

**Step 4: 运行测试确认通过**

Run: `go test ./core/db/ -count=1`
Expected: PASS（db_test 里表清单断言不含新列，无需改表数；迁移计数 19、incompatible 插 20 全部对齐）

**Step 5: Commit**

```bash
git add core/db/migrate.go core/db/db_test.go
git commit -m "feat(route-log): migration v19 add route_attempts.first_byte_at"
```

---

### Task 2: contracts.RouteAttempt 加 FirstByteAt 字段

**Files:**
- Modify: `plugins/contracts/routing.go`（RouteAttempt 结构体，FinishedAt 之后）

**Step 1: 先写契约单测**（`plugins/contracts/` 如有测试文件则追加，无则本任务仅编译验证——契约是纯结构体，测试价值低，跳过写失败测试，直接实现）

**Step 2: 实现**

`plugins/contracts/routing.go` RouteAttempt（第 99-124 行）的 `FinishedAt` 之后加：

```go
	// FirstByteAt 流式尝试收到上游响应头的时刻（TTFB）。仅流式 attempt 有值，
	// 运行中由 model-gateway 写入，前端据此展示"等待响应 Xs → 输出中 Ys"。
	FirstByteAt       *time.Time     `json:"first_byte_at,omitempty"`
```

**Step 3: 编译验证**

Run: `go build ./plugins/contracts/`
Expected: PASS

**Step 4: Commit**

```bash
git add plugins/contracts/routing.go
git commit -m "feat(route-log): add RouteAttempt.FirstByteAt field"
```

---

### Task 3: route-log service 读写 first_byte_at

**Files:**
- Modify: `plugins/route-log/service.go`（Attempt 的 INSERT/UPSERT 第 69 行、Detail 的 SELECT 第 306 行 + 扫描 322-339 行）

**Step 1: 写失败测试**

`plugins/route-log/service_test.go` 追加：

```go
// TestAttemptFirstByteAt：running 占位写 first_byte_at，success UPSERT 不传时保留旧值。
func TestAttemptFirstByteAt(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t) // 假设 helper 存在；若叫 newService 则以现有为准
	now := time.Now()
	fb := now.Add(-3 * time.Second)
	if err := svc.Start(ctx, contracts.RouteRequest{RequestID: "r-fb", RequestedModel: "m", StartedAt: now}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// 1) running 占位带 first_byte_at
	if _, err := svc.Attempt(ctx, contracts.RouteAttempt{
		RequestID: "r-fb", StepNo: 1, Action: "首次尝试", Model: "m", ChannelID: "c",
		StartedAt: now, Result: "running", Stream: true, FirstByteAt: &fb,
	}); err != nil {
		t.Fatalf("Attempt(running): %v", err)
	}
	// 2) success UPSERT 不传 FirstByteAt → 旧值保留
	if _, err := svc.Attempt(ctx, contracts.RouteAttempt{
		RequestID: "r-fb", StepNo: 1, Action: "首次尝试", Model: "m", ChannelID: "c",
		StartedAt: now, FinishedAt: ptr(now.Add(10 * time.Second)), Result: "success", Stream: true,
	}); err != nil {
		t.Fatalf("Attempt(success): %v", err)
	}
	detail, err := svc.Detail(ctx, "r-fb")
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if len(detail.Attempts) != 1 || detail.Attempts[0].FirstByteAt == nil {
		t.Fatalf("first_byte_at 应保留: %+v", detail.Attempts)
	}
	if got := detail.Attempts[0].FirstByteAt.Unix(); got != fb.Unix() {
		t.Fatalf("first_byte_at = %d, want %d", got, fb.Unix())
	}
}
```

**Step 2: 运行确认失败**

Run: `go test ./plugins/route-log/ -run TestAttemptFirstByteAt -count=1`
Expected: FAIL（first_byte_at 无列 / 字段为 nil）

**Step 3: 实现**

`service.go:69` Attempt 的 SQL 改为（关键：UPSERT 时 `first_byte_at=COALESCE(excluded.first_byte_at, route_attempts.first_byte_at)`，success 不传时保留 running 阶段写的值）：

```go
	var firstByte any
	if attempt.FirstByteAt != nil {
		firstByte = attempt.FirstByteAt.UTC().Format(time.RFC3339Nano)
	}
	// INSERT 列表加 first_byte_at，VALUES 加 firstByte；
	// ON CONFLICT DO UPDATE SET 加 first_byte_at=COALESCE(excluded.first_byte_at, route_attempts.first_byte_at)
```

`service.go:306` Detail 的 attempt SELECT 加 `first_byte_at`（在 finished_at 后）——**注意列序**：INSERT 的 Scan 参数顺序必须与 SELECT 列序一一对应。当前 SELECT 是 `... started_at, finished_at, result, failure_class, ...`，改为 `... started_at, finished_at, first_byte_at, result, failure_class, ...`，则 Scan 参数同样把 first_byte 插在 finished 之后、result 之前：

```go
	var firstByte sql.NullString
	// Scan 参数按新列序插入（finished 之后、result 之前）
	if firstByte.Valid {
		if parsed, err := time.Parse(time.RFC3339Nano, firstByte.String); err == nil {
			attempt.FirstByteAt = &parsed
		}
	}
```

> ⚠️ 测试 helper 名（审核修正）：route-log 的 `service_test.go` **没有** `newTestService`/`ptr`，实际 helper 是 `NewService(logDB(t), nil)`（L14/L25）和 `pointer`（L118）。Task 3 测试代码必须改成：

```go
	svc := NewService(logDB(t), nil) // 若已有别的 helper 名以现有测试文件为准
	...
	FinishedAt: pointer(now.Add(10 * time.Second)),
```


**Step 4: 运行测试确认通过**

Run: `go test ./plugins/route-log/ -count=1`
Expected: PASS

**Step 5: Commit**

```bash
git add plugins/route-log/service.go plugins/route-log/service_test.go
git commit -m "feat(route-log): persist and read route_attempts.first_byte_at"
```

---

### Task 4: model-gateway token 估算函数 + 单测

**Files:**
- Modify: `plugins/model-gateway/proxy.go`（新增 estimateTokens + isCJK）
- Modify: `plugins/model-gateway/proxy_test.go`（新增测试）

**Step 1: 写失败测试**

`proxy_test.go` 追加：

```go
// TestEstimateTokens 估算口径：CJK ≈ 1 token/字，其他 ≈ 4 字符/token（向上取整）。
func TestEstimateTokens(t *testing.T) {
	cases := []struct{ in string; want int }{
		{"", 0},
		{"你好世界", 4},        // 4 个 CJK = 4
		{"hello", 2},         // 5 字符 / 4 = 1.25 → 2
		{"hello world!", 4},  // 12 字符 / 4 = 3 → 3？见实现口径
		{"你好 hello", 5},     // 2 CJK + 6 字符 → 2 + 2 = 4？见实现口径
	}
	for _, c := range cases {
		if got := estimateTokens(c.in); got != c.want {
			t.Fatalf("estimateTokens(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
```

> 注：实现确定后按实际算法填 want（英文按 `(len+3)/4` 向上取整，即 "hello world!" 12 字符 = 3；"你好 hello" = 2 + ceil(6/4)=2 → 4）。

**Step 2: 运行确认失败**

Run: `go test ./plugins/model-gateway/ -run TestEstimateTokens -count=1`
Expected: FAIL（estimateTokens 未定义）

**Step 3: 实现**

`proxy.go` 末尾（isSSEDone 附近）加：

```go
// estimateTokens 极简 token 估算：CJK 字符 ≈ 1 token/字，其余字符 ≈ 4 字符/token
// （向上取整）。零依赖、±20% 精度，仅用于实时展示"正在吐字"，不用于计费/上下文窗口。
// 流结束后由上游真实 usage 覆盖。
func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	var cjk, other int
	for _, r := range s {
		if isCJK(r) {
			cjk++
		} else {
			other++
		}
	}
	return cjk + (other+3)/4
}

func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r)
}
```

import 加 `"unicode"`。

**Step 4: 运行确认通过**

Run: `go test ./plugins/model-gateway/ -run TestEstimateTokens -count=1`
Expected: PASS

**Step 5: Commit**

```bash
git add plugins/model-gateway/proxy.go plugins/model-gateway/proxy_test.go
git commit -m "feat(model-gateway): add zero-dep token estimation helper"
```

---

### Task 5: proxyStreamAttempt 记 first_byte_at + proxyStream 节流写估算 token

**Files:**
- Modify: `plugins/model-gateway/proxy.go`（proxyStreamAttempt 693-746、proxyForward 流式分支 344-348、proxyStream 365-429）

**Step 1: 写失败测试**（E2E：流式响应断言 attempt 有 first_byte_at + 估算 tokens）

`proxy_test.go` 追加（复用 `TestProxyStreamDoneHangsUpstream` 的模式，断言路由日志）：

```go
// TestProxyStreamAttemptFirstByteAndEstTokens：流式 attempt 在运行中有 first_byte_at，
// completion_tokens 为估算值（非 0）；流结束后 route log 里能看到。
func TestProxyStreamAttemptFirstByteAndEstTokens(t *testing.T) {
	// 审核修正：newTestService(t) 只返回 (*Service, *store.Store)，svc.routeLog 为 nil，
	// flushEst/proxyStreamAttempt 会提前 return，断言无从写。必须先接 route-log：
	//   database, _ := db.OpenMemory(); t.Cleanup(...)
	//   rl := routelog.NewService(database, slog.Default())
	//   svc.SetRoutingServices(database, mockHealth{}, rl)  // mockHealth 可复用 e2eHealth 样式或本包现有 mock
	// 然后经 svc.routeLog.Detail(requestID) 断言（同包可访问未导出字段）。
	svc, _ := newTestService(t)
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("db.OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	rl := routelog.NewService(database, slog.Default())
	svc.SetRoutingServices(database, mockRouteLogHealth{}, rl) // 具体 mock 名以本包现有为准
	// 用 SSE 回显：发送两段中文内容 + [DONE]
	sse := []string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"你好世界\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"再次输出\"}}]}\n\n",
		"data: [DONE]\n\n",
	}
	echo, _ := newEchoServer(t, "", 0, sse)
	defer echo.Close()
	writeEchoChannel(t, svc, echo.URL)
	body := `{"model":"gpt-4o","messages":[],"stream":true}`
	rr := doProxy(t, svc, "POST", "chat/completions", "", body)
	_ = rr
	// 断言 route log：attempt 有 first_byte_at 且 completion_tokens == estimateTokens("你好世界再次输出")
	// （经由 svc.routeLog.List/Detail 查询，实施时按实际 mock 与字段确认）
}
```

**Step 2: 运行确认失败**

Run: `go test ./plugins/model-gateway/ -run TestProxyStreamAttemptFirstByteAndEstTokens -count=1`
Expected: FAIL（first_byte_at 为 nil / tokens 为 0）

**Step 3: 实现**

a) `proxyStreamAttempt`（696 行，非 693）done=false 分支加 FirstByteAt：

```go
	if !done {
		step++
		pipe.Metadata["__route_step"] = step
		mainStep, _ := pipe.Metadata["__main_route_step"].(int)
		pipe.Metadata["__main_route_step"] = mainStep + 1
		...action 判定...
		// 此刻已收到上游响应头（proxyForward 拿到 resp 后才调本函数），记首字节时间
		now := time.Now()
		firstByte = &now
		// 存 metadata 供 proxyStream 节流写库复用
		pipe.Metadata["__stream_action"] = action
		pipe.Metadata["__stream_started"] = started
	}
```

即：done=false 时 `firstByteAt := time.Now()` 传给 Attempt；done=true 时传 nil（SQL COALESCE 保留旧值）。

b) `proxyStream`（367 行，**签名不变**——已有 pipe 参数）+ 节流写库：

```go
func (s *Service) proxyStream(w http.ResponseWriter, resp *http.Response, pipe *ProxyPipeline) contracts.TokenUsage {
	defer resp.Body.Close()
	...
	var usage contracts.TokenUsage
	// 审核修正：真实 usage 已从流末行解析过（parseUsageLine 命中）就不再被估算覆盖。
	var sawRealUsage bool
	// 节流状态：估算 token 每 500ms 或累计 64 个 chunk UPSERT 一次
	var estTokens, chunksSinceWrite int
	var lastWrite time.Time
	flushEst := func() {
		if estTokens == 0 || sawRealUsage {
			return
		}
		if s.routeLog == nil {
			return
		}
		step, _ := pipe.Metadata["__route_step"].(int)
		if step == 0 {
			return
		}
		action, _ := pipe.Metadata["__stream_action"].(string) // 需要存，见下
		model := pipe.Request.Model
		channelID, _ := pipe.Metadata["__last_tried_channel"].(string)
		startedAt, _ := pipe.Metadata["__stream_started"].(time.Time)
		usage.CompletionTokens = estTokens
		// 审核修正：客户端断开时 r.Context() 已 cancel，ExecContext 会静默失败；
		// 仿 proxyFinishLog 用 WithoutCancel 解耦，保证节流写库总能落盘。
		ctx := context.WithoutCancel(pipe.HTTPRequest.Context())
		// 复用 Attempt UPSERT：同 step 更新 completion_tokens（running 态）
		_, _ = s.routeLog.Attempt(ctx, contracts.RouteAttempt{
			RequestID: pipe.RequestID, StepNo: step, Action: action,
			Model: model, ChannelID: channelID, StartedAt: startedAt,
			Result: "running", Stream: true, CompletionTokens: estTokens,
		})
		chunksSinceWrite = 0
		lastWrite = time.Now()
	}
```

> 说明：`__stream_action` / `__stream_started` 由 proxyStreamAttempt(done=false) 写入 pipe.Metadata；`__last_tried_channel` 由 proxyForward 在循环顶部写入。非流式无此路径。

在循环体每 chunk（`if len(data) > 0` 内，flush 之后）：

```go
		estTokens += estimateTokens(string(data))
		chunksSinceWrite++
		if time.Since(lastWrite) >= 500*time.Millisecond || chunksSinceWrite >= 64 {
			flushEst()
		}
```

`parseUsageLine` 命中真实 usage 时置 `sawRealUsage = true`（防止流末 flush 覆盖真实值）：

```go
		if u := parseUsageLine(line); u.PromptTokens > 0 || u.CompletionTokens > 0 || u.CachedTokens > 0 {
			usage = u
			sawRealUsage = true
		}
```

循环结束（return usage 前）补最后一次 flush（保证 [DONE] 退出时也写了）：

```go
	flushEst()
	return usage
```

c) `proxyForward` 流式分支（345 行，非 344）调 proxyStreamAttempt(done=false) 时同步存 metadata——已由 a) 在 proxyStreamAttempt 内部完成，**proxyForward 无需额外改动**（现有调用行保持）：

```go
		s.proxyStreamAttempt(r, pipe, model, ch.ID, nil, "", attemptStarted, resp.StatusCode, false, contracts.TokenUsage{})
```

d) 最终 success 更新（done=true 调 proxyStreamAttempt）时把真实 usage 写进去——现有代码已传 usage 参数，保持。若上游未发 usage（sawRealUsage=false），usage.CompletionTokens 是估算值，行为符合预期。

e) **既有隐患一并处理（审核 P1）**：proxyStreamAttempt 内部 done=true 分支调用 `s.routeLog.Attempt(r.Context(), ...)` 同样有 client-disconnect 静默失败风险，改为 `context.WithoutCancel(r.Context())`（与 proxyFinishLog 一致）。

**Step 4: 运行测试确认通过**

Run: `go test ./plugins/model-gateway/ -count=1`
Expected: PASS（新测试 + 既有流式测试全绿）

**Step 5: Commit**

```bash
git add plugins/model-gateway/proxy.go plugins/model-gateway/proxy_test.go
git commit -m "feat(model-gateway): write first_byte_at and throttled live token estimate during stream"
```

---

### Task 6: 前端展示 —— 等待响应/输出中耗时 + 实时 token

**Files:**
- Modify: `frontend/src/lib/types.ts`（RouteAttempt 加 first_byte_at / finished_at）
- Modify: `frontend/src/components/route-logs/RouteLogTable.vue`（新增 `liveProgress` prop + attempt 行 running 态展示）
- Modify: `frontend/src/views/ModelTestView.vue`（传 `:live-progress="false"`，模型测试页**不显示**实时进度）

> ⚠️ 组件复用隔离：`ModelTestView.vue`（907-926 行）与 `RouteLogsView.vue`（224 行）共用 `RouteLogTable` 组件。模型测试页不要实时进度效果（用户明确"不需要管"）。因此新增独立 prop `liveProgress`（默认 `true`，路由日志页用默认值），模型测试页显式传 `false` 关闭——**不动 ModelTestView 内部逻辑，只加一个 prop 传参**。

**Step 0: RouteLogTable 加 liveProgress prop**

```ts
const props = withDefaults(
  defineProps<{
    logs: RouteLog[]
    channels: Channel[]
    loadingDetail?: string
    /** false = 不做折叠：详情行始终展开、无箭头（如模型测试请求记录） */
    collapsible?: boolean
    /** false = 不显示实时进度（等待响应/输出中耗时 + 估算 token）。模型测试页关闭，路由日志页默认开启 */
    liveProgress?: boolean
  }>(),
  { collapsible: true, liveProgress: true },
)
```

**Step 1: types.ts 加字段**

```ts
export interface RouteAttempt {
  ...
  started_at: string
  /** 收到上游响应头的时刻（流式 TTFB），前端据此算"等待响应 Xs" */
  first_byte_at?: string
  duration_ms?: number
  ...
}
```

**Step 2: RouteLogTable.vue 加阶段耗时 helper**

script 里加：

```ts
// 流式 attempt 的阶段耗时：等待响应（started_at→first_byte_at）→ 输出中（first_byte_at→now/finished_at）。
// 返回 { waiting: string; streaming: string }，无 first_byte_at 时 streaming 为空。
function phaseTimes(a: RouteAttempt): { waiting: string; streaming: string } {
  const now = new Date()
  const started = new Date(a.started_at).getTime()
  const fb = a.first_byte_at ? new Date(a.first_byte_at).getTime() : 0
  const finished = a.finished_at ? new Date(a.finished_at).getTime() : 0
  if (!Number.isFinite(started)) return { waiting: '', streaming: '' }
  const end = finished || (a.result === 'running' ? now.getTime() : started)
  const waitMs = fb ? Math.max(0, fb - started) : Math.max(0, end - started)
  const streamMs = fb ? Math.max(0, (finished || now.getTime()) - fb) : 0
  return {
    waiting: waitMs > 0 ? formatDuration(waitMs) : '',
    streaming: streamMs > 0 ? formatDuration(streamMs) : '',
  }
}
```

> 注：`RouteAttempt` 前端类型需补 `finished_at?: string`（后端 RouteAttempt JSON 有 finished_at）。

**Step 3: 模板 attempt 行渲染**（duration 徽章之后，`v-if="attempt.stream"` 的流徽章旁，条件加 `props.liveProgress`）：

```html
<span v-if="props.liveProgress && attempt.stream && attempt.result === 'running' && phaseTimes(attempt).waiting"
      class="text-xs text-muted-foreground tabular-nums">
  ⏳ 等待响应 {{ phaseTimes(attempt).waiting }}<template v-if="phaseTimes(attempt).streaming"> → 输出中 {{ phaseTimes(attempt).streaming }}</template>
  <template v-if="attempt.completion_tokens"><span class="opacity-60">·</span>已输出 {{ formatTokens(attempt.completion_tokens) }} tok（估算）</template>
</span>
```

**Step 4: ModelTestView 传 false**

`frontend/src/views/ModelTestView.vue:911` `:collapsible="false"` 之后加：

```html
:collapsible="false"
:live-progress="false"
```

**Step 5: 前端类型检查**

Run: `cd frontend && vue-tsc -b --noEmit`
Expected: PASS

**Step 5: Commit**

```bash
git add frontend/src/lib/types.ts frontend/src/components/route-logs/RouteLogTable.vue frontend/src/views/ModelTestView.vue
git commit -m "feat(frontend): show waiting/streaming phase times and live token estimate in route log"
```

> 注：`ModelTestView.vue` 只加 `:live-progress="false"` 一个 prop 传参，其余逻辑零改动。

---

### Task 7: 文档更新 + 全量验证

**Files:**
- Modify: `docs/ROUTE-LOG-ARCHITECTURE.md`（§2 数据模型 route_attempts 表加 first_byte_at；§7 前端展示补充运行中阶段耗时说明）
- Modify: `docs/plans/`（本计划归档，无需改）

**Step 1: 文档补充**

- §2.2 route_attempts 表新增行：`| first_byte_at | TEXT NULL | 流式尝试收到响应头的时刻（TTFB），前端算"等待响应 Xs → 输出中 Ys" |`
- §7.2/7.3 前端数据流补一句：running 的流式 attempt 展示阶段耗时与估算 token（3s 刷新实时跳动）。

**Step 2: 全量验证**

Run: `go build -buildvcs=false ./... && go test ./core/db/ ./plugins/route-log/ ./plugins/model-gateway/ ./plugins/aggregate/ -count=1`
Expected: 全部 PASS

Run: `cd frontend && vue-tsc -b --noEmit`
Expected: PASS

**Step 3: Commit**

```bash
git add docs/ROUTE-LOG-ARCHITECTURE.md
git commit -m "docs(route-log): document first_byte_at and live token estimate"
```

---

## 已知边界 / 不做的

1. **不拆状态机**：不加 waiting 状态枚举，SelfHeal 判定逻辑（只认 running）不动。
2. **估算精度**：±20%，仅展示；流结束被真实 usage 覆盖（既有 parseUsageLine 逻辑）。
3. **不引入 tiktoken**：第三方 tokenizer 只适配单模型家族，收益低、成本高。
4. **节流写库**：500ms / 64 chunk 阈值，避免流式高频打爆 SQLite；running 态 UPSERT 与既有 success UPSERT 共用 ON CONFLICT 路径，无新增并发风险。
5. **非流式**：无 first_byte_at（无流阶段），前端仅展示总耗时，行为不变。
6. **模型测试页不变**：`ModelTestView.vue` 通过 `:live-progress="false"` 关闭实时进度（该页不需要"当前进度"效果，用户明确），仅加一个 prop 传参，内部逻辑零改动。
7. **vision 测试既有失败**：`TestVisionE2EFlushOnSuccess` 因 OpenMemory 唯一库名导致渠道隔离（既有 bug），不在本计划范围。
