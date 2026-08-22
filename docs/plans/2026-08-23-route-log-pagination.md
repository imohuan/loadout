# 转发日志真分页（LIMIT/OFFSET + total）Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 转发日志列表从「前端伪分页（后端最多 100 条）」升级为「后端真分页」：`GET /api/route-logs` 支持 `page`/`pageSize` 参数，返回 `{ items, total }`，前端翻页真正拉取对应区间的数据，总数不再恒等于 100。

**Architecture:** 后端 `route-log.Service.List` 增加 `COUNT(*)`（与查询同 WHERE）与 `OFFSET`，返回新增的 `contracts.RouteLogPage{Items, Total}`；`RouteLogFilter` 增加 `Offset` 字段；admin-api handler 解析 `page`/`pageSize` 并换算 `Limit/Offset`。前端 `useRouteLogs.list` 传分页参数、返回类型改为 `RouteLogPage`；`RouteLogsView` 持有 `page/pageSize` 状态并监听表格分页事件刷新；`RouteLogTable` 增加可选 `total` prop（缺省回退 `logs.length`，ModelTestView 不受影响），有 `total` 时不再做前端切片。

**Tech Stack:** Go（contracts / route-log / admin-api）+ Vue 3（RouteLogsView / RouteLogTable / useRouteLogs）。

**背景（为什么这样做）：** 用户反馈转发日志「最大总数一直是 100」。根因：`route-log/service.go` 的 `List` 只有 `LIMIT`（钳制 100）没有 `OFFSET` 也不返回 total；前端 `RouteLogTable.vue` 拿「后端返回的最多 100 条」做纯前端 `slice` 伪分页，`DataPagination :total="logs.length"`。5 页 × 20/pageSize = 100 只是被 100 cap 截出的假象，第 6 页起直接为空，更早的历史日志永久不可见。修复 = 后端真分页 + total，前端用后端 total 并传 page/pageSize 拉数。

**审核修正记录（2026-08-23，子代理 code review 后吸收）：**
- **P0-1** 接口签名变更波及三处测试 mock（`model_gateway_test.go:350` `mockRouteLog.List`、`vision/proxy_test.go:465` 与 `vision_v2/tool_loop_test.go:554` 的 `mockVisionRouteLog.List`），全部返回 `[]RouteRequestView`，`go build ./...` 不编译 `_test.go` 暴露不了，Task 7 会炸 → Task 4 扩充为「mock 签名适配」，Task 7 验证清单补 `./plugins/vision_v2/`。
- **P1-1** `service_test.go` 现有 import 无 `fmt`，TestListPagination 的 `fmt.Sprintf` 需补 import。
- **P1-2** `RouteLogsView` 与 `RouteLogTable` 各持一份 page/pageSize，仅靠 page-change 单向同步；`apply`/`clear`/`manualRefresh` 不重置 page=1 时，高页码下过滤会先拉到空页（EmptyState 闪现）+ 一次 DataPagination 自愈往返 → apply/clear 时同步 `page.value=1`。
- **P1-3** Task 3 pageSize 超上限静默回退 20 语义别扭 → 改为 `pageSize = min(n, 100)`。
- **P2-1** RouteLogTable 新代码用 `watch`，现有 import 仅 `{ computed, ref }`，需补 import。
- **P2-2** 真分页模式越界页 items 为空但 total>0 时，`v-if="pagedLogs.length"` 会误显「还没有请求记录」→ EmptyState 条件改为同时看 total。
- **P2-3** COUNT 与 SELECT 非同一事务，并发写入下 total/items 可能瞬时不一致 → 代码注释说明可接受。
- **P2-4** Task 5 落地后、Task 6 完成前前端 `vue-tsc` 必 FAIL（RouteLogsView 仍按数组用），Step 3 期望明确写「必 FAIL」。审计结论：SQL（listWhere 等价性/args 顺序）、前端 watch/自愈链路无死循环（useListLoader seq 守卫 + pageCount 纠正单调收敛）、ModelTestView 兼容分支成立；补齐 P0 后可执行。
- **实施后 code review 修正（2026-08-23）**：P0——分页状态双份导致过滤后页码错位。RouteLogTable 内部自持 page/pageSize，RouteLogsView 的 apply/clear 只重置父组件 page，表格内部停留在旧页码：过滤后显示第 1 页数据但分页器高亮旧页（新 total 页数足够时永久错位）。修法：`page`/`pageSize` 提升为受控 prop（v-model 语义，`update:page`/`update:pageSize` 事件），删 page-change 事件与内部 watch；非受控模式（ModelTestView 不传）由 internalPage 回退，行为不变。

