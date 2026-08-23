package requestlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"loadout/core/db"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// testService 种子能力路由（JSON 文件模式，不依赖 repo），返回 Service。
func testService(t *testing.T, routes []types.CapabilityRoute) *Service {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) > 0 {
		if err := st.Write(types.FileCapabilityRoutes, routes); err != nil {
			t.Fatal(err)
		}
	}
	return NewService(st, slog.New(slog.DiscardHandler), nil, nil)
}

func TestDecideRouteMiss(t *testing.T) {
	svc := testService(t, nil)
	route, err := svc.DecideRoute("gpt-4o", "")
	if err != nil {
		t.Fatal(err)
	}
	if route != nil {
		t.Fatalf("no routes should miss, got %+v", route)
	}
}

func TestDecideRouteNativeStops(t *testing.T) {
	svc := testService(t, []types.CapabilityRoute{
		{Models: []string{"*"}, Capability: capabilityName, Route: types.RouteNative},
	})
	route, err := svc.DecideRoute("gpt-4o", "")
	if err != nil {
		t.Fatal(err)
	}
	if route == nil || route.Route != types.RouteNative {
		t.Fatalf("want native route, got %+v", route)
	}
}

func TestDecideRouteProxyWildcardModel(t *testing.T) {
	svc := testService(t, []types.CapabilityRoute{
		{Models: []string{"*"}, Capability: capabilityName, Route: types.RouteProxy},
	})
	route, err := svc.DecideRoute("gpt-4o", "")
	if err != nil {
		t.Fatal(err)
	}
	if route == nil || route.Route != types.RouteProxy {
		t.Fatalf("want proxy route, got %+v", route)
	}
}

func TestDecideRouteChannelScoped(t *testing.T) {
	svc := testService(t, []types.CapabilityRoute{
		{Models: []string{"*"}, Capability: capabilityName, Route: types.RouteProxy, ChannelIDs: []string{"c1"}},
	})
	// 命中绑定渠道
	route, err := svc.DecideRoute("gpt-4o", "c1")
	if err != nil {
		t.Fatal(err)
	}
	if route == nil {
		t.Fatal("channel c1 should match")
	}
	// 未命中其他渠道
	route, err = svc.DecideRoute("gpt-4o", "c2")
	if err != nil {
		t.Fatal(err)
	}
	if route != nil {
		t.Fatalf("channel c2 should miss, got %+v", route)
	}
}

func TestDecideRouteUnboundChannelMatchesAny(t *testing.T) {
	// 路由未绑定渠道（channel_ids/base_urls 均空）= 全渠道命中（与 sensitive-filter 一致）
	svc := testService(t, []types.CapabilityRoute{
		{Models: []string{"*"}, Capability: capabilityName, Route: types.RouteProxy},
	})
	route, err := svc.DecideRoute("gpt-4o", "c2")
	if err != nil {
		t.Fatal(err)
	}
	if route == nil {
		t.Fatal("unbound route should match any channel")
	}
}

