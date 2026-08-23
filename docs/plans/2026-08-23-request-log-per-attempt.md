# request-log 内层 attempt 独立日志实现计划

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 让 route-log 详情页**内层每条 attempt**（step 1、1.1、1.2、2、3、4…）都能显示独立的「完整日志」跳转按钮——成功、失败、流式中断都各有自己的一条 request-log 记录，点击跳转 `/request-logs/{uuid}`。

**Architecture:** 核心是让 request_logs 从「一条 pipe 一条」变为「每次渠道尝试一条」：request-log 插件去掉 `__request_log_recorded` 早退，每次 `HandleBeforeAttempt` 触发都生成**新的 UUID**、写**新行**，并把本次 UUID 暂存 `pipe.Metadata[__request_log_attempt_id]`；model-gateway 的 `proxyAttemptLog`/`proxyStreamAttempt` 读该 key 写入 `route_attempts.request_log_id`（新增列 v25）；route-log 的 Detail/List 带出，前端内层行渲染按钮。

**Tech Stack:** Go, SQLite (loadout.db migration v25), Vue 3 (RouteLogTable.vue), shadcn-vue。

---

## 决策记录（用户已拍板，实现时勿改）

| 维度 | 决策 | 说明 |
|---|---|---|
| 日志颗粒度 | **每次渠道尝试一条**（per-attempt） | 用户原话：「每一个请求都会进入我的路由能力，只要匹配他就会有日志」；成功失败都要有自己的日志 |
| UUID 来源 | request-log 每次 HandleBeforeAttempt 触发时**自造新 UUID**（newRequestLogID） | 不再复用 model-gateway 的 pipe 级 MetadataRequestLogID（那是 pipe 级共享的，failover 同 pipe 所有 attempt 拿同一个） |
| attempt ↔ 日志关联 | model-gateway 写 route_attempts 时读 `pipe.Metadata[__request_log_attempt_id]` | request-log 在 HandleBeforeAttempt 里写该 key；proxyAttemptLog/proxyStreamAttempt 读它传给 contracts.RouteAttempt.RequestLogID |
| 外层按钮 | 保留现有行为 | route_requests.request_log_id 仍 UPDATE（首次命中的 UUID）；外层按钮跳第一条 attempt 的日志 |
| 视觉子段（1.1/1.2） | **无独立日志，不显示按钮** | 视觉请求由 vision_v2 插件 `http.Client` 直连上游（describe.go:250-259），**不经 gateway proxyAttempt 管线** → 不触发 request-log → request_log_id 为空。边界写死，不做 |
| 未命中能力路由的 attempt | 无日志、无按钮 | 与现有语义一致（列 NULL → 前端不渲染） |
| 失败 attempt | 有日志 | HandleBeforeAttempt 在**请求发出前**触发（proxy.go:302），失败也先写了半条 → 靠 ProxyUpstreamFailed / self-heal 收尾为 failed |

### 决策记录补充（sub-agent audit 后修订）