---

### Task 1: contracts —— RouteLogFilter.Offset + RouteLogPage + List 签名

**Files:**
- Modify: `plugins/contracts/routing.go`（RouteLogFilter 第 175-182 行、RouteLog interface 第 209-222 行，新增 RouteLogPage）

**Step 1: 直接实现（纯结构体/接口变更，无单测价值，靠编译验证）**

`plugins/contracts/routing.go` RouteLogFilter（第 181 行 `Limit` 之后）加：

```go
	Limit         int
	Offset        int
```

`RouteRequestView` 之后新增分页包装结构：

```go
// RouteLogPage 分页结果：Items 为当前页记录，Total 为满足过滤条件的全量条数
// （COUNT 与 List 查询共用同一 WHERE）。前端 DataPagination 用 Total 计算页数。
type RouteLogPage struct {
	Items []RouteRequestView `json:"items"`
	Total int                `json:"total"`
}
```

`RouteLog` interface（第 213 行）的 List 签名改为：

```go
	List(context.Context, RouteLogFilter) (RouteLogPage, error)
```

**Step 2: 编译验证（预期失败——实现方与三处测试 mock 还没改）**

Run: `go build ./plugins/...`
Expected: FAIL（route-log service 与 admin-api handler 未适配新签名）

> ⚠️ `go build` 不编译 `_test.go`，三处 mock（Task 4 会适配）此时不报错，但 Task 7 的 `go test` 会暴露，属预期。

**Step 3: Commit（先提交接口变更，后续 Task 逐个修编译）**

```bash
git add plugins/contracts/routing.go
git commit -m "feat(contracts): add RouteLogPage and RouteLogFilter.Offset for pagination"
```

---

### Task 2: route-log service —— List 加 COUNT + OFFSET，返回 RouteLogPage

**Files:**
- Modify: `plugins/route-log/service.go`（List 第 266-312 行）
- Modify: `plugins/route-log/service_test.go`（新增分页测试；第 89 行旧断言适配）

**Step 1: 写失败测试**

> 先补 import（审计 P1-1）：`service_test.go` 现有 import 无 `fmt`，测试用到 `fmt.Sprintf`，在文件顶部 import 块加 `"fmt"`。

`plugins/route-log/service_test.go` 追加：