// testPipe 构造一次渠道尝试的管线（请求体含 base64 图与 sk- 密钥，用于脱敏断言）。
func testPipe(requestID string) *modelgateway.ProxyPipeline {
	return &modelgateway.ProxyPipeline{
		RequestID: requestID,
		Request: &modelgateway.ProxyRequest{
			Method: "POST",
			Path:   "chat/completions",
			Query:  "model=gpt-4o",
			Header: http.Header{
				"Authorization": {"Bearer sk-abc123"},
				"Content-Type":  {"application/json"},
			},
			Body:  []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"data:image/png;base64,iVBORw0KGgo="}],"api_key":"sk-secret-xyz"}`),
			Model: "gpt-4o",
		},
		Metadata: map[string]any{"__current_channel": "c1"},
	}
}

func TestHandleBeforeAttemptCapturesRequest(t *testing.T) {
	reqDB, err := openRequestLogDB(t.TempDir() + "/request-log.db")
	if err != nil {
		t.Fatal(err)
	}
	defer reqDB.Close()
	svc := testService(t, []types.CapabilityRoute{
		{Models: []string{"*"}, Capability: capabilityName, Route: types.RouteProxy},
	})
	svc.reqDB = reqDB

	pipe := testPipe("req-1")
	out, err := svc.HandleBeforeAttempt(pipe)
	if err != nil {
		t.Fatal(err)
	}
	if out != pipe {
		t.Fatal("handler must return original payload")
	}

	// UUID 写入 metadata
	uuid, _ := pipe.Metadata[metadataKey].(string)
	if uuid == "" {
		t.Fatal("metadata __request_log_id missing")
	}

	// 半条：running + request_json 脱敏
	var result, reqJSON string
	if err := reqDB.QueryRow(`SELECT result, request_json FROM request_logs WHERE id = ?`, uuid).Scan(&result, &reqJSON); err != nil {
		t.Fatal(err)
	}
	if result != "running" {
		t.Fatalf("result = %q, want running", result)
	}
	var snap requestSnapshot
	if err := json.Unmarshal([]byte(reqJSON), &snap); err != nil {
		t.Fatal(err)
	}
	if got := snap.Headers.Get("Authorization"); got != "***" {
		t.Fatalf("Authorization header = %q, want ***", got)
	}
	if strings.Contains(snap.Body, "sk-secret-xyz") {
		t.Fatal("body still contains raw sk- secret")
	}
	if !strings.Contains(snap.Body, "sk-***") {
		t.Fatal("body missing sk-*** redaction")
	}
	if !strings.Contains(snap.Body, "[image: image/png, 8B]") {
		t.Fatalf("body missing image placeholder: %s", snap.Body)
	}
}

func TestHandleBeforeAttemptIdempotentSamePipe(t *testing.T) {
	reqDB, _ := openRequestLogDB(t.TempDir() + "/request-log.db")
	defer reqDB.Close()
	svc := testService(t, []types.CapabilityRoute{
		{Models: []string{"*"}, Capability: capabilityName, Route: types.RouteProxy},
	})
	svc.reqDB = reqDB

	pipe := testPipe("req-2")
	if _, err := svc.HandleBeforeAttempt(pipe); err != nil {
		t.Fatal(err)
	}
	// failover 同一 pipe 再次触发：metadata 已有 UUID，直接跳过
	if _, err := svc.HandleBeforeAttempt(pipe); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := reqDB.QueryRow(`SELECT count(*) FROM request_logs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("rows = %d, want 1 (idempotent)", count)
	}
}