| 维度 | 决策 | 说明 |
|---|---|---|
| **收尾关联（P0 修复）** | HandleBeforeAttempt 里 **覆写 `pipe.Metadata[metadataKey]` 为 per-attempt UUID** | 收尾事件（after-upstream/stream-chunk/upstream-failed）统一走 `pipeRequestLogID`（service.go:327-344）读 `MetadataRequestLogID`。覆写后收尾拿到的是**本次 attempt** 的 UUID → `finishRequestLog` 能命中行。若只写新 key 不覆写，收尾读 pipe 级 UUID 找不到行 → **所有 per-attempt 行永久 running**（P0） |
| 中间失败 attempt 终态 | 靠 self-heal 兜底（已知限制） | failover 链中先前失败的 attempt：after 事件只在最终路径发一次且 keyed 最新 UUID → 中间行留 running，`healStuckList`/`Detail` 访问时按 `route_requests.result` 收尾为 failed/stream_interrupted（service.go:647-673）。真实 4xx/429 错误体在普通模型 failover 场景拿不到（无输出事件），只落 stream_interrupted |
| :448 / :660 关联 | 依赖 Task 4 的 CASE WHEN 保留 | after-hook 拒绝（proxy.go:448）与 flushEst（:660）直接构造 RouteAttempt 传空 RequestLogID，靠 running 占位（:432/:479）已写列 + UPSERT CASE WHEN 保留旧值 —— **Task 4 的 CASE WHEN 是硬依赖，实现时必须落地** |
| lookupRequestLogID | **删除**（P1） | 改造后每次 HandleBeforeAttempt 都自造新 UUID，反查复用逻辑成死代码 |
| 外层按钮指向 | 首次命中 attempt 的 UUID（已知限制） | route_requests.request_log_id 仍只写首次；failover 时收尾 finalize 的是最新行，外层按钮可能跳到 stream_interrupted 行。与现有「一条 pipe 一条日志」的 UX 相比略不一致，属可接受范围（内层按钮才是 per-attempt 的正主） |
| request_logs INSERT UPSERT | 简化为纯 INSERT | ON CONFLICT(id) 在每次新 UUID 下永不触发（P2），删掉 DO UPDATE 分支 |
| 既有测试处置 | 4 个测试需同步 | `TestHandleBeforeAttemptConsumesInjectedUUID`（断言注入 UUID 消费 + re-entry 早退，改造后必挂，删/改）；`TestHandleAfterUpstreamSuccess` / `TestHandleUpstreamFailed`（从 metadataKey 取 uuid，覆写方案下仍成立，**无需改**）；`TestHandleBeforeAttemptIdempotentSamePipe`（断言同 pipe 两次 count=1，必挂，改为 per-attempt 语义）；`TestHandleBeforeAttemptReuseOnRetry`（断言 uuid1==uuid2，必挂，改为新 UUID 新行） |

---

## 背景事实（已核实，代码位置）

1. **request-log 现状早退**：`plugins/request-log/service.go:192-195`，`recordedKey` 使同 pipe 只写一条日志；UUID 消费 model-gateway 生成的 `MetadataRequestLogID`（`proxy.go:297-299`，pipe 级幂等）→ **所有 attempt 共享同一条日志**，这是「内层没有独立日志」的根源。
2. **事件触发时机**（已核实 `proxy.go:280-308`）：`proxyAttempt` 内先生成 pipe 级 UUID（:297-299）→ `Waterfall(ProxyBeforeAttempt)`（:302）→ request-log.HandleBeforeAttempt（此时 `__current_channel`/`__last_tried_channel` 已写入 metadata :285-292）→ 构建上游请求（:304）→ 发出 → `proxyAttemptLog`（:328 失败 / :389 等）写 route_attempts 行。**request-log 半条先写、attempt 行后写**，关联只能通过 metadata 传递。
3. **流式 attempt 两阶段**（`proxy.go:1094-1150`）：`proxyStreamAttempt` 先写 running 占位（step 已分配），流结束后同 step UPSERT 成 success。**request_log_id 必须在 UPSERT 时保留旧值**（不能因 done=true 传空把列清空）→ route-log `Attempt()` 的 ON CONFLICT UPDATE 需 COALESCE 保留。
4. **route_attempts 无 request_log_id 列**：当前最新迁移 v24（`migrate.go:529-538`，route_requests.request_log_id）。加 v25 = `ALTER TABLE route_attempts ADD COLUMN request_log_id TEXT`。**db_test.go:56 硬编码 `want 24` 需改 25**；表数量恒 21（v25 不加新表）。
5. **contracts.RouteAttempt**（`plugins/contracts/routing.go:99-137`）无 RequestLogID 字段 → 需加 `RequestLogID string \`json:"request_log_id,omitempty"\``。
6. **route-log Attempt() SQL**（`plugins/route-log/service.go:78`）：INSERT + ON CONFLICT(request_id, step_no) DO UPDATE，需在 VALUES 与 UPDATE 两处加 request_log_id 列。
7. **route-log Detail() 读 attempts**（`plugins/route-log/service.go:338`）：SELECT 硬编码列清单，需加 request_log_id；scan（:356）需加字段。
8. **前端类型**：`frontend/src/lib/types.ts:65-80` RouteAttempt 无 request_log_id → 加 `request_log_id?: string`。
9. **前端内层渲染**：`frontend/src/components/route-logs/RouteLogTable.vue:361-419`，attempt li 循环内无日志按钮 → 加 router-link（`v-if="attempt.request_log_id"`，跳 `/request-logs/${attempt.request_log_id}`）。外层按钮逻辑（:346-351）不动。
10. **视觉子段不经过 request-log**：`vision_v2/describe.go:250-259`、`tool_loop.go:474-489` 用 `http.Client` 直连上游 → 视觉 attempt（step 1.1/1.2）的 request_log_id 恒空，前端 v-if 自然不渲染按钮（无需特判）。
11. **metadata key 常量位置**：`MetadataRequestLogID = "__request_log_id"` 在 `plugins/model-gateway/types.go:209`。新增 `MetadataRequestLogAttemptID = "__request_log_attempt_id"` 并列定义（model-gateway 与 request-log 双向引用，放 model-gateway 最自洽）。