```go
// TestListPagination：List 支持 Limit/Offset 分页，Total 为满足过滤条件的全量条数。
func TestListPagination(t *testing.T) {
	service := NewService(logDB(t), nil)
	ctx := context.Background()
	// 插入 25 条日志（started_at 递增，保证 ORDER BY 顺序确定）
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 25; i++ {
		id := fmt.Sprintf("r-pg-%02d", i)
		started := base.Add(time.Duration(i) * time.Minute)
		if err := service.Start(ctx, contracts.RouteRequest{RequestID: id, RequestedModel: "m", StartedAt: started}); err != nil {
			t.Fatalf("Start(%s): %v", id, err)
		}
		if err := service.Finish(ctx, contracts.RouteFinish{RequestID: id, FinishedAt: started.Add(time.Second), Result: "success"}); err != nil {
			t.Fatalf("Finish(%s): %v", id, err)
		}
	}
	// 第 1 页：pageSize=10 → 10 条、total=25
	page1, err := service.List(ctx, contracts.RouteLogFilter{Limit: 10, Offset: 0})
	if err != nil {
		t.Fatal(err)
	}
	if page1.Total != 25 {
		t.Fatalf("total = %d, want 25", page1.Total)
	}
	if len(page1.Items) != 10 {
		t.Fatalf("page1 len = %d, want 10", len(page1.Items))
	}
	// 第 3 页：offset=20 → 5 条（余量）
	page3, err := service.List(ctx, contracts.RouteLogFilter{Limit: 10, Offset: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(page3.Items) != 5 {
		t.Fatalf("page3 len = %d, want 5", len(page3.Items))
	}
	// 越界页：offset=30 → 0 条，但 total 仍为 25
	pageOver, err := service.List(ctx, contracts.RouteLogFilter{Limit: 10, Offset: 30})
	if err != nil {
		t.Fatal(err)
	}
	if len(pageOver.Items) != 0 {
		t.Fatalf("over page len = %d, want 0", len(pageOver.Items))
	}
	if pageOver.Total != 25 {
		t.Fatalf("over page total = %d, want 25", pageOver.Total)
	}
	// 负 offset 钳 0
	pageNeg, err := service.List(ctx, contracts.RouteLogFilter{Limit: 10, Offset: -5})
	if err != nil {
		t.Fatal(err)
	}
	if len(pageNeg.Items) != 10 {
		t.Fatalf("negative offset len = %d, want 10", len(pageNeg.Items))
	}
	// 过滤条件同样影响 total：造 1 条 failed 做对照（r-pg-00 已是 success，再插一条 failed）
	if err := service.Start(ctx, contracts.RouteRequest{RequestID: "r-fail", RequestedModel: "m", StartedAt: base.Add(-2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := service.Finish(ctx, contracts.RouteFinish{RequestID: "r-fail", FinishedAt: base.Add(-2 * time.Hour).Add(time.Second), Result: "failed"}); err != nil {
		t.Fatal(err)
	}
	failedPage, err := service.List(ctx, contracts.RouteLogFilter{Result: "failed", Limit: 10, Offset: 0})
	if err != nil {
		t.Fatal(err)
	}
	if failedPage.Total != 1 {
		t.Fatalf("failed total = %d, want 1", failedPage.Total)
	}
}
```

**Step 2: 运行确认失败**

Run: `go test ./plugins/route-log/ -run TestListPagination -count=1`
Expected: FAIL（编译失败——List 返回类型还没改；改完签名后断言失败——无 OFFSET/total）

**Step 3: 实现**

`service.go` List（第 266-312 行）重构为三部分：

a) 抽出 WHERE 构建（COUNT 与 SELECT 共用同一条件、同一 args 顺序）：

```go
// listWhere 返回 List/Count 共用的 WHERE 子句与参数（含开头的 " WHERE 1=1"）。
// COUNT 与 SELECT 必须使用完全相同的条件，保证 total 与当前页属于同一数据集。
func listWhere(filter contracts.RouteLogFilter) (string, []any) {
	query := ` WHERE 1=1`
	args := []any{}
	if filter.Model != "" {
		query += ` AND (r.requested_model = ? OR r.final_model = ? OR EXISTS (SELECT 1 FROM route_attempts a WHERE a.request_id = r.request_id AND a.model = ?))`
		args = append(args, filter.Model, filter.Model, filter.Model)
	}
	if filter.ChannelID != "" {
		query += ` AND (r.final_channel_id = ? OR EXISTS (SELECT 1 FROM json_each(r.final_channel_ids_json) WHERE value = ?) OR EXISTS (SELECT 1 FROM route_attempts a WHERE a.request_id = r.request_id AND (a.channel_id = ? OR EXISTS (SELECT 1 FROM json_each(a.channel_ids_json) WHERE value = ?))))`
		args = append(args, filter.ChannelID, filter.ChannelID, filter.ChannelID, filter.ChannelID)
	}
	if filter.Result != "" {
		query += ` AND r.result = ?`
		args = append(args, filter.Result)
	}
	if filter.StartedAfter != nil {
		query += ` AND r.started_at >= ?`
		args = append(args, filter.StartedAfter.UTC().Format(time.RFC3339Nano))
	}
	if filter.StartedBefore != nil {
		query += ` AND r.started_at <= ?`
		args = append(args, filter.StartedBefore.UTC().Format(time.RFC3339Nano))
	}
	return query, args
}
```