// TestHandleBeforeAttemptReuseOnRetry 客户端重试复用同一 X-Request-Id：
// 新 pipe 不带 metadata，但 route_requests 列已有 UUID → 复用，不产生孤儿行。
func TestHandleBeforeAttemptReuseOnRetry(t *testing.T) {
	loadout, err := db.Open(t.TempDir() + "/loadout.db")
	if err != nil {
		t.Fatal(err)
	}
	defer loadout.Close()
	if _, err := loadout.Exec(`INSERT INTO route_requests(request_id, requested_model, started_at, result) VALUES ('req-3', 'gpt-4o', '2026-01-01T00:00:00Z', 'running')`); err != nil {
		t.Fatal(err)
	}
	reqDB, _ := openRequestLogDB(t.TempDir() + "/request-log.db")
	defer reqDB.Close()
	svc := testService(t, []types.CapabilityRoute{
		{Models: []string{"*"}, Capability: capabilityName, Route: types.RouteProxy},
	})
	svc.reqDB = reqDB
	svc.loadout = loadout

	first := testPipe("req-3")
	if _, err := svc.HandleBeforeAttempt(first); err != nil {
		t.Fatal(err)
	}
	uuid1, _ := first.Metadata[metadataKey].(string)

	// 客户端重试：新 pipe（新 metadata），同 request_id
	retry := testPipe("req-3")
	delete(retry.Metadata, metadataKey)
	if _, err := svc.HandleBeforeAttempt(retry); err != nil {
		t.Fatal(err)
	}
	uuid2, _ := retry.Metadata[metadataKey].(string)
	if uuid1 == "" || uuid1 != uuid2 {
		t.Fatalf("retry must reuse uuid: %q vs %q", uuid1, uuid2)
	}
	var count int
	if err := reqDB.QueryRow(`SELECT count(*) FROM request_logs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("rows = %d, want 1 (reuse on retry)", count)
	}
}

// TestHandleBeforeAttemptConsumesInjectedUUID 回归：model-gateway 已在实际请求位置
// （proxyAttempt）生成 UUID 并写入 metadata，handler 必须消费而非自造。
func TestHandleBeforeAttemptConsumesInjectedUUID(t *testing.T) {
	reqDB, _ := openRequestLogDB(t.TempDir() + "/request-log.db")
	defer reqDB.Close()
	svc := testService(t, []types.CapabilityRoute{
		{Models: []string{"*"}, Capability: capabilityName, Route: types.RouteProxy},
	})
	svc.reqDB = reqDB

	pipe := testPipe("req-inj")
	pipe.Metadata[metadataKey] = "injected-uuid-0001" // 模拟 model-gateway 生成
	if _, err := svc.HandleBeforeAttempt(pipe); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := reqDB.QueryRow(`SELECT count(*) FROM request_logs WHERE id = 'injected-uuid-0001'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("rows with injected uuid = %d, want 1 (must consume not regenerate)", count)
	}
	// 已记录标记：同 pipe 再次触发早退
	if _, err := svc.HandleBeforeAttempt(pipe); err != nil {
		t.Fatal(err)
	}
	if err := reqDB.QueryRow(`SELECT count(*) FROM request_logs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("rows after re-entry = %d, want 1 (recorded early-exit)", count)
	}
}

// TestHandleBeforeAttemptNoRouteSkips 未命中路由：不记录。
func TestHandleBeforeAttemptNoRouteSkips(t *testing.T) {
	reqDB, _ := openRequestLogDB(t.TempDir() + "/request-log.db")
	defer reqDB.Close()
	svc := testService(t, nil) // 无路由
	svc.reqDB = reqDB

	if _, err := svc.HandleBeforeAttempt(testPipe("req-4")); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := reqDB.QueryRow(`SELECT count(*) FROM request_logs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rows = %d, want 0 (no route)", count)
	}
}

// svcWithDB 构造带独立库的 Service（proxy 路由）。
func TestHandleAfterUpstreamSuccess(t *testing.T) {
	reqDB, err := openRequestLogDB(t.TempDir() + "/request-log.db")
	if err != nil {
		t.Fatal(err)
	}
	defer reqDB.Close()
	svc := testService(t, []types.CapabilityRoute{
		{Models: []string{"*"}, Capability: capabilityName, Route: types.RouteProxy},
	})
	svc.reqDB = reqDB

	pipe := testPipe("req-5")
	if _, err := svc.HandleBeforeAttempt(pipe); err != nil {
		t.Fatal(err)
	}
	uuid, _ := pipe.Metadata[metadataKey].(string)
	pipe.Metadata["__last_tried_channel"] = "c9" // 最终渠道回填

	ap := &modelgateway.AfterUpstreamPayload{
		Pipe: pipe,
		Response: &modelgateway.ProxyResponse{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       []byte(`{"choices":[{"message":{"content":"hi"}}]}`),
		},
	}
	out, err := svc.HandleAfterUpstream(ap)
	if err != nil {
		t.Fatal(err)
	}
	if out != ap {
		t.Fatal("handler must return original payload")
	}

	var result, respJSON, channel string
	var status int
	var finished sql.NullString
	if err := reqDB.QueryRow(`SELECT result, http_status, channel, response_json, finished_at FROM request_logs WHERE id = ?`, uuid).
		Scan(&result, &status, &channel, &respJSON, &finished); err != nil {
		t.Fatal(err)
	}
	if result != "success" {
		t.Fatalf("result = %q, want success", result)
	}
	if status != 200 {
		t.Fatalf("http_status = %d, want 200", status)
	}
	if channel != "c9" {
		t.Fatalf("channel = %q, want c9 (回填)", channel)
	}
	if !finished.Valid {
		t.Fatal("finished_at should be set")
	}
	if !strings.Contains(respJSON, "hi") {
		t.Fatalf("response_json missing body: %s", respJSON)
	}
}