---

## 数据模型（migration v25）

```sql
-- v25: route-attempts-request-log-id
-- 内层 attempt 的独立 request-log 关联：request-log 插件在每次 proxy:before-attempt
-- 生成新 UUID 写 request_logs 新行，并暂存 pipe.Metadata[__request_log_attempt_id]；
-- model-gateway 写 route_attempts 时读它落本列。可空：未命中能力路由/视觉子段为 NULL。
-- 不加 UNIQUE（同 request_id 多行独立生成）。不加索引（无按此列查询需求）。
ALTER TABLE route_attempts ADD COLUMN request_log_id TEXT;
```

---

## 插件结构（文件清单）

改动既有文件：
- `plugins/model-gateway/types.go` — 加 `MetadataRequestLogAttemptID` 常量
- `plugins/model-gateway/proxy.go` — proxyAttemptLog/proxyStreamAttempt 读 metadata 传 RequestLogID
- `plugins/request-log/service.go` — 去早退、per-attempt UUID、写 attempt key、request_logs 多行
- `plugins/request-log/service_test.go` — per-attempt 回归测试
- `plugins/contracts/routing.go` — RouteAttempt 加 RequestLogID
- `plugins/route-log/service.go` — Attempt() SQL 加列、Detail() 读列
- `plugins/route-log/service_test.go` — Detail 带出 request_log_id 断言
- `core/db/migrate.go` — 追加 v25
- `core/db/db_test.go` — 迁移计数 24 → 25
- `frontend/src/lib/types.ts` — RouteAttempt 加 request_log_id
- `frontend/src/components/route-logs/RouteLogTable.vue` — 内层按钮

---

## Task 1: migration v25 + db_test 计数

**Files:**
- Modify: `core/db/migrate.go`（在 v24 块 `}, {` 之后追加）
- Modify: `core/db/db_test.go:56`（`want 24` → `want 25`）

**Step 1: 写失败测试（改计数）**

改 `core/db/db_test.go:56`：
```go
t.Fatalf("schema_migrations count = %d, want 25", count)
```

**Step 2: 跑测试确认失败**

Run: `go test ./core/db/ -run TestMigrateIsIdempotent`
Expected: FAIL（count=24, want 25）

**Step 3: 实现 v25**

`core/db/migrate.go` v24 块后追加：
```go
	}, {
		version: 25,
		name:    "route-attempts-request-log-id",
		sql: `
-- request-log 插件 per-attempt 关联列：route_attempts 行指向 request_logs 独立库
-- 主键 UUID。UUID 由 request-log 插件在每次 proxy:before-attempt 生成并暂存
-- pipe.Metadata[__request_log_attempt_id]，model-gateway 写 attempt 行时落本列。
-- 可空：未命中 request_log 能力路由的 attempt（含视觉子段）为 NULL。
ALTER TABLE route_attempts ADD COLUMN request_log_id TEXT;
`,
	}}
```

