package requestlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"loadout/core/db"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
	routetest "loadout/testkit/routetest"
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

// scopeFor 构造单渠道 scope（复用共享 routetest.ScopeWithChannelID）。
func scopeFor(svc *Service, channelID string) types.ChannelRequestScope {
	return routetest.ScopeWithChannelID(channelID, svc.requestChannelBaseURLs(channelID))
}

func TestDecideRouteMiss(t *testing.T) {
	svc := testService(t, nil)
	route, err := routetest.FirstRoute(svc.DecideRoutesScope("gpt-4o", scopeFor(svc, "")))
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
	route, err := routetest.FirstRoute(svc.DecideRoutesScope("gpt-4o", scopeFor(svc, "")))
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
	route, err := routetest.FirstRoute(svc.DecideRoutesScope("gpt-4o", scopeFor(svc, "")))
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
	route, err := routetest.FirstRoute(svc.DecideRoutesScope("gpt-4o", scopeFor(svc, "c1")))
	if err != nil {
		t.Fatal(err)
	}
	if route == nil {
		t.Fatal("channel c1 should match")
	}
	// 未命中其他渠道
	route, err = routetest.FirstRoute(svc.DecideRoutesScope("gpt-4o", scopeFor(svc, "c2")))
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
	route, err := routetest.FirstRoute(svc.DecideRoutesScope("gpt-4o", scopeFor(svc, "c2")))
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
	// 回归：单值 header 应序列化为字符串，而非 []string
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(reqJSON), &raw); err != nil {
		t.Fatal(err)
	}
	headersJSON, ok := raw["headers"]
	if !ok {
		t.Fatalf("headers field missing in %s", reqJSON)
	}
	var rawHeaders map[string]json.RawMessage
	if err := json.Unmarshal(headersJSON, &rawHeaders); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(rawHeaders["Content-Type"])) != `"application/json"` {
		t.Fatalf("Content-Type header serialized as %s, want string", rawHeaders["Content-Type"])
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

// TestHandleBeforeAttemptPerAttemptLogs 回归：同 pipe 多次触发（failover）必须
// 每次生成新 UUID 写新行（per-attempt 独立日志），并覆写 metadata 两个 key——
// metadataKey（收尾事件反查）与 MetadataRequestLogAttemptID（model-gateway 关联
// route_attempts 行）。
func TestHandleBeforeAttemptPerAttemptLogs(t *testing.T) {
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
	firstID, _ := pipe.Metadata[metadataKey].(string)
	firstAttemptID, _ := pipe.Metadata[attemptMetadataKey].(string)
	if firstID == "" || firstAttemptID == "" {
		t.Fatalf("first attempt: metadata keys missing, id=%q attempt_id=%q", firstID, firstAttemptID)
	}
	if firstID != firstAttemptID {
		t.Fatalf("first attempt: metadataKey=%q != attemptMetadataKey=%q", firstID, firstAttemptID)
	}
	// failover 同一 pipe 再次触发：新 UUID、新行
	if _, err := svc.HandleBeforeAttempt(pipe); err != nil {
		t.Fatal(err)
	}
	secondID, _ := pipe.Metadata[metadataKey].(string)
	secondAttemptID, _ := pipe.Metadata[attemptMetadataKey].(string)
	if secondID == "" || secondID == firstID {
		t.Fatalf("second attempt must get new id, first=%q second=%q", firstID, secondID)
	}
	if secondID != secondAttemptID {
		t.Fatalf("second attempt: metadataKey=%q != attemptMetadataKey=%q", secondID, secondAttemptID)
	}
	var count int
	if err := reqDB.QueryRow(`SELECT count(*) FROM request_logs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("rows = %d, want 2 (per-attempt logs)", count)
	}
}

// TestHandleBeforeAttemptNewLogOnRetry 客户端重试（新 pipe 同 X-Request-Id）：
// per-attempt 语义下每次渠道尝试独立日志 → 重试产生新 UUID、新行，不复用旧 UUID。
func TestHandleBeforeAttemptNewLogOnRetry(t *testing.T) {
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

	// 客户端重试：新 pipe（新 metadata），同 request_id → 新 UUID、新行
	retry := testPipe("req-3")
	if _, err := svc.HandleBeforeAttempt(retry); err != nil {
		t.Fatal(err)
	}
	uuid2, _ := retry.Metadata[metadataKey].(string)
	if uuid1 == "" || uuid2 == "" || uuid1 == uuid2 {
		t.Fatalf("retry must produce new uuid: %q vs %q", uuid1, uuid2)
	}
	var count int
	if err := reqDB.QueryRow(`SELECT count(*) FROM request_logs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("rows = %d, want 2 (per-attempt: retry = new log)", count)
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

// TestHandleBeforeAttemptVirtualModelMatchesPhysical 回归：聚合模型（虚拟名）内部
// 切换到真实模型后，能力路由必须按「当前 attempt 的真实模型」匹配（与 sensitive-filter
// 对齐），不能被 __virtual_model 一票否决。配置只含真实模型 hy3 时必须命中记录。
func TestHandleBeforeAttemptVirtualModelMatchesPhysical(t *testing.T) {
	reqDB, _ := openRequestLogDB(t.TempDir() + "/request-log.db")
	defer reqDB.Close()
	svc := testService(t, []types.CapabilityRoute{
		{Models: []string{"hy3"}, Capability: capabilityName, Route: types.RouteProxy},
	})
	svc.reqDB = reqDB

	pipe := testPipe("req-virtual")
	pipe.Request.Model = "hy3"                           // 聚合已改写为真实模型
	pipe.Metadata["__virtual_model"] = "volcengine_auto" // 虚拟名仅保留在 metadata
	if _, err := svc.HandleBeforeAttempt(pipe); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := reqDB.QueryRow(`SELECT count(*) FROM request_logs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("rows = %d, want 1 (virtual model must not override physical match)", count)
	}
	var model string
	if err := reqDB.QueryRow(`SELECT model FROM request_logs LIMIT 1`).Scan(&model); err != nil {
		t.Fatal(err)
	}
	if model != "hy3" {
		t.Fatalf("model = %q, want hy3 (log records real model)", model)
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

// TestHealStuckFetchesAttemptErrorBody 回归：self-heal 必须按 UUID 反查
// route_attempts 拿到对应 attempt 的 error_body/status_code（per-attempt 错误信息在
// route_attempts 表，不在 route_requests）。原反查只看 route_requests，外层 success 时
// 把失败 attempt 误标 stream_interrupted、response_json 为空——丢失真正的上游错误。
func TestHealStuckFetchesAttemptErrorBody(t *testing.T) {
	loadout, err := db.Open(t.TempDir() + "/loadout.db")
	if err != nil {
		t.Fatal(err)
	}
	defer loadout.Close()
	// 外层 result=success（最后一次 attempt 成功），但 step 1 是 429 失败——必须按
	// request_log_id 反查 attempt 行才能拿到 error_body。
	if _, err := loadout.Exec(`INSERT INTO route_requests(request_id, requested_model, started_at, finished_at, result, final_model) VALUES ('r-heal', 'auto', '2026-08-23T00:00:00Z', '2026-08-23T00:00:02Z', 'success', 'hy3')`); err != nil {
		t.Fatal(err)
	}
	const errBody = `{"error":{"data":{"code":14018,"msg":"额度已用尽"}}}`
	if _, err := loadout.Exec(`INSERT INTO route_attempts(request_id, step_no, action, model, channel_id, started_at, finished_at, result, status_code, error_message, error_body, request_log_id) VALUES ('r-heal', '1', '首次尝试', 'hy3', 'c1', '2026-08-23T00:00:00Z', '2026-08-23T00:00:01Z', 'failed', 429, '上游返回错误(429)', ?, 'uuid-failed-1')`, errBody); err != nil {
		t.Fatal(err)
	}

	reqDB, _ := openRequestLogDB(t.TempDir() + "/request-log.db")
	defer reqDB.Close()
	svc := testService(t, nil)
	svc.reqDB = reqDB
	svc.loadout = loadout

	// 种子 running 行（started_at 设为很久以前，超出 60s self-heal 阈值）
	started, _ := time.Parse(time.RFC3339Nano, "2026-08-23T00:00:00Z")
	if _, err := reqDB.Exec(`INSERT INTO request_logs(id, request_id, model, channel, stream, started_at, result, request_json, created_at) VALUES ('uuid-failed-1', 'r-heal', 'hy3', 'c1', 0, '2026-08-23T00:00:00Z', 'running', '{}', '2026-08-23T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	svc.healStuck("uuid-failed-1", "r-heal", started)

	var result, resp string
	var status int
	if err := reqDB.QueryRow(`SELECT result, COALESCE(http_status, 0), COALESCE(response_json, '') FROM request_logs WHERE id = ?`, "uuid-failed-1").Scan(&result, &status, &resp); err != nil {
		t.Fatal(err)
	}
	if result != "failed" {
		t.Fatalf("result = %q, want failed (must NOT mislabel as stream_interrupted)", result)
	}
	if status != 429 {
		t.Fatalf("http_status = %d, want 429", status)
	}
	if !strings.Contains(resp, "14018") || !strings.Contains(resp, "额度已用尽") {
		t.Fatalf("response_json missing attempt error body: %s", resp)
	}
}

// TestHealStuckSuccessAttemptNotMislabeled P0 反向回归：attempt 行 result=success
// （流式断开无 [DONE] 但 attempt 侧已 success）时，healStuck 反查不得把它标 failed——
// 反查必须限定 result='failed'，否则 200 成功尝试被误标 failed(200) 且丢 body。
func TestHealStuckSuccessAttemptNotMislabeled(t *testing.T) {
	loadout, err := db.Open(t.TempDir() + "/loadout.db")
	if err != nil {
		t.Fatal(err)
	}
	defer loadout.Close()
	if _, err := loadout.Exec(`INSERT INTO route_requests(request_id, requested_model, started_at, finished_at, result, final_model) VALUES ('r-heal-ok', 'auto', '2026-08-23T00:00:00Z', '2026-08-23T00:00:02Z', 'success', 'hy3')`); err != nil {
		t.Fatal(err)
	}
	// attempt 是 success+200（客户端断开但上游正常返回过）——不能被标 failed
	if _, err := loadout.Exec(`INSERT INTO route_attempts(request_id, step_no, action, model, channel_id, started_at, finished_at, result, status_code, error_body, request_log_id) VALUES ('r-heal-ok', '1', '首次尝试', 'hy3', 'c1', '2026-08-23T00:00:00Z', '2026-08-23T00:00:01Z', 'success', 200, '', 'uuid-ok-1')`); err != nil {
		t.Fatal(err)
	}

	reqDB, _ := openRequestLogDB(t.TempDir() + "/request-log.db")
	defer reqDB.Close()
	svc := testService(t, nil)
	svc.reqDB = reqDB
	svc.loadout = loadout

	started, _ := time.Parse(time.RFC3339Nano, "2026-08-23T00:00:00Z")
	if _, err := reqDB.Exec(`INSERT INTO request_logs(id, request_id, model, channel, stream, started_at, result, request_json, created_at) VALUES ('uuid-ok-1', 'r-heal-ok', 'hy3', 'c1', 1, '2026-08-23T00:00:00Z', 'running', '{}', '2026-08-23T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	svc.healStuck("uuid-ok-1", "r-heal-ok", started)

	var result string
	if err := reqDB.QueryRow(`SELECT result FROM request_logs WHERE id = ?`, "uuid-ok-1").Scan(&result); err != nil {
		t.Fatal(err)
	}
	if result == "failed" {
		t.Fatalf("result = failed, want NOT failed (success attempt must not be mislabeled)")
	}
}

// TestBeforeAttemptMissClearsAttemptKeys P1 回归：未命中能力路由的 attempt 必须
// 清掉关联 key 并打哨兵，防止残留上一 attempt 的 UUID 串号（route_attempts.
// request_log_id 错指 + 收尾覆盖）。
func TestBeforeAttemptMissClearsAttemptKeys(t *testing.T) {
	reqDB, _ := openRequestLogDB(t.TempDir() + "/request-log.db")
	defer reqDB.Close()
	svc := testService(t, []types.CapabilityRoute{
		{Models: []string{"hy3"}, Capability: capabilityName, Route: types.RouteProxy},
	})
	svc.reqDB = reqDB

	// attempt 1：hy3 命中路由 → UUID 写入
	pipe := testPipe("req-miss")
	pipe.Request.Model = "hy3"
	if _, err := svc.HandleBeforeAttempt(pipe); err != nil {
		t.Fatal(err)
	}
	if _, ok := pipe.Metadata[attemptMetadataKey].(string); !ok {
		t.Fatal("attempt 1: metadata key missing")
	}
	// attempt 2：换模型 gpt-4o 未命中 → key 必须清空 + 哨兵
	pipe.Request.Model = "gpt-4o"
	if _, err := svc.HandleBeforeAttempt(pipe); err != nil {
		t.Fatal(err)
	}
	if _, ok := pipe.Metadata[attemptMetadataKey]; ok {
		t.Fatal("attempt 2 miss: attemptMetadataKey must be cleared")
	}
	if _, ok := pipe.Metadata[metadataKey]; ok {
		t.Fatal("attempt 2 miss: metadataKey must be cleared")
	}
	if skipped, _ := pipe.Metadata[skippedKey].(bool); !skipped {
		t.Fatal("attempt 2 miss: skipped sentinel must be set")
	}
	// 收尾事件（pipeRequestLogID）在哨兵下必须返回空，不反查旧行
	if id := svc.pipeRequestLogID(pipe); id != "" {
		t.Fatalf("pipeRequestLogID = %q, want empty under skipped sentinel", id)
	}
}

// ---- 保留策略 / 统计 / 清空 / 存在性 ----

// seedLogRow 直接插一行日志（绕过 handler，便于精确控制 started_at 与体积）。
func seedLogRow(t *testing.T, reqDB *sql.DB, id, startedAt string, bodySize int) {
	t.Helper()
	body := strings.Repeat("x", bodySize)
	// bytes 与生产写入路径保持一致（rowBytes），容量清理按这一列求和。
	if _, err := reqDB.Exec(`INSERT INTO request_logs(id, request_id, model, channel, stream, started_at, result, request_json, bytes, created_at) VALUES (?, ?, 'm', 'c', 0, ?, 'success', ?, ?, ?)`,
		id, id, startedAt, body, rowBytes(body, ""), startedAt); err != nil {
		t.Fatal(err)
	}
}

// TestApplyRetentionByAge 按天数清理：只删早于阈值的行，最近的行必须留下。
func TestApplyRetentionByAge(t *testing.T) {
	reqDB, err := openRequestLogDB(t.TempDir() + "/request-log.db")
	if err != nil {
		t.Fatal(err)
	}
	defer reqDB.Close()
	svc := NewService(nil, slog.New(slog.DiscardHandler), reqDB, nil)

	now := time.Now().UTC()
	seedLogRow(t, reqDB, "old", now.AddDate(0, 0, -30).Format(time.RFC3339Nano), 10)
	seedLogRow(t, reqDB, "recent", now.AddDate(0, 0, -1).Format(time.RFC3339Nano), 10)

	svc.ApplyRetention(context.Background(), RetentionConfig{MaxAgeDays: 7})

	var count int
	if err := reqDB.QueryRow(`SELECT count(*) FROM request_logs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("rows after age cleanup = %d, want 1", count)
	}
	var id string
	if err := reqDB.QueryRow(`SELECT id FROM request_logs`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id != "recent" {
		t.Fatalf("kept row = %q, want recent", id)
	}
}

// TestApplyRetentionNoLimitIsNoop 两个阈值都为 0 时不得删任何东西（默认行为不变）。
func TestApplyRetentionNoLimitIsNoop(t *testing.T) {
	reqDB, err := openRequestLogDB(t.TempDir() + "/request-log.db")
	if err != nil {
		t.Fatal(err)
	}
	defer reqDB.Close()
	svc := NewService(nil, slog.New(slog.DiscardHandler), reqDB, nil)

	now := time.Now().UTC()
	for _, id := range []string{"a", "b", "c"} {
		seedLogRow(t, reqDB, id, now.Format(time.RFC3339Nano), 10)
	}
	svc.ApplyRetention(context.Background(), RetentionConfig{})

	var count int
	if err := reqDB.QueryRow(`SELECT count(*) FROM request_logs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("rows = %d, want 3 (no limit must not delete)", count)
	}
}

// TestClearRemovesRowsAndResetsSize 清空后行数为 0，且文件大小回落
// （验证 wal_checkpoint 确实让磁盘占用跟着掉，否则用户点完清空会以为没生效）。
func TestClearRemovesRowsAndResetsSize(t *testing.T) {
	path := t.TempDir() + "/request-log.db"
	reqDB, err := openRequestLogDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reqDB.Close()
	svc := NewService(nil, slog.New(slog.DiscardHandler), reqDB, nil)
	svc.SetDBPath(path)

	now := time.Now().UTC()
	for i := 0; i < 20; i++ {
		seedLogRow(t, reqDB, fmt.Sprintf("row-%d", i), now.Format(time.RFC3339Nano), 64*1024)
	}
	// 用 compactedSize 取 before：它先 checkpoint 把 WAL 并回主文件再量，
	// 否则 before 会少算还在 WAL 里的部分、after 反而"变大"（清空后 VACUUM 把
	// 数据从 WAL 搬进主文件，两个文件加起来才是真实占用）。
	before := svc.compactedSize(context.Background())
	if before <= 0 {
		t.Fatalf("diskSize before clear = %d, want > 0", before)
	}

	affected, err := svc.Clear(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if affected != 20 {
		t.Fatalf("affected = %d, want 20", affected)
	}
	var count int
	if err := reqDB.QueryRow(`SELECT count(*) FROM request_logs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rows after clear = %d, want 0", count)
	}
	// 必须真正收缩：只删行不 VACUUM 的话文件几乎不变，用户会以为清空没生效。
	if after := svc.diskSize(); after > before/4 {
		t.Fatalf("diskSize after clear = %d, want well under a quarter of %d (VACUUM must reclaim space)", after, before)
	}
}

// TestClearResetsRouteRequestLink 清空后必须把 loadout.db 的关联列一起清掉，
// 否则前端列表会一直显示指向已删日志的「进入日志」入口。
func TestClearResetsRouteRequestLink(t *testing.T) {
	dir := t.TempDir()
	loadoutDB, err := sql.Open("sqlite", dir+"/loadout.db")
	if err != nil {
		t.Fatal(err)
	}
	defer loadoutDB.Close()
	if _, err := loadoutDB.Exec(`CREATE TABLE route_requests (request_id TEXT PRIMARY KEY, request_log_id TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := loadoutDB.Exec(`INSERT INTO route_requests(request_id, request_log_id) VALUES ('r1', 'uuid-1'), ('r2', NULL)`); err != nil {
		t.Fatal(err)
	}

	reqDB, err := openRequestLogDB(dir + "/request-log.db")
	if err != nil {
		t.Fatal(err)
	}
	defer reqDB.Close()
	svc := NewService(nil, slog.New(slog.DiscardHandler), reqDB, loadoutDB)
	seedLogRow(t, reqDB, "uuid-1", time.Now().UTC().Format(time.RFC3339Nano), 10)

	if _, err := svc.Clear(context.Background()); err != nil {
		t.Fatal(err)
	}
	var link sql.NullString
	if err := loadoutDB.QueryRow(`SELECT request_log_id FROM route_requests WHERE request_id = 'r1'`).Scan(&link); err != nil {
		t.Fatal(err)
	}
	if link.Valid && link.String != "" {
		t.Fatalf("request_log_id = %q, want empty after clear", link.String)
	}
}

// TestExistingIDs 存在性查询：只回仍然在库里的 id，且能跨过 500 个一批的分段边界。
func TestExistingIDs(t *testing.T) {
	reqDB, err := openRequestLogDB(t.TempDir() + "/request-log.db")
	if err != nil {
		t.Fatal(err)
	}
	defer reqDB.Close()
	svc := NewService(nil, slog.New(slog.DiscardHandler), reqDB, nil)

	now := time.Now().UTC().Format(time.RFC3339Nano)
	seedLogRow(t, reqDB, "alive-1", now, 10)
	seedLogRow(t, reqDB, "alive-2", now, 10)

	// 候选里混入 600 个不存在的 id，跨过 500 的分段边界，最后再放两个真实存在的
	candidates := []string{"gone-0"}
	for i := 0; i < 600; i++ {
		candidates = append(candidates, fmt.Sprintf("gone-%d", i+1))
	}
	candidates = append(candidates, "alive-1", "alive-2")

	found, err := svc.ExistingIDs(context.Background(), candidates)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 || !found["alive-1"] || !found["alive-2"] {
		t.Fatalf("found = %v, want exactly alive-1/alive-2", found)
	}
}

// TestStatsDiskSizeAndConfig Stats 要报出文件占用与行数。
func TestStatsDiskSizeAndConfig(t *testing.T) {
	// repo 用 nil：Stats 仍应给出行数/大小，保留配置回落 0。
	path := t.TempDir() + "/request-log.db"
	reqDB, err := openRequestLogDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reqDB.Close()
	svc := NewService(nil, slog.New(slog.DiscardHandler), reqDB, nil)
	svc.SetDBPath(path)

	now := time.Now().UTC()
	seedLogRow(t, reqDB, "one", now.Add(-time.Hour).Format(time.RFC3339Nano), 4096)
	seedLogRow(t, reqDB, "two", now.Format(time.RFC3339Nano), 4096)

	stats := svc.Stats(context.Background())
	if stats.Count != 2 {
		t.Fatalf("count = %d, want 2", stats.Count)
	}
	if stats.Size <= 0 {
		t.Fatalf("size = %d, want > 0", stats.Size)
	}
	if stats.OldestStartedAt == "" || stats.NewestStartedAt == "" {
		t.Fatalf("time range missing: oldest=%q newest=%q", stats.OldestStartedAt, stats.NewestStartedAt)
	}
	if stats.MaxAgeDays != 0 || stats.MaxSizeMB != 0 {
		t.Fatalf("config without repo = %d/%d, want 0/0", stats.MaxAgeDays, stats.MaxSizeMB)
	}
}

// TestTrimBySizeKeepsNewest 容量清理必须是 FIFO：删最旧的，保留最新的。
func TestTrimBySizeKeepsNewest(t *testing.T) {
	path := t.TempDir() + "/request-log.db"
	reqDB, err := openRequestLogDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reqDB.Close()
	svc := NewService(nil, slog.New(slog.DiscardHandler), reqDB, nil)
	svc.SetDBPath(path)

	base := time.Now().UTC()
	// 200 行、每行 32KB ≈ 6.4MB；阈值设 1MB 必然触发清理。
	for i := 0; i < 200; i++ {
		seedLogRow(t, reqDB, fmt.Sprintf("row-%03d", i), base.Add(time.Duration(i)*time.Minute).Format(time.RFC3339Nano), 32*1024)
	}
	before := svc.diskSize()

	svc.ApplyRetention(context.Background(), RetentionConfig{MaxSizeMB: 1})

	after := svc.diskSize()
	if after >= before {
		t.Fatalf("size after trim = %d, want < %d", after, before)
	}
	// FIFO：剩下的必须是最新的那批（row-199 一定还在，row-000 一定被删）。
	var newestExists, oldestExists int
	if err := reqDB.QueryRow(`SELECT count(*) FROM request_logs WHERE id = 'row-199'`).Scan(&newestExists); err != nil {
		t.Fatal(err)
	}
	if err := reqDB.QueryRow(`SELECT count(*) FROM request_logs WHERE id = 'row-000'`).Scan(&oldestExists); err != nil {
		t.Fatal(err)
	}
	if newestExists != 1 {
		t.Fatal("newest row must survive FIFO trim")
	}
	if oldestExists != 0 {
		t.Fatal("oldest row must be deleted first by FIFO trim")
	}
}

// TestTrimBySizeRespectsMinKeep 阈值小到不可能达成时，也不能把库删空——
// 至少保留 retentionMinKeep 条（防用户设 1MB 反而丢掉全部日志）。
func TestTrimBySizeRespectsMinKeep(t *testing.T) {
	path := t.TempDir() + "/request-log.db"
	reqDB, err := openRequestLogDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reqDB.Close()
	svc := NewService(nil, slog.New(slog.DiscardHandler), reqDB, nil)
	svc.SetDBPath(path)

	now := time.Now().UTC()
	// 只放 10 行（远少于下限），阈值设 1MB
	for i := 0; i < 10; i++ {
		seedLogRow(t, reqDB, fmt.Sprintf("row-%d", i), now.Add(time.Duration(i)*time.Minute).Format(time.RFC3339Nano), 64*1024)
	}
	svc.ApplyRetention(context.Background(), RetentionConfig{MaxSizeMB: 1})

	var count int
	if err := reqDB.QueryRow(`SELECT count(*) FROM request_logs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 10 {
		t.Fatalf("rows = %d, want 10 (below min-keep floor, must not delete)", count)
	}
}

// TestTrimBySizeDrainsLargeDatabase 回归：大库必须真的一路删到阈值以下。
//
// 历史 bug：早期实现限制「最多 5 轮、每轮最多 200 行」，15GB 的库（两万多条）
// 最多删 1000 行就停，用户把上限从「不限」改成 1000MB 后大小毫无变化。
// 本测试造一个远超阈值的库，断言清理后确实降到了上限以下。
func TestTrimBySizeDrainsLargeDatabase(t *testing.T) {
	path := t.TempDir() + "/request-log.db"
	reqDB, err := openRequestLogDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reqDB.Close()
	svc := NewService(nil, slog.New(slog.DiscardHandler), reqDB, nil)
	svc.SetDBPath(path)

	base := time.Now().UTC()
	// 2000 行 × 16KB ≈ 32MB。阈值 2MB → 必须删掉绝大多数行才可能达标，
	// 远远超过旧的「5 轮 × 200 行 = 1000 行」上限能删的量。
	for i := 0; i < 2000; i++ {
		seedLogRow(t, reqDB, fmt.Sprintf("d%04d", i), base.Add(time.Duration(i)*time.Second).Format(time.RFC3339Nano), 16*1024)
	}
	before := svc.diskSize()
	if before < 8*1024*1024 {
		t.Fatalf("seeded size = %d, want tens of MB for a meaningful test", before)
	}

	svc.ApplyRetention(context.Background(), RetentionConfig{MaxSizeMB: 2})

	after := svc.compactedSize(context.Background())
	limit := int64(2 * 1024 * 1024)
	if after > limit {
		t.Fatalf("size after trim = %d, want <= %d (large DB must drain to the cap)", after, limit)
	}

	// 仍然必须是 FIFO：最新的留下，最旧的先走。
	var newestKept, oldestKept int
	if err := reqDB.QueryRow(`SELECT count(*) FROM request_logs WHERE id = 'd1999'`).Scan(&newestKept); err != nil {
		t.Fatal(err)
	}
	if err := reqDB.QueryRow(`SELECT count(*) FROM request_logs WHERE id = 'd0000'`).Scan(&oldestKept); err != nil {
		t.Fatal(err)
	}
	if newestKept != 1 {
		t.Fatal("newest row must survive")
	}
	if oldestKept != 0 {
		t.Fatal("oldest row must be deleted first")
	}

	// 下限保护：不能把库清空。删了这么多行之后仍应远多于... 实际上这里会删到下限，
	// 因为 2MB 装不下 100 条 16KB 的行；断言至少保留下限条数。
	var remaining int
	if err := reqDB.QueryRow(`SELECT count(*) FROM request_logs`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining < retentionMinKeep {
		t.Fatalf("remaining = %d, want >= %d (never drain the DB completely)", remaining, retentionMinKeep)
	}
}

// TestTrimBySizeBelowCapIsNoop 未超阈值时不得删任何行。
func TestTrimBySizeBelowCapIsNoop(t *testing.T) {
	path := t.TempDir() + "/request-log.db"
	reqDB, err := openRequestLogDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reqDB.Close()
	svc := NewService(nil, slog.New(slog.DiscardHandler), reqDB, nil)
	svc.SetDBPath(path)

	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		seedLogRow(t, reqDB, fmt.Sprintf("k%d", i), now.Format(time.RFC3339Nano), 1024)
	}
	svc.ApplyRetention(context.Background(), RetentionConfig{MaxSizeMB: 100})

	var count int
	if err := reqDB.QueryRow(`SELECT count(*) FROM request_logs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 5 {
		t.Fatalf("rows = %d, want 5 (under cap must not delete)", count)
	}
}

// TestApplyCurrentRetentionUsesSettings 手动触发接口按「当前设置」清理。
// 这是用户改完上限后点「立即清理」走的路：必须真的把超限的旧日志删掉。
func TestApplyCurrentRetentionUsesSettings(t *testing.T) {
	dir := t.TempDir()
	// 只开一次连接：db.Open 会建好 loadout 的全部表（servercore 装配时同样只用一份连接）。
	loadoutDB, err := db.Open(dir + "/loadout.db")
	if err != nil {
		t.Fatal(err)
	}
	defer loadoutDB.Close()
	if _, err := loadoutDB.Exec(`INSERT INTO settings(id, request_log_max_age_days, request_log_max_size_mb) VALUES(1, 0, 1) ON CONFLICT(id) DO UPDATE SET request_log_max_size_mb = 1`); err != nil {
		t.Fatal(err)
	}

	path := dir + "/request-log.db"
	reqDB, err := openRequestLogDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reqDB.Close()
	svc := NewService(nil, slog.New(slog.DiscardHandler), reqDB, nil)
	svc.SetDBPath(path)
	repo, err := db.NewRepository(loadoutDB)
	if err != nil {
		t.Fatal(err)
	}
	svc.SetRepository(repo)

	base := time.Now().UTC()
	for i := 0; i < 400; i++ {
		seedLogRow(t, reqDB, fmt.Sprintf("m%03d", i), base.Add(time.Duration(i)*time.Second).Format(time.RFC3339Nano), 16*1024)
	}
	before := svc.diskSize()

	stats := svc.ApplyCurrentRetention(context.Background())

	if stats.Size >= before {
		t.Fatalf("size after manual apply = %d, want < %d", stats.Size, before)
	}
	if stats.Count >= 400 {
		t.Fatalf("count after manual apply = %d, want < 400", stats.Count)
	}
	// 回带的统计必须与设置一致，前端直接拿它刷新卡片。
	if stats.MaxSizeMB != 1 {
		t.Fatalf("stats.max_size_mb = %d, want 1", stats.MaxSizeMB)
	}
}

// TestTrimBySizeDoesNotOverDelete 回归用户实际场景：日志库「内容多但文件不算大」时
// 不得过度删除。
//
// 历史 bug：旧实现拿「文件大小」当删除进度。SQLite 删行不会让文件变小，于是每次
// 检查都显示「还超限」，一路删到 100 条保留下限——实测 301 行只有 19MB、上限
// 20MB 的场景被删到只剩 100 行，用户丢掉了本可以保留的日志。
// 正确行为：只删到「真实文件大小落到上限内」为止，剩下的行必须留下。
//
// ⚠️ 数据量必须真的超过上限，否则本测试没有意义。这里踩过一次坑：原先写 300 行 × 64KB，
// 备注按「300 × 64KB ≈ 23MB」推算以为超了 20MB 上限，但实际文件只有 ~19.9MB——本来就
// 在限内，清理逻辑正确地什么都不删，而后面的 `after < before` 却断定「必须变小」，
// 于是测试长期失败。真正超限需要 ~340 行以上（实测 340 行 ≈ 22.5MB）。这里用 400 行
// 留出余量，避免 SQLite 页头/对齐带来的估算偏差又把它压回限内。
func TestTrimBySizeDoesNotOverDelete(t *testing.T) {
	path := t.TempDir() + "/request-log.db"
	reqDB, err := openRequestLogDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reqDB.Close()
	svc := NewService(nil, slog.New(slog.DiscardHandler), reqDB, nil)
	svc.SetDBPath(path)

	base := time.Now().UTC()
	// 400 行 × 64KB ≈ 26.5MB 磁盘占用（实测）。上限设 20MB：只超出一点点，
	// 压缩 + 删掉少量最旧的行就该达标，绝不能一路删到 100 条下限。
	const rows = 400
	for i := 0; i < rows; i++ {
		seedLogRow(t, reqDB, fmt.Sprintf("s%03d", i), base.Add(time.Duration(i)*time.Second).Format(time.RFC3339Nano), 64*1024)
	}
	before := svc.diskSize()

	// 前置条件守卫：本测试的全部意义都建立在「清理前确实超限」上。若数据量涨不上去
	// （阈值被调小、种子逻辑变化），后面的「必须收缩」会变成假失败，这里直接点破原因。
	const limitBytes = 20 * 1024 * 1024
	if before <= limitBytes {
		t.Fatalf("测试前提不成立：清理前 %d 字节未超过上限 %d，本测试需要「确实超限」的库", before, limitBytes)
	}

	svc.ApplyRetention(context.Background(), RetentionConfig{MaxSizeMB: 20})

	after := svc.compactedSize(context.Background())
	if after > limitBytes {
		t.Fatalf("size after trim = %d, want <= %d", after, limitBytes)
	}
	if after >= before {
		t.Fatalf("size after trim = %d, want < %d (must actually shrink)", after, before)
	}
	// 关键断言：不该删到保留下限。数据量只有 20MB 出头，远不到需要砍到 100 行的地步。
	var remaining int
	if err := reqDB.QueryRow(`SELECT COUNT(*) FROM request_logs`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	t.Logf("before=%d after=%d remaining=%d", before, after, remaining)
	if remaining < retentionMinKeep {
		t.Fatalf("remaining = %d, want >= %d (never drain the DB completely)", remaining, retentionMinKeep)
	}
	// 400 行里最多只需删掉少数旧行；绝不能删到只剩个位数。
	// 这里给一个宽松上限：留下的必须占绝大多数。
	if remaining < 200 {
		t.Fatalf("remaining = %d, want >= 200 (must not over-delete)", remaining)
	}
}

// TestTrimBySizeDeletesOldestFirst 超限时按 FIFO 从最旧的行删起，最新的必须留下。
func TestTrimBySizeDeletesOldestFirst(t *testing.T) {
	path := t.TempDir() + "/request-log.db"
	reqDB, err := openRequestLogDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reqDB.Close()
	svc := NewService(nil, slog.New(slog.DiscardHandler), reqDB, nil)
	svc.SetDBPath(path)

	base := time.Now().UTC()
	// 400 行 × 64KB ≈ 31MB，上限 8MB → 必须删掉大部分旧行。
	const rows = 400
	for i := 0; i < rows; i++ {
		seedLogRow(t, reqDB, fmt.Sprintf("s%03d", i), base.Add(time.Duration(i)*time.Second).Format(time.RFC3339Nano), 64*1024)
	}

	svc.ApplyRetention(context.Background(), RetentionConfig{MaxSizeMB: 8})

	if after := svc.diskSize(); after > 8*1024*1024 {
		t.Fatalf("size after trim = %d, want <= %d", after, 8*1024*1024)
	}
	var remaining int
	if err := reqDB.QueryRow(`SELECT COUNT(*) FROM request_logs`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining >= rows {
		t.Fatalf("remaining = %d, want < %d (must shrink an over-cap DB)", remaining, rows)
	}
	if remaining < retentionMinKeep {
		t.Fatalf("remaining = %d, want >= %d (never drain the DB completely)", remaining, retentionMinKeep)
	}
	// 最旧的先走、最新的必留。
	var oldest, newest int
	if err := reqDB.QueryRow(`SELECT COUNT(*) FROM request_logs WHERE id = 's000'`).Scan(&oldest); err != nil {
		t.Fatal(err)
	}
	if err := reqDB.QueryRow(`SELECT COUNT(*) FROM request_logs WHERE id = 's399'`).Scan(&newest); err != nil {
		t.Fatal(err)
	}
	if oldest != 0 {
		t.Fatal("oldest row must be deleted first")
	}
	if newest != 1 {
		t.Fatal("newest row must survive")
	}
	// 保留的必须是最新的一段：留下的最旧一行必须比任何被删的行都新。
	var minKept string
	if err := reqDB.QueryRow(`SELECT MIN(started_at) FROM request_logs`).Scan(&minKept); err != nil {
		t.Fatal(err)
	}
	var olderLeft int
	if err := reqDB.QueryRow(`SELECT COUNT(*) FROM request_logs WHERE started_at < ?`, minKept).Scan(&olderLeft); err != nil {
		t.Fatal(err)
	}
	if olderLeft != 0 {
		t.Fatalf("found %d rows older than the oldest kept row — kept set is not a contiguous newest tail", olderLeft)
	}
}

// TestKeepRowsWithinCountsFromNewest 估算「保留最新多少行」必须从最新往旧累加。
//
// 回归：早先这条 SQL 写成 SUM(...) OVER (ORDER BY rn DESC)，rn 是最新为 1 的序号，
// 这样累加是**从最旧往新**，acc 命中的是最旧的几行——算出来的数字含义完全反了，
// 结果每次清理都严重删不到位（5000 行只删 0 行），只能靠后面反复 VACUUM 兜底，
// 300MB 的库要跑 9 秒。
func TestKeepRowsWithinCountsFromNewest(t *testing.T) {
	reqDB, err := openRequestLogDB(t.TempDir() + "/request-log.db")
	if err != nil {
		t.Fatal(err)
	}
	defer reqDB.Close()
	svc := NewService(nil, slog.New(slog.DiscardHandler), reqDB, nil)

	base := time.Now().UTC()
	// 200 行，每行 1KB（bytes 记 1024 + 固定开销）。
	const rows = 200
	for i := 0; i < rows; i++ {
		seedLogRow(t, reqDB, fmt.Sprintf("k%03d", i), base.Add(time.Duration(i)*time.Second).Format(time.RFC3339Nano), 1024)
	}
	perRow := rowBytes(strings.Repeat("x", 1024), "")
	// contentBudget 会先压 95% 再折 ratio，这里反推出「想保留 100 行」对应的 limit：
	// budget = (limit*0.95 - fixed) / ratio = perRow*100  =>  limit = (perRow*100*ratio + fixed) / 0.95
	want := float64(perRow * 100)
	limit := int64((want*sizeRatio + float64(fixedSizeOverhead)) / 0.95)
	keep := svc.keepRowsWithin(context.Background(), limit)

	if keep < 90 || keep > 110 {
		t.Fatalf("keep = %d, want around 100 (must count from the newest, not the oldest)", keep)
	}
	// 保留的必须是最新的一批：最旧那一行不该被算进 keep 里。
	var oldestRank int64
	if err := reqDB.QueryRow(`SELECT rn FROM (SELECT id, ROW_NUMBER() OVER (ORDER BY started_at DESC) AS rn FROM request_logs) WHERE id = 'k000'`).Scan(&oldestRank); err != nil {
		t.Fatal(err)
	}
	if keep >= oldestRank {
		t.Fatalf("keep = %d reached the oldest row (rn=%d): accumulation direction is wrong", keep, oldestRank)
	}
}

// TestTrimBySizeSingleVacuum 回归性能：清理只该 VACUUM 一次（估算准 -> 一次删到位）。
//
// 早先每轮 VACUUM 一次，300MB 的库要十几秒、15GB 就是十几分钟。VACUUM 与库大小
// 成正比，是整个流程唯一的大头，必须只做一次。
func TestTrimBySizeSingleVacuum(t *testing.T) {
	path := t.TempDir() + "/request-log.db"
	reqDB, err := openRequestLogDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reqDB.Close()
	svc := NewService(nil, slog.New(slog.DiscardHandler), reqDB, nil)
	svc.SetDBPath(path)

	base := time.Now().UTC()
	// 600 行 × 16KB ≈ 9.4MB，上限 4MB —— 必须删掉大部分。
	const rows = 600
	for i := 0; i < rows; i++ {
		seedLogRow(t, reqDB, fmt.Sprintf("v%03d", i), base.Add(time.Duration(i)*time.Second).Format(time.RFC3339Nano), 16*1024)
	}

	svc.ApplyRetention(context.Background(), RetentionConfig{MaxSizeMB: 4})

	after := svc.diskSize()
	if after > 4*1024*1024 {
		t.Fatalf("size after trim = %d, want <= %d", after, 4*1024*1024)
	}
	var remaining int
	if err := reqDB.QueryRow(`SELECT COUNT(*) FROM request_logs`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining >= rows {
		t.Fatalf("remaining = %d, want < %d", remaining, rows)
	}
	if remaining < retentionMinKeep {
		t.Fatalf("remaining = %d, want >= %d", remaining, retentionMinKeep)
	}
	// 保留的必须是最新的：最新的留下，最旧的走。
	var newest, oldest int
	_ = reqDB.QueryRow(`SELECT COUNT(*) FROM request_logs WHERE id = 'v599'`).Scan(&newest)
	_ = reqDB.QueryRow(`SELECT COUNT(*) FROM request_logs WHERE id = 'v000'`).Scan(&oldest)
	if newest != 1 {
		t.Fatal("newest row must survive")
	}
	if oldest != 0 {
		t.Fatal("oldest row must be deleted first")
	}
}

// TestFinishRequestLogRecalculatesBytes 收尾写入响应体后，bytes 必须跟着变大。
//
// 这是「写入完成时记录大小」的关键一环：请求落库时只能算到请求体，响应体是
// 收尾时才有的。如果收尾忘了重算 bytes，这行就永远只报半个大小，容量清理
// 会少算它占的空间。
func TestFinishRequestLogRecalculatesBytes(t *testing.T) {
	reqDB, err := openRequestLogDB(t.TempDir() + "/request-log.db")
	if err != nil {
		t.Fatal(err)
	}
	defer reqDB.Close()
	svc := NewService(nil, slog.New(slog.DiscardHandler), reqDB, nil)

	reqBody := strings.Repeat("q", 1000)
	started := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := reqDB.Exec(`INSERT INTO request_logs(id, request_id, model, channel, stream, started_at, result, request_json, bytes, created_at) VALUES ('b1','b1','m','c',0,?,'running',?,?,?)`,
		started, reqBody, rowBytes(reqBody, ""), started); err != nil {
		t.Fatal(err)
	}

	var before int64
	if err := reqDB.QueryRow(`SELECT bytes FROM request_logs WHERE id = 'b1'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if want := rowBytes(reqBody, ""); before != want {
		t.Fatalf("bytes before finish = %d, want %d", before, want)
	}

	respBody := strings.Repeat("r", 5000)
	svc.finishRequestLog("b1", 200, respBody, "ch", "success")

	var after int64
	if err := reqDB.QueryRow(`SELECT bytes FROM request_logs WHERE id = 'b1'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if want := rowBytes(reqBody, respBody); after != want {
		t.Fatalf("bytes after finish = %d, want %d", after, want)
	}
	if after <= before {
		t.Fatalf("bytes should grow after writing the response body: %d -> %d", before, after)
	}
}

// TestOuterLinkFollowsLatestAttempt 回归：外层表格那条「完整日志」入口必须指向最后一次
// 渠道尝试的日志，而不是第一次。
//
// 原实现加上空值条件后只写首次，failover 后外层按钮仍指着
// 早已失败的第 1 条（用户看到的「成功」行点进去却是 429 失败的日志）。每次尝试都会写一条
// 独立日志，最后一条（真正返回给用户的那次）才是这条请求的日志。
func TestOuterLinkFollowsLatestAttempt(t *testing.T) {
	loadout, err := db.Open(t.TempDir() + "/loadout.db")
	if err != nil {
		t.Fatal(err)
	}
	defer loadout.Close()
	if _, err := loadout.Exec(`INSERT INTO route_requests(request_id, requested_model, started_at, result) VALUES ('req-last', 'gpt-4o', '2026-01-01T00:00:00Z', 'running')`); err != nil {
		t.Fatal(err)
	}
	reqDB, _ := openRequestLogDB(t.TempDir() + "/request-log.db")
	defer reqDB.Close()
	svc := testService(t, []types.CapabilityRoute{
		{Models: []string{"*"}, Capability: capabilityName, Route: types.RouteProxy},
	})
	svc.reqDB = reqDB
	svc.loadout = loadout

	// 首次尝试
	pipe := testPipe("req-last")
	if _, err := svc.HandleBeforeAttempt(pipe); err != nil {
		t.Fatal(err)
	}
	firstID, _ := pipe.Metadata[metadataKey].(string)
	if firstID == "" {
		t.Fatal("first attempt uuid missing")
	}
	var afterFirst sql.NullString
	if err := loadout.QueryRow(`SELECT request_log_id FROM route_requests WHERE request_id = 'req-last'`).Scan(&afterFirst); err != nil {
		t.Fatal(err)
	}
	if afterFirst.String != firstID {
		t.Fatalf("after first attempt link = %q, want %q", afterFirst.String, firstID)
	}

	// failover：同一 pipe 第 2 次尝试 = 新日志，外层关联必须跟着走
	if _, err := svc.HandleBeforeAttempt(pipe); err != nil {
		t.Fatal(err)
	}
	lastID, _ := pipe.Metadata[metadataKey].(string)
	if lastID == "" || lastID == firstID {
		t.Fatalf("second attempt must get a new uuid: first=%q second=%q", firstID, lastID)
	}
	var afterSecond sql.NullString
	if err := loadout.QueryRow(`SELECT request_log_id FROM route_requests WHERE request_id = 'req-last'`).Scan(&afterSecond); err != nil {
		t.Fatal(err)
	}
	if afterSecond.String != lastID {
		t.Fatalf("outer link = %q, want latest attempt %q", afterSecond.String, lastID)
	}
}