func TestHandleUpstreamFailed(t *testing.T) {
	reqDB, err := openRequestLogDB(t.TempDir() + "/request-log.db")
	if err != nil {
		t.Fatal(err)
	}
	defer reqDB.Close()
	svc := testService(t, []types.CapabilityRoute{
		{Models: []string{"*"}, Capability: capabilityName, Route: types.RouteProxy},
	})
	svc.reqDB = reqDB

	pipe := testPipe("req-6")
	if _, err := svc.HandleBeforeAttempt(pipe); err != nil {
		t.Fatal(err)
	}
	uuid, _ := pipe.Metadata[metadataKey].(string)

	fp := &modelgateway.ProxyFailurePayload{
		Pipe:       pipe,
		StatusCode: 429,
		ErrorBody:  `{"error":{"message":"rate limit exceeded"}}`,
	}
	if _, err := svc.HandleUpstreamFailed(fp); err != nil {
		t.Fatal(err)
	}

	var result, respJSON string
	var status int
	if err := reqDB.QueryRow(`SELECT result, http_status, response_json FROM request_logs WHERE id = ?`, uuid).
		Scan(&result, &status, &respJSON); err != nil {
		t.Fatal(err)
	}
	if result != "failed" {
		t.Fatalf("result = %q, want failed", result)
	}
	if status != 429 {
		t.Fatalf("http_status = %d, want 429", status)
	}
	if !strings.Contains(respJSON, "rate limit") {
		t.Fatalf("response_json missing error body: %s", respJSON)
	}
}

func TestHandleStreamChunkAssemblesUntilDone(t *testing.T) {
	reqDB, err := openRequestLogDB(t.TempDir() + "/request-log.db")
	if err != nil {
		t.Fatal(err)
	}
	defer reqDB.Close()
	svc := testService(t, []types.CapabilityRoute{
		{Models: []string{"*"}, Capability: capabilityName, Route: types.RouteProxy},
	})
	svc.reqDB = reqDB

	pipe := testPipe("req-7")
	pipe.Request.Stream = true
	if _, err := svc.HandleBeforeAttempt(pipe); err != nil {
		t.Fatal(err)
	}
	uuid, _ := pipe.Metadata[metadataKey].(string)

	chunks := []string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"你\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"好\"}}]}\n\n",
		"data: [DONE]\n\n",
	}
	for _, c := range chunks {
		if _, err := svc.HandleStreamChunk(&modelgateway.StreamChunkPayload{Pipe: pipe, Data: []byte(c)}); err != nil {
			t.Fatal(err)
		}
	}

	var result, respJSON string
	if err := reqDB.QueryRow(`SELECT result, response_json FROM request_logs WHERE id = ?`, uuid).
		Scan(&result, &respJSON); err != nil {
		t.Fatal(err)
	}
	if result != "success" {
		t.Fatalf("result = %q, want success", result)
	}
	// SSE 原文完整拼接（含前两个 data 行与 [DONE] 行）
	if !strings.Contains(respJSON, "你") || !strings.Contains(respJSON, "好") || !strings.Contains(respJSON, "[DONE]") {
		t.Fatalf("response_json missing stream content: %s", respJSON)
	}
}

func TestHandleStreamChunkNoDoneStaysRunning(t *testing.T) {
	reqDB, err := openRequestLogDB(t.TempDir() + "/request-log.db")
	if err != nil {
		t.Fatal(err)
	}
	defer reqDB.Close()
	svc := testService(t, []types.CapabilityRoute{
		{Models: []string{"*"}, Capability: capabilityName, Route: types.RouteProxy},
	})
	svc.reqDB = reqDB

	pipe := testPipe("req-8")
	pipe.Request.Stream = true
	if _, err := svc.HandleBeforeAttempt(pipe); err != nil {
		t.Fatal(err)
	}
	uuid, _ := pipe.Metadata[metadataKey].(string)

	// 中断：只有部分 chunk，无 [DONE]（模拟客户端断开/EOF）
	if _, err := svc.HandleStreamChunk(&modelgateway.StreamChunkPayload{Pipe: pipe, Data: []byte("data: {\"x\":1}\n\n")}); err != nil {
		t.Fatal(err)
	}
	var result string
	if err := reqDB.QueryRow(`SELECT result FROM request_logs WHERE id = ?`, uuid).Scan(&result); err != nil {
		t.Fatal(err)
	}
	if result != "running" {
		t.Fatalf("result = %q, want running (waiting for self-heal)", result)
	}
}