**Step 4: 跑测试确认通过**

Run: `go test ./core/db/`
Expected: PASS

**Step 5: Commit**

```bash
git add core/db/migrate.go core/db/db_test.go
git commit -m "feat(request-log): migration v25 route_attempts.request_log_id"
```

---

## Task 2: contracts.RouteAttempt 加 RequestLogID

**Files:**
- Modify: `plugins/contracts/routing.go:99-137`

**Step 1: 写失败测试**

`plugins/contracts/routing_test.go`（已有序列化测试）追加字段断言：
```go
func TestRouteAttemptRequestLogIDJSON(t *testing.T) {
	attempt := RouteAttempt{RequestID: "r1", StepNo: "1", RequestLogID: "abc123"}
	encoded, err := json.Marshal(attempt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"request_log_id":"abc123"`) {
		t.Fatalf("missing request_log_id in JSON: %s", encoded)
	}
	var decoded RouteAttempt
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.RequestLogID != "abc123" {
		t.Fatalf("RequestLogID = %q, want abc123", decoded.RequestLogID)
	}
}
```

**Step 2: 跑测试确认失败**

Run: `go test ./plugins/contracts/ -run TestRouteAttemptRequestLogIDJSON`
Expected: FAIL（字段不存在，序列化缺 key）

**Step 3: 实现**

`plugins/contracts/routing.go` RouteAttempt 结构体加字段（放 ChannelName 后）：
```go
	// RequestLogID 本次 attempt 的独立 request-log 主键（request-log.db request_logs.id）。
	// 由 request-log 插件在 before-attempt 生成，model-gateway 写 attempt 行时落库；
	// 为空表示该 attempt 未命中 request_log 能力路由（前端不显示日志入口）。
	RequestLogID       string         `json:"request_log_id,omitempty"`
```

**Step 4: 跑测试确认通过**

Run: `go test ./plugins/contracts/`
Expected: PASS

**Step 5: Commit**

```bash
git add plugins/contracts/routing.go plugins/contracts/routing_test.go
git commit -m "feat(contracts): RouteAttempt.RequestLogID per-attempt field"
```

---

## Task 3: model-gateway 常量 + proxyAttemptLog/proxyStreamAttempt 传 RequestLogID

**Files:**
- Modify: `plugins/model-gateway/types.go:206-210`
- Modify: `plugins/model-gateway/proxy.go:1038-1087`（proxyAttemptLog）、`1094-1170`（proxyStreamAttempt）

**Step 1: 写失败测试**

`plugins/model-gateway/proxy_attempt_test.go` 追加（参考现有 testPipe 构造）：
```go
func TestProxyAttemptLogCarriesRequestLogID(t *testing.T) {
	// 构造带 routeLog mock 的 Service（参照本文件现有测试模式），
	// pipe.Metadata[MetadataRequestLogAttemptID] = "uuid-attempt-1"
	// 调用 proxyAttemptLog → 断言 routeLog.Attempt 收到的 RouteAttempt.RequestLogID == "uuid-attempt-1"
}
```
（实现时按 proxy_attempt_test.go 现有 mock 模式落地，见文件内已有 routeLog 桩。）

**Step 2: 跑测试确认失败**

Run: `go test ./plugins/model-gateway/ -run TestProxyAttemptLogCarriesRequestLogID`
Expected: FAIL（proxyAttemptLog 未读 metadata key）

**Step 3: 实现**

`types.go` 常量（MetadataRequestLogID 旁）：
```go
// MetadataRequestLogAttemptID 每次渠道尝试的 request-log 关联 UUID 键。request-log
// 插件在 proxy:before-attempt 生成新 UUID 并写入，model-gateway 写 route_attempts
// 行时读取落 request_log_id 列（per-attempt 独立日志，区别于 pipe 级 MetadataRequestLogID）。
const MetadataRequestLogAttemptID = "__request_log_attempt_id"
```

`proxy.go` proxyAttemptLog 内 `message := ""` 块后加：
```go
	requestLogID, _ := pipe.Metadata[MetadataRequestLogAttemptID].(string)