b) List 改为先 COUNT 再 SELECT：

```go
func (s *Service) List(ctx context.Context, filter contracts.RouteLogFilter) (contracts.RouteLogPage, error) {
	where, whereArgs := listWhere(filter)
	// COUNT 与 SELECT 非同一事务：并发写入下 total/items 可能瞬时不一致（先 COUNT 后 SELECT，
	// 中间新请求可能漏算或多算一页）。本地单机 SQLite + 3s 轮询场景可接受，仅影响页数展示，
	// 不影响数据正确性；如需强一致再包事务（本计划不做）。
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM route_requests r`+where, whereArgs...).Scan(&total); err != nil {
		return contracts.RouteLogPage{}, err
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `SELECT request_id, requested_model, COALESCE(virtual_model, ''), started_at, finished_at, result, COALESCE(final_model, ''), COALESCE(final_channel_id, ''), COALESCE(final_channel_ids_json, '[]'), COALESCE(final_channel_base_url, ''), COALESCE(final_channel_name, ''), COALESCE(http_status, 0), COALESCE(duration_ms, 0), error_message, COALESCE(error_body, ''), COALESCE(stream, 0), COALESCE(prompt_tokens, 0), COALESCE(completion_tokens, 0), COALESCE(cached_tokens, 0) FROM route_requests r` + where + ` ORDER BY r.started_at DESC LIMIT ? OFFSET ?`
	args := append(whereArgs, limit, offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return contracts.RouteLogPage{}, err
	}
	defer rows.Close()
	result := make([]contracts.RouteRequestView, 0)
	for rows.Next() {
		view, err := scanRequest(rows)
		if err != nil {
			return contracts.RouteLogPage{}, err
		}
		result = append(result, view)
	}
	return contracts.RouteLogPage{Items: result, Total: total}, rows.Err()
}
```

> ⚠️ 现有 List 的 SELECT 列序、scanRequest 保持不变；只是从「直接 return []」改为「包进 RouteLogPage」。

**Step 4: 适配既有测试调用（service_test.go:89）**

第 89 行 `list, err := service.List(ctx, contracts.RouteLogFilter{})` 后遍历改为：

```go
	count := 0
	for _, item := range list.Items {
		if item.RequestID == "r-retry" {
			count++
		}
	}
```

**Step 5: 运行确认通过**

Run: `go test ./plugins/route-log/ -count=1`
Expected: PASS（新分页测试 + 既有测试全绿）

**Step 6: Commit**

```bash
git add plugins/route-log/service.go plugins/route-log/service_test.go
git commit -m "feat(route-log): paginate List with COUNT+OFFSET and return RouteLogPage"
```

---

### Task 3: admin-api handler —— 解析 page/pageSize

**Files:**
- Modify: `plugins/admin-api/routing.go`（handleRouteLogsList 第 829-851 行）

**Step 1: 直接实现（handler 解析逻辑，编译由 Task 1 的签名变更驱动验证）**

`routing.go` handleRouteLogsList 构造 filter 后加分页解析（第 844 行 `}` 之后、第 845 行 List 调用之前）：

```go
	page := 1
	if v := r.URL.Query().Get("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			page = n
		}
	}
	pageSize := 20
	if v := r.URL.Query().Get("pageSize"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n > 100 {
				n = 100 // 审计修正：超上限钳到 100 而非静默回退 20，避免请求 50 却拿到 20 的错觉
			}
			pageSize = n
		}
	}
	filter.Limit = pageSize
	filter.Offset = (page - 1) * pageSize