// seedRows 直接插入 request_logs 测试行。
func seedRows(t *testing.T, reqDB *sql.DB, rows ...[]any) {
	t.Helper()
	for _, r := range rows {
		if _, err := reqDB.Exec(`INSERT INTO request_logs(id, request_id, model, channel, http_status, stream, started_at, result, request_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, '{}', ?)`, r...); err != nil {
			t.Fatal(err)
		}
	}
}

func TestListFilters(t *testing.T) {
	reqDB, err := openRequestLogDB(t.TempDir() + "/request-log.db")
	if err != nil {
		t.Fatal(err)
	}
	defer reqDB.Close()
	svc := testService(t, nil)
	svc.reqDB = reqDB

	seedRows(t, reqDB,
		[]any{"u1", "r1", "gpt-4o", "c1", 200, 0, "2026-08-01T00:00:00Z", "success", "2026-08-01T00:00:00Z"},
		[]any{"u2", "r2", "gpt-4o", "c2", 429, 0, "2026-08-02T00:00:00Z", "failed", "2026-08-02T00:00:00Z"},
		[]any{"u3", "r3", "claude-3", "c1", 200, 1, "2026-08-03T00:00:00Z", "success", "2026-08-03T00:00:00Z"},
	)
	ctx := context.Background()

	cases := []struct {
		name   string
		filter requestLogFilter
		total  int
	}{
		{"model", requestLogFilter{Model: "gpt-4o"}, 2},
		{"channel", requestLogFilter{Channel: "c1"}, 2},
		{"result", requestLogFilter{Result: "failed"}, 1},
		{"status_code", requestLogFilter{StatusCode: 429}, 1},
		{"stream", requestLogFilter{Stream: intPtr(1)}, 1},
		{"request_id", requestLogFilter{RequestID: "r2"}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			page, err := svc.List(ctx, c.filter)
			if err != nil {
				t.Fatal(err)
			}
			if page.Total != c.total {
				t.Fatalf("total = %d, want %d", page.Total, c.total)
			}
		})
	}

	// 时间范围
	from := mustTime(t, "2026-08-02T00:00:00Z")
	page, err := svc.List(ctx, requestLogFilter{From: &from})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 {
		t.Fatalf("from-filter total = %d, want 2", page.Total)
	}

	// 分页 limit/offset
	page, err = svc.List(ctx, requestLogFilter{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Total != 3 {
		t.Fatalf("paged len=%d total=%d, want 1/3", len(page.Items), page.Total)
	}
	// 排序：started_at DESC → u3(08-03) > u2(08-02) > u1(08-01)；offset=1 跳过 u3
	if page.Items[0].ID != "u2" {
		t.Fatalf("first item = %s, want u2 (DESC + offset 1)", page.Items[0].ID)
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func intPtr(n int) *int { return &n }

func TestDetailNotFound(t *testing.T) {
	reqDB, _ := openRequestLogDB(t.TempDir() + "/request-log.db")
	defer reqDB.Close()
	svc := testService(t, nil)
	svc.reqDB = reqDB
	if _, err := svc.Detail(context.Background(), "nope"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("want ErrNoRows, got %v", err)
	}
}

func TestDetailSelfHealStuckRunning(t *testing.T) {
	reqDB, _ := openRequestLogDB(t.TempDir() + "/request-log.db")
	defer reqDB.Close()
	svc := testService(t, nil)
	svc.reqDB = reqDB
	// 卡 running：2 小时前开始（远超 60s 阈值），无 loadout（route_requests 查不到 → stream_interrupted）
	old := time.Now().Add(-2 * time.Hour).UTC()
	seedRows(t, reqDB,
		[]any{"stuck1", "req-stuck", "gpt-4o", "c1", 0, 1, old.Format(time.RFC3339Nano), "running", old.Format(time.RFC3339Nano)},
	)
	d, err := svc.Detail(context.Background(), "stuck1")
	if err != nil {
		t.Fatal(err)
	}
	if d.Result != "stream_interrupted" {
		t.Fatalf("result = %q, want stream_interrupted", d.Result)
	}
	if d.FinishedAt == nil {
		t.Fatal("finished_at should be set after heal")
	}
}

func TestDetailSelfHealPromotesToFailed(t *testing.T) {
	loadout, err := db.Open(t.TempDir() + "/loadout.db")
	if err != nil {
		t.Fatal(err)
	}
	defer loadout.Close()
	reqDB, _ := openRequestLogDB(t.TempDir() + "/request-log.db")
	defer reqDB.Close()
	svc := testService(t, nil)
	svc.reqDB = reqDB
	svc.loadout = loadout

	old := time.Now().Add(-2 * time.Hour).UTC()
	seedRows(t, reqDB,
		[]any{"stuck2", "req-failed", "gpt-4o", "c1", 0, 0, old.Format(time.RFC3339Nano), "running", old.Format(time.RFC3339Nano)},
	)
	// route_requests 侧已 failed（429）
	if _, err := loadout.Exec(`INSERT INTO route_requests(request_id, requested_model, started_at, result, http_status) VALUES ('req-failed', 'gpt-4o', ?, 'failed', 429)`, old.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	d, err := svc.Detail(context.Background(), "stuck2")
	if err != nil {
		t.Fatal(err)
	}
	if d.Result != "failed" {
		t.Fatalf("result = %q, want failed (route side already failed)", d.Result)
	}
	if d.HTTPStatus != 429 {
		t.Fatalf("http_status = %d, want 429", d.HTTPStatus)
	}
}

// TestListSelfHealsStuckRunning P0 回归：ProxyUpstreamFailed 仅聚合模型触发，
// 普通模型失败无事件收尾，List 必须对超时 running 行批量 self-heal。
func TestListSelfHealsStuckRunning(t *testing.T) {
	reqDB, _ := openRequestLogDB(t.TempDir() + "/request-log.db")
	defer reqDB.Close()
	svc := testService(t, nil)
	svc.reqDB = reqDB

	old := time.Now().Add(-2 * time.Hour).UTC()
	seedRows(t, reqDB,
		[]any{"stuck-l1", "req-s1", "gpt-4o", "c1", 0, 1, old.Format(time.RFC3339Nano), "running", old.Format(time.RFC3339Nano)},
		[]any{"fresh-l2", "req-s2", "gpt-4o", "c1", 0, 0, time.Now().UTC().Format(time.RFC3339Nano), "running", time.Now().UTC().Format(time.RFC3339Nano)},
	)
	page, err := svc.List(context.Background(), requestLogFilter{})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]string{}
	for _, it := range page.Items {
		byID[it.ID] = it.Result
	}
	if byID["stuck-l1"] != "stream_interrupted" {
		t.Fatalf("stuck row result = %q, want stream_interrupted (List self-heal)", byID["stuck-l1"])
	}
	// 未超时的 running 行不动
	if byID["fresh-l2"] != "running" {
		t.Fatalf("fresh row result = %q, want running (untouched)", byID["fresh-l2"])
	}
}

// TestHealStuckCarriesErrorBody P0 补充：普通模型失败无事件，self-heal 时必须把
// route_requests.error_body 还原进 response_json（否则错误详情永久丢失）。
func TestHealStuckCarriesErrorBody(t *testing.T) {
	loadout, err := db.Open(t.TempDir() + "/loadout.db")
	if err != nil {
		t.Fatal(err)
	}
	defer loadout.Close()
	reqDB, _ := openRequestLogDB(t.TempDir() + "/request-log.db")
	defer reqDB.Close()
	svc := testService(t, nil)
	svc.reqDB = reqDB
	svc.loadout = loadout

	old := time.Now().Add(-2 * time.Hour).UTC()
	seedRows(t, reqDB,
		[]any{"stuck-e1", "req-err", "gpt-4o", "c1", 0, 0, old.Format(time.RFC3339Nano), "running", old.Format(time.RFC3339Nano)},
	)
	if _, err := loadout.Exec(`INSERT INTO route_requests(request_id, requested_model, started_at, result, http_status, error_body) VALUES ('req-err', 'gpt-4o', ?, 'failed', 500, '{"error":{"message":"boom"}}')`, old.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	d, err := svc.Detail(context.Background(), "stuck-e1")
	if err != nil {
		t.Fatal(err)
	}
	if d.Result != "failed" {
		t.Fatalf("result = %q, want failed", d.Result)
	}
	if !strings.Contains(string(d.ResponseJSON), "boom") {
		t.Fatalf("response_json missing error body: %s", d.ResponseJSON)
	}
}

// TestHandleStreamChunkRedacts P1 回归：流式 chunk 也必须脱敏（sk- 密钥打码）。
func TestHandleStreamChunkRedacts(t *testing.T) {
	reqDB, _ := openRequestLogDB(t.TempDir() + "/request-log.db")
	defer reqDB.Close()
	svc := testService(t, []types.CapabilityRoute{
		{Models: []string{"*"}, Capability: capabilityName, Route: types.RouteProxy},
	})
	svc.reqDB = reqDB

	pipe := testPipe("req-sr")
	pipe.Request.Stream = true
	if _, err := svc.HandleBeforeAttempt(pipe); err != nil {
		t.Fatal(err)
	}
	uuid, _ := pipe.Metadata[metadataKey].(string)

	if _, err := svc.HandleStreamChunk(&modelgateway.StreamChunkPayload{Pipe: pipe, Data: []byte("data: {\"content\":\"key: sk-abc123xyz\"}\n\n")}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.HandleStreamChunk(&modelgateway.StreamChunkPayload{Pipe: pipe, Data: []byte("data: [DONE]\n\n")}); err != nil {
		t.Fatal(err)
	}
	var respJSON string
	if err := reqDB.QueryRow(`SELECT response_json FROM request_logs WHERE id = ?`, uuid).Scan(&respJSON); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(respJSON, "sk-abc123xyz") {
		t.Fatal("stream body contains raw sk- secret")
	}
	if !strings.Contains(respJSON, "sk-***") {
		t.Fatalf("stream body missing redaction: %s", respJSON)
	}
}

// TestHandleStreamChunkTruncates P1 回归：大流式缓冲触顶截断并标记 truncated。
func TestHandleStreamChunkTruncates(t *testing.T) {
	reqDB, _ := openRequestLogDB(t.TempDir() + "/request-log.db")
	defer reqDB.Close()
	svc := testService(t, []types.CapabilityRoute{
		{Models: []string{"*"}, Capability: capabilityName, Route: types.RouteProxy},
	})
	svc.reqDB = reqDB

	pipe := testPipe("req-st")
	pipe.Request.Stream = true
	if _, err := svc.HandleBeforeAttempt(pipe); err != nil {
		t.Fatal(err)
	}
	uuid, _ := pipe.Metadata[metadataKey].(string)

	// 收紧上限到 1KB，用两段大 chunk 触发截断
	maxStreamBuffer = 1024
	defer func() { maxStreamBuffer = 32 << 20 }()
	big := strings.Repeat("data: {\"x\":\"a\"}\n\n", 200) // ~4KB
	if _, err := svc.HandleStreamChunk(&modelgateway.StreamChunkPayload{Pipe: pipe, Data: []byte(big)}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.HandleStreamChunk(&modelgateway.StreamChunkPayload{Pipe: pipe, Data: []byte("data: [DONE]\n\n")}); err != nil {
		t.Fatal(err)
	}
	var respJSON string
	if err := reqDB.QueryRow(`SELECT response_json FROM request_logs WHERE id = ?`, uuid).Scan(&respJSON); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(respJSON, `"truncated":true`) {
		t.Fatalf("response_json missing truncated flag: %s", respJSON)
	}
}