```
并在 `contracts.RouteAttempt{...}` 中加：
```go
		RequestLogID:     requestLogID,
```

`proxy.go` proxyStreamAttempt 同样（running 占位与 success UPSERT 两处调用都读，读同一 key）：
```go
	requestLogID, _ := pipe.Metadata[MetadataRequestLogAttemptID].(string)
	// ... 放入 RouteAttempt{ RequestLogID: requestLogID }
```

**Step 4: 跑测试确认通过**

Run: `go test ./plugins/model-gateway/`
Expected: PASS

**Step 5: Commit**

```bash
git add plugins/model-gateway/types.go plugins/model-gateway/proxy.go plugins/model-gateway/proxy_attempt_test.go
git commit -m "feat(model-gateway): pass per-attempt request_log_id to route attempts"
```

---

## Task 4: route-log Attempt() 写列 + Detail() 读列

**Files:**
- Modify: `plugins/route-log/service.go:78`（Attempt INSERT/UPSERT）、`:338-379`（Detail 读 attempts）
- Modify: `plugins/route-log/service_test.go`

**Step 1: 写失败测试**

`plugins/route-log/service_test.go` 追加：
```go
func TestAttemptPersistsRequestLogID(t *testing.T) {
	db := openTestDB(t)
	service := NewService(db, nil)
	started := time.Now()
	if _, err := service.Attempt(context.Background(), contracts.RouteAttempt{
		RequestID: "r-attempt-log", StepNo: "1", Model: "m", StartedAt: started,
		RequestLogID: "uuid-attempt-1", Result: "success",
	}); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.QueryRow(`SELECT request_log_id FROM route_attempts WHERE request_id='r-attempt-log'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "uuid-attempt-1" {
		t.Fatalf("request_log_id = %q, want uuid-attempt-1", got)
	}
	// UPSERT 保留旧值：done=true 传空 RequestLogID 不清空
	if _, err := service.Attempt(context.Background(), contracts.RouteAttempt{
		RequestID: "r-attempt-log", StepNo: "1", Model: "m", StartedAt: started,
		Result: "success", // RequestLogID 空
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT request_log_id FROM route_attempts WHERE request_id='r-attempt-log'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "uuid-attempt-1" {
		t.Fatalf("request_log_id after UPSERT = %q, want preserved uuid-attempt-1", got)
	}
}
```

**Step 2: 跑测试确认失败**

Run: `go test ./plugins/route-log/ -run TestAttemptPersistsRequestLogID`
Expected: FAIL（no such column: request_log_id）

**Step 3: 实现**

`Attempt()` INSERT 的 VALUES 列表末尾（metadata_json 后）加 `request_log_id`：
```sql
INSERT INTO route_attempts(request_id, previous_attempt_id, step_no, action, model, channel_id, channel_ids_json, channel_base_url, channel_name, started_at, finished_at, first_byte_at, result, failure_class, status_code, error_message, error_body, duration_ms, stream, prompt_tokens, completion_tokens, cached_tokens, metadata_json, request_log_id) VALUES (?, ?, ?, ?, ?, COALESCE(NULLIF(?, ''), ''), ?, COALESCE(NULLIF(?, ''), ''), COALESCE(NULLIF(?, ''), ''), ?, ?, ?, ?, ?, NULLIF(?, 0), ?, ?, ?, ?, ?, ?, ?, ?, COALESCE(NULLIF(?, ''), '')) ON CONFLICT(request_id, step_no) DO UPDATE SET ...（其余不变）..., metadata_json=excluded.metadata_json, request_log_id=CASE WHEN excluded.request_log_id = '' THEN route_attempts.request_log_id ELSE excluded.request_log_id END
```
对应追加参数 `attempt.RequestLogID`（放参数列表末尾）。**UPSERT 用 CASE WHEN 保留旧值**（流式 done=true 传空不清空，事实 3）。

`Detail()` 读 attempts 的 SELECT 加 `request_log_id`（列清单末尾）：
```sql
SELECT id, request_id, previous_attempt_id, step_no, action, model, COALESCE(channel_id, ''), COALESCE(channel_ids_json, '[]'), COALESCE(channel_base_url, ''), COALESCE(channel_name, ''), started_at, finished_at, first_byte_at, result, failure_class, COALESCE(status_code, 0), error_message, COALESCE(error_body, ''), COALESCE(duration_ms, 0), COALESCE(stream, 0), COALESCE(prompt_tokens, 0), COALESCE(completion_tokens, 0), COALESCE(cached_tokens, 0), metadata_json, COALESCE(request_log_id, '') FROM route_attempts WHERE request_id=?
```
scan 加 `&attempt.RequestLogID`（`var` 声明区加 `requestLogID string`，或直接 `&attempt.RequestLogID`）。

**Step 4: 跑测试确认通过**

Run: `go test ./plugins/route-log/`
Expected: PASS

**Step 5: Commit**

```bash
git add plugins/route-log/service.go plugins/route-log/service_test.go
git commit -m "feat(route-log): persist & expose per-attempt request_log_id"
```

---

## Task 5: request-log 去早退 + per-attempt UUID + 写 attempt key

**Files:**
- Modify: `plugins/request-log/service.go:184-246`（HandleBeforeAttempt）
- Modify: `plugins/request-log/service_test.go`

**Step 1: 写失败测试**

`plugins/request-log/service_test.go` 追加：
```go
// TestHandleBeforeAttemptPerAttemptLogs 回归：同 pipe 多次触发（failover）必须
// 每次生成新 UUID 写新行（per-attempt 独立日志），并把本次 UUID 写入
// pipe.Metadata[MetadataRequestLogAttemptID] 供 model-gateway 关联 attempt 行。
func TestHandleBeforeAttemptPerAttemptLogs(t *testing.T) {
	reqDB, _ := openRequestLogDB(t.TempDir() + "/request-log.db")
	defer reqDB.Close()
	svc := testService(t, []types.CapabilityRoute{
		{Models: []string{"*"}, Capability: capabilityName, Route: types.RouteProxy},
	})
	svc.reqDB = reqDB

	pipe := testPipe("req-multi")
	if _, err := svc.HandleBeforeAttempt(pipe); err != nil {
		t.Fatal(err)
	}
	firstID, _ := pipe.Metadata[modelgateway.MetadataRequestLogAttemptID].(string)
	if firstID == "" {
		t.Fatal("first attempt: metadata __request_log_attempt_id missing")
	}
	// failover：同 pipe 再次触发（无 recorded 早退）
	if _, err := svc.HandleBeforeAttempt(pipe); err != nil {
		t.Fatal(err)
	}
	secondID, _ := pipe.Metadata[modelgateway.MetadataRequestLogAttemptID].(string)
	if secondID == "" || secondID == firstID {
		t.Fatalf("second attempt must get new id, first=%q second=%q", firstID, secondID)
	}
	var count int
	if err := reqDB.QueryRow(`SELECT count(*) FROM request_logs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("rows = %d, want 2 (per-attempt logs)", count)
	}
}
```

**Step 2: 跑测试确认失败**

Run: `go test ./plugins/request-log/ -run TestHandleBeforeAttemptPerAttemptLogs`
Expected: FAIL（现在早退，count=1 / 或 metadata key 缺失）

**Step 3: 实现**

`HandleBeforeAttempt` 改造：
1. **删掉 recorded 早退**（:192-195 整块删除，含 `recordedKey` 常量定义 :50-52）。
2. model 取真实模型（已在上一次修复完成，保留 `model := pipe.Request.Model`）。
3. **每次生成新 UUID**：`uuid := newRequestLogID()`，不再消费 `pipe.Metadata[metadataKey]`。
4. **覆写两个 metadata key**（P0 修复，收尾事件靠它命中行）：
   ```go
   pipe.Metadata[modelgateway.MetadataRequestLogAttemptID] = uuid // model-gateway 写 attempt 行关联
   pipe.Metadata[metadataKey] = uuid                              // 收尾事件 pipeRequestLogID 读取（覆写 pipe 级 key 为本次 attempt UUID）
   ```
   （原 :212-219 的「消费注入 UUID → 反查 → 自造」三段删除，直接自造。）
5. **外层关联保持首次更新**：route_requests.request_log_id 的 UPDATE 条件 `AND COALESCE(request_log_id, '') = ''` 已保证只写首次，保留不动。
6. **写 request_logs 新行**：INSERT 简化为纯 INSERT（**去掉 ON CONFLICT(id) DO UPDATE 分支**，每次新 UUID 永不冲突）：
   ```go
   if _, err := s.reqDB.Exec(`INSERT INTO request_logs(id, request_id, model, channel, stream, started_at, result, request_json, created_at) VALUES (?, ?, ?, ?, ?, ?, 'running', ?, ?)`,
       uuid, pipe.RequestID, model, channel, boolToInt(pipe.Request.Stream), started.Format(time.RFC3339Nano), string(reqJSON), started.Format(time.RFC3339Nano)); err != nil {
   ```
7. **删除死代码**：`lookupRequestLogID`（:250-259）整个函数删除（改造后每次自造新 UUID，无反查复用）。
8. 移除 `pipe.Metadata[recordedKey] = true`（:244）与 recordedKey 常量。

注意：`pipeRequestLogID`（:326-343）的 metadata 优先分支已覆盖（key 被覆写为本次 attempt UUID）；其反查兜底（按 request_id 查最新行）保留不动，作为 metadata 丢失场景（pipe 重建）防御。

**Step 4: 跑测试确认通过**

Run: `go test ./plugins/request-log/`
Expected: PASS。**需同步处置的既有测试**（audit P0/P1）：
- `TestHandleBeforeAttemptConsumesInjectedUUID`（:266-296）：断言「消费注入 UUID + re-entry 早退」——语义已废，**改为**「同 pipe 两次触发各得新 UUID、两行」或删除（与新增的 PerAttemptLogs 测试重复，删除）。
- `TestHandleBeforeAttemptIdempotentSamePipe`（:195-218）：断言同 pipe 两次 count=1 —— **改为断言 count=2**（per-attempt 语义）。
- `TestHandleBeforeAttemptReuseOnRetry`（:222-262）：断言 uuid1==uuid2 —— **改为断言重试（新 pipe 同 request_id）产生新 UUID、新行**。
- `TestHandleAfterUpstreamSuccess`（:318+）/ `TestHandleUpstreamFailed`：从 `pipe.Metadata[metadataKey]` 取 uuid —— 覆写方案下 metadataKey 就是本次 attempt UUID，**无需改**（跑通即可）。

**Step 5: Commit**

```bash
git add plugins/request-log/service.go plugins/request-log/service_test.go
git commit -m "feat(request-log): per-attempt independent logs with attempt_id metadata"
```

---

## Task 6: 前端类型 + 内层按钮

**Files:**
- Modify: `frontend/src/lib/types.ts:65-80`
- Modify: `frontend/src/components/route-logs/RouteLogTable.vue:361-419`

**Step 1: 加类型**

`types.ts` RouteAttempt 加（duration_ms 后）：
```ts
  /** 本次 attempt 的独立 request-log 主键（命中 request_log 能力路由才有；空则无日志入口） */
  request_log_id?: string
```

**Step 2: 加内层按钮**

`RouteLogTable.vue` attempt li 内，`formatDuration(attempt.duration_ms)` span 后加：
```html
<router-link v-if="attempt.request_log_id"
  :to="`/request-logs/${attempt.request_log_id}`"
  class="text-xs text-primary hover:underline" @click.stop>日志</router-link>
```
（`@click.stop` 防冒泡触发行 toggle；外层按钮 :346-351 不动。）

**Step 3: 构建验证**

Run: `cd frontend && npm run build`（或 `pnpm build`，按项目脚本）
Expected: PASS，产物 dist/assets/RouteLogTable-*.js 含 `request_log_id` 渲染。

**Step 4: Commit**

```bash
git add frontend/src/lib/types.ts frontend/src/components/route-logs/RouteLogTable.vue
git commit -m "feat(frontend): per-attempt request log link in RouteLogTable"
```

---

## 明确不做的事（scope 边界）

- **视觉子段（step 1.1/1.2）不记独立日志、不显示按钮**：视觉请求由 vision_v2 插件直连上游，不经 gateway proxyAttempt → 不触发 request-log。前端 v-if 空 request_log_id 自然不渲染，无需特判。若要视觉也有日志属新 feature（需 vision_v2 接入 request-log 事件），另立 plan。
- **未命中能力路由的 attempt**：不记日志、无按钮（与现状一致）。
- **request_logs 表结构不动**：不加 per-attempt 专用列（request_id 已能区分同请求多行）。
- **不做「合并展示」**：同一次外层请求的多条 attempt 日志在 /api/request-logs 列表里各自成行（request_id 相同、UUID 不同），不聚合。

---

## 风险与决策点

1. **RecordedKey 语义变更**：删早退后，同 pipe 每次 HandleBeforeAttempt 都写行。视觉子段不受影响（不经管线）；安检拒绝的 attempt（rejectBySecurity，proxy.go:307）也会先写半条再被拒 → 会被记录（行为与现状一致，plan 原文事实 12 已确认可接受）。
2. **流式 attempt 的 request_log_id 保留**：Task 4 的 UPSERT CASE WHEN 保证 done=true 传空不清空（事实 3 关键；audit 确认 :448/:660 依赖此分支）。
3. **重试语义变更**：客户端重试（新 pipe 同 request_id）从「复用 UUID」变「新 UUID 新行」——符合 per-attempt 语义；既有测试需同步（Task 5 Step 4 清单：ConsumesInjectedUUID 删、IdempotentSamePipe/ReuseOnRetry 改语义、AfterUpstream/UpstreamFailed 无需改）。
4. **收尾命中（P0 已修）**：HandleBeforeAttempt 覆写 `pipe.Metadata[metadataKey]` 为 per-attempt UUID，收尾事件经 pipeRequestLogID 命中本次行。若漏掉覆写 → 收尾找不到行 → 永久 running。
5. **中间失败 attempt 终态靠 self-heal（已知限制）**：failover 链中间 attempt 无输出事件，列表/详情访问时按 route_requests.result 收尾。真实错误体（4xx/429）在普通模型 failover 场景丢失，只落 stream_interrupted；聚合路径（ProxyUpstreamFailed）能拿到 error_body。可接受，属架构边界。
6. **db_test.go 计数**：v25 后硬编码 24 → 25（Task 1），漏改则迁移测试挂。
7. **真实环境验证**（用户强需求）：改完全部后必须重启 loadout.exe 并用真实 volcengine_auto → hy3 请求验证：① 内层 attempt 行出现「日志」按钮；② 点击跳转 /api/request-logs/{uuid} 返回 200 且带 response；③ 失败 attempt（如额度不足）也有按钮且日志 result=failed；④ failover 多 attempt 场景每行独立 UUID、互不覆盖。

---

## Task 列表（执行顺序）

1. migration v25 + db_test 计数（Task 1）
2. contracts.RouteAttempt.RequestLogID（Task 2）
3. model-gateway 常量 + proxyAttemptLog/proxyStreamAttempt 传值（Task 3）
4. route-log Attempt()/Detail() 读写列（Task 4）
5. request-log 去早退 + per-attempt UUID（Task 5）
6. 前端类型 + 内层按钮（Task 6）
7. 全量测试：`go test ./...` + 前端 build
8. 重建 bin/loadout.exe + 真实环境验证