```

List 调用与返回改为（第 845-850 行）：

```go
	values, err := s.routeLog.List(r.Context(), filter)
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, values)
```

（`values` 已是 `RouteLogPage`，直接序列化为 `{"items": [...], "total": N}`，writeJSON 不需要改。）

> 说明：`strconv` 已在 `plugins/admin-api` 包被使用（stats_models.go 有 `strconv.Atoi`），无需新增 import。

**Step 2: 编译验证**

Run: `go build ./plugins/admin-api/ ./plugins/route-log/ ./plugins/contracts/`
Expected: PASS

**Step 3: Commit**

```bash
git add plugins/admin-api/routing.go
git commit -m "feat(admin-api): parse page/pageSize for route-logs list"
```

---

### Task 4: 接口变更影响面适配（真实调用 + 测试 mock）

**Files:**
- Modify: `plugins/vision/proxy_vision_e2e_test.go`（第 153 行、第 220 行真实调用）
- Modify: `plugins/model-gateway/model_gateway_test.go`（第 350 行 mockRouteLog.List）
- Modify: `plugins/vision/proxy_test.go`（第 465 行 mockVisionRouteLog.List）
- Modify: `plugins/vision_v2/tool_loop_test.go`（第 554 行 mockVisionRouteLog.List）

> ⚠️ 审计 P0-1：List 签名变更除了真实调用（vision e2e），还有三处测试 mock 实现了 `RouteLog` 接口，全部返回 `[]RouteRequestView`，不改会编译失败。`go build` 不覆盖 `_test.go`，必须用 `go vet` / `go test` 验证。

**Step 1: 适配真实调用（vision/proxy_vision_e2e_test.go）**

第 153-160 行：

```go
	logs, err := rl.List(context.Background(), contracts.RouteLogFilter{})
	...
	if len(logs) != 1 {
	...
	detail, err := rl.Detail(context.Background(), logs[0].RequestID)
```

改为（List 返回 RouteLogPage，取 .Items）：

```go
	page, err := rl.List(context.Background(), contracts.RouteLogFilter{})
	if err != nil {
		t.Fatalf("查询 route log 失败: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("应有 1 条请求日志，实际 %d", len(page.Items))
	}
	detail, err := rl.Detail(context.Background(), page.Items[0].RequestID)
```

第 220-227 行同款改法：

```go
	page, err := rl.List(context.Background(), contracts.RouteLogFilter{})
	if err != nil {
		t.Fatalf("查询 route log 失败: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Result != "failed" {
		t.Fatalf("应有 1 条 failed 请求日志，实际 %+v", page.Items)
	}
	detail, err := rl.Detail(context.Background(), page.Items[0].RequestID)
```

**Step 2: 适配三处 mock**

三处 mock 的 `List` 签名统一改为（返回 `RouteLogPage`，空页返回零值即可，mock 的用途是占位不涉及分页断言）：

```go
func (m *mockRouteLog) List(ctx context.Context, f contracts.RouteLogFilter) (contracts.RouteLogPage, error) {
	return contracts.RouteLogPage{}, nil
}
```

- `plugins/model-gateway/model_gateway_test.go:350`（receiver `*mockRouteLog`）
- `plugins/vision/proxy_test.go:465`（receiver `*mockVisionRouteLog`）
- `plugins/vision_v2/tool_loop_test.go:554`（receiver `*mockVisionRouteLog`）

> 若某 mock 的 List 原本有返回体（如填充 fake 数据），按该文件现有断言语义改，不要只图编译过。

**Step 3: 运行确认通过**

Run: `go vet ./plugins/model-gateway/ ./plugins/vision/ ./plugins/vision_v2/ ./plugins/route-log/ ./plugins/admin-api/`
Expected: PASS（vet 会编译测试文件，能抓到 mock 签名漏改）

Run: `go test ./plugins/vision/ -run TestVision -count=1`
Expected: PASS（若既有 vision E2E 因环境失败，以该文件历史行为为准，至少保证编译通过）

Run: `go build ./...`
Expected: PASS（全仓非测试编译）

**Step 4: Commit**

```bash
git add plugins/vision/proxy_vision_e2e_test.go plugins/model-gateway/model_gateway_test.go plugins/vision/proxy_test.go plugins/vision_v2/tool_loop_test.go
git commit -m "test: adapt RouteLog.List callers and mocks to paginated RouteLogPage"
```

---

### Task 5: 前端 types + useRouteLogs —— 分页参数与返回类型

**Files:**
- Modify: `frontend/src/lib/types.ts`（RouteLog 第 98 行之后新增 RouteLogPage）
- Modify: `frontend/src/composables/useRouteLogs.ts`（list 第 19-27 行）

**Step 1: types.ts 新增类型**

`RouteLog` 接口之后加：

```ts
/** 转发日志分页结果：items 为当前页记录，total 为满足过滤条件的全量条数（后端 COUNT）。 */
export interface RouteLogPage {
  items: RouteLog[]
  total: number
}
```

**Step 2: useRouteLogs.ts 改 list 签名**

```ts
export function useRouteLogs() {
  function list(
    filters: RouteLogFilters,
    pagination?: { page?: number; pageSize?: number },
  ) {
    const search = new URLSearchParams()
    if (filters.model) search.set('model', filters.model)
    if (filters.channel_id) search.set('channel_id', filters.channel_id)
    if (filters.result) search.set('result', filters.result)
    if (toISOString(filters.from)) search.set('from', toISOString(filters.from)!)
    if (toISOString(filters.to)) search.set('to', toISOString(filters.to)!)
    if (pagination?.page) search.set('page', String(pagination.page))
    if (pagination?.pageSize) search.set('pageSize', String(pagination.pageSize))
    return api<RouteLogPage>(`/api/route-logs${search.size ? `?${search}` : ''}`)
  }
  ...
}
```

（`detail` / `clear` 不变。`RouteLog` import 保留，新增 `RouteLogPage` 引入。）

**Step 3: 前端类型检查**

Run: `cd frontend && vue-tsc -b --noEmit`
Expected: **必 FAIL**（审计 P2-4：Task 5 已改 `useRouteLogs.list` 返回 `RouteLogPage`，但 RouteLogsView 仍按 `RouteLog[]` 使用 `logs.value`，Task 6 完成前前端处于中间态——此为预期，Task 6 Step 3 再确认 PASS）

**Step 4: Commit**

```bash
git add frontend/src/lib/types.ts frontend/src/composables/useRouteLogs.ts
git commit -m "feat(frontend): support pagination params in route-logs api"
```

---

### Task 6: 前端 RouteLogsView + RouteLogTable —— 真分页联动

**Files:**
- Modify: `frontend/src/views/RouteLogsView.vue`（useListLoader 第 19-24 行、模板第 238-244 行）
- Modify: `frontend/src/components/route-logs/RouteLogTable.vue`（props 第 12-23 行、内部分页第 92-98 行、DataPagination 第 394-396 行）

**设计取舍（为何分页状态放 RouteLogsView）：**
- 3s 自动刷新（RouteLogsView 第 147-167 行）必须知道当前 page/pageSize 才能刷新「当前页」；状态放表格内部则刷新逻辑无法感知页码。
- RouteLogTable 保持「纯展示 + 通知」，数据永远来自后端当前页，不再本地切片（避免翻页瞬间 slice 到空）。
- ModelTestView 不传 `total` → 走「本地切片 + total=logs.length」的兼容分支，行为与现状完全一致（模型测试页日志来自本地缓存，不走后端分页）。

**Step 1: RouteLogTable 增加可选 props + page-change 事件**

> 审计 P2-1：RouteLogTable 第 2 行现有 import 为 `import { computed, ref } from 'vue'`，新代码用到 `watch`，需改为 `import { computed, ref, watch } from 'vue'`。

props（第 12-23 行）追加：

```ts
  /** 后端返回的全量条数（真分页）。缺省时回退 logs.length，保持本地伪分页兼容（模型测试页） */
  total?: number
```

emit 增加：

```ts
const emit = defineEmits<{
  expand: [log: RouteLog]
  /** 页码/每页条数变化（真分页模式下父组件据此重新拉取当前页） */
  'page-change': [page: number, pageSize: number]
}>()
```

内部分页逻辑（第 92-98 行）改为：

```ts
// ---------- 分页 ----------
// 有 total（真分页：数据已是后端返回的当前页）→ 不做本地切片，直接渲染 logs；
// 无 total（模型测试页兼容）→ 保持本地切片伪分页。
const page = ref(1)
const pageSize = ref(20)
const pagedLogs = computed(() => {
  if (props.total !== undefined) return props.logs || []
  const start = (page.value - 1) * pageSize.value
  return (props.logs || []).slice(start, start + pageSize.value)
})
// 真分页模式下，页码/每页条数变化通知父组件刷新（watch 覆盖 DataPagination 内部纠正 page 的路径）
watch([page, pageSize], ([p, s]) => {
  if (props.total !== undefined) emit('page-change', p, s)
})
```

DataPagination（第 394-396 行）total 改为：

```html
<DataPagination v-model:page="page" v-model:page-size="pageSize" :total="props.total ?? logs.length" />
```

> ⚠️ 组件复用隔离：ModelTestView（950-970 行）不传 total → 走本地切片分支、不 emit page-change，内部行为不变。RouteLogsView 传 total → 真分页。
> ⚠️ 翻页越界自愈：DataPagination 内部 watch pageCount（total 变化导致页数变少）会自动 emit update:page 纠正，page 变化触发 page-change → 父组件刷新，一次到位，无死循环（刷新后 total 稳定）。

**Step 2: RouteLogsView 持有分页状态 + 监听事件**

第 18-24 行区域改为：

```ts
const service = useRouteLogs()
const channelService = useChannels()
const filters = ref<RouteLogFilters>({})
const page = ref(1)
const pageSize = ref(20)
const {
  data: logsData,
  loading,
  refreshing,
  refresh,
} = useListLoader(() => service.list(filters.value, { page: page.value, pageSize: pageSize.value }))
const logs = computed(() => logsData.value?.items ?? [])
const total = computed(() => logsData.value?.total ?? 0)
```

> ⚠️ `useListLoader` 泛型从 `RouteLog[]` 变为 `RouteLogPage`；原 `logs.value` 用法（displayLogs 第 98-109 行、selfHealStuckLogs 第 76 行、refreshActiveDetails 第 114 行）需同步改为 `logs.value`（新 computed）。其中 selfHeal / refreshActiveDetails 遍历的是「当前页数据」，行为正确——只对可见行做自愈/详情刷新。

新增翻页处理：

```ts
function onPageChange(nextPage: number, nextSize: number) {
  page.value = nextPage
  pageSize.value = nextSize
  void refresh()
}
```

> 审计 P1-2：`apply`（第 169-175 行）与 `clear`（第 193-205 行）在改 `filters.value` / 清空后，需先重置 `page.value = 1` 再 `refresh()`，否则高页码下过滤会先拉到空页（EmptyState 闪现）+ 一次 DataPagination 自愈往返：

```ts
async function apply(next: RouteLogFilters) {
  await run('apply', async () => {
    filters.value = next
    page.value = 1 // 过滤条件变化回到第一页
    await refresh()
    await refreshActiveDetails()
  })
}
// clear 内 refresh() 前同样加 page.value = 1
```

模板 RouteLogTable（第 238-244 行）追加：

```html
<RouteLogTable
  :logs="displayLogs"
  :channels="channels || []"
  :loading-detail="loadingDetail"
  :total="total"
  @expand="expand"
  @page-change="onPageChange"
/>
```

> 审计 P2-2：真分页模式越界页 items 为空但 total>0 时，`v-if="pagedLogs.length"` 的 EmptyState 分支会误显「还没有请求记录」。RouteLogTable 模板 EmptyState 条件（第 393 行）改为 `v-else-if="!total"`，真分页空页时也不显示误导文案（越界页会在 DataPagination 自愈后自动回到合法页）。

**Step 3: 前端类型检查**

Run: `cd frontend && vue-tsc -b --noEmit`
Expected: PASS

Run: `cd frontend && npx vite build`（或项目既有 build 命令，确认产物可出）
Expected: PASS

**Step 4: Commit**

```bash
git add frontend/src/views/RouteLogsView.vue frontend/src/components/route-logs/RouteLogTable.vue
git commit -m "feat(frontend): wire real pagination in route log list"
```

---

### Task 7: 文档更新 + 全量验证

**Files:**
- Modify: `docs/ROUTE-LOG-ARCHITECTURE.md`（§6/§7 数据流与 API 响应结构，第 311-318 行附近）
- Modify: `docs/API.md`（可选：route-logs 响应结构补一句）

**Step 1: 文档补充**

- ROUTE-LOG-ARCHITECTURE.md：`GET /api/route-logs` 响应从数组改为 `{ "items": [...], "total": N }`；支持 `page`（默认 1）/`pageSize`（默认 20，上限 100）参数；前端数据流描述补「3s 定时刷新 + 翻页均带当前 page/pageSize」。
- API.md：同口径补一句响应结构说明（如有路由日志章节）。

**Step 2: 全量验证**

Run: `go vet ./plugins/contracts/ ./plugins/route-log/ ./plugins/admin-api/ ./plugins/vision/ ./plugins/vision_v2/ ./plugins/model-gateway/`
Expected: PASS（vet 编译测试文件，确保 mock 与真实调用全部适配）

Run: `go build -buildvcs=false ./... && go test ./core/db/ ./plugins/route-log/ ./plugins/admin-api/ ./plugins/vision/ ./plugins/vision_v2/ ./plugins/model-gateway/ -count=1`
Expected: 全部 PASS（vision 既有环境类失败以历史行为为准）

Run: `cd frontend && vue-tsc -b --noEmit`
Expected: PASS

**Step 3: Commit**

```bash
git add docs/ROUTE-LOG-ARCHITECTURE.md docs/API.md
git commit -m "docs(route-log): document paginated list response"
```

---

## 已知边界 / 不做的

1. **API 破坏性变更**：`GET /api/route-logs` 响应从裸数组改为 `{items, total}`。全仓仅 admin-api handler 一个消费者（stats_models 直接 SQL 不依赖 List；desktop 前端 dist 是主 frontend 构建副本，重新 build 后自然一致）。
2. **pageSize 上限 100**：沿用现有 List 钳制语义（limit>500 → 100 的原逻辑保留，handler 侧 pageSize 已钳 ≤100），与 DataPagination 的 pageSizes=[10,20,50,100] 对齐。
3. **大 offset 性能**：SQLite 本地库，数据量级小，OFFSET 直接扫描可接受；不在本计划做游标分页。
4. **3s 自动刷新与分页**：刷新保持当前页；翻页越界由 DataPagination 自愈（watch pageCount 纠正 page），不额外做前端 clamp。
5. **模型测试页不变**：ModelTestView 不传 total → RouteLogTable 走本地切片兼容分支，行为与现状一致；只改 RouteLogTable 内部条件分支，不动 ModelTestView 逻辑。
6. **detail 接口不变**：`GET /api/route-logs/{id}` 仍返回单条 RouteRequestView，不做分页包装。
7. **COUNT/SELECT 非同一事务**：并发写入下 total/items 可能瞬时不一致（先 COUNT 后 SELECT 之间新增的请求会漏在当前页外），本地单机 SQLite + 3s 轮询场景可接受，仅影响页数展示，代码注释已说明。
