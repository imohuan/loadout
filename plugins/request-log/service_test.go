package requestlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
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
	svc := NewService(st, slog.New(slog.DiscardHandler), nil, nil)
	// 测试统一装配块存储（生产装配层同样总是装配）。
	if err := svc.SetBlobDir(filepath.Join(t.TempDir(), "request-logs")); err != nil {
		t.Fatal(err)
	}
	return svc
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

	// 半条：running + 详情组装出的 request_json 脱敏
	var result string
	if err := reqDB.QueryRow(`SELECT result FROM request_logs WHERE id = ?`, uuid).Scan(&result); err != nil {
		t.Fatal(err)
	}
	if result != "running" {
		t.Fatalf("result = %q, want running", result)
	}
	d, err := svc.Detail(context.Background(), uuid)
	if err != nil {
		t.Fatal(err)
	}
	var snap requestSnapshot
	if err := json.Unmarshal(d.RequestJSON, &snap); err != nil {
		t.Fatal(err)
	}
	if got := snap.Headers.Get("Authorization"); got != "***" {
		t.Fatalf("Authorization header = %q, want ***", got)
	}
	// 回归：单值 header 应序列化为字符串，而非 []string
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(d.RequestJSON, &raw); err != nil {
		t.Fatal(err)
	}
	headersJSON, ok := raw["headers"]
	if !ok {
		t.Fatalf("headers field missing in %s", d.RequestJSON)
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
	m, err := svc.blobs.readManifest(uuid)
	if err != nil {
		t.Fatal(err)
	}
	result = m.Result
	status = m.HTTPStatus
	channel = m.Channel
	if m.Resp != "" {
		if data, err := svc.blobs.getBlob(m.Resp); err == nil {
			respJSON = string(data)
		}
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
	if ma, merr := svc.blobs.readManifest(uuid); merr != nil || ma.FinishedAt == "" {
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
	m6, err := svc.blobs.readManifest(uuid)
	if err != nil {
		t.Fatal(err)
	}
	result = m6.Result
	status = m6.HTTPStatus
	if m6.Resp != "" {
		if data, err := svc.blobs.getBlob(m6.Resp); err == nil {
			respJSON = string(data)
		}
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
	m7, err := svc.blobs.readManifest(uuid)
	if err != nil {
		t.Fatal(err)
	}
	result = m7.Result
	if m7.Resp != "" {
		if data, err := svc.blobs.getBlob(m7.Resp); err == nil {
			respJSON = string(data)
		}
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

// seedManifest 测试辅助：直接写一个 running 清单 + 元数据行（绕过 HandleBeforeAttempt，
// 供 self-heal / 清理类测试造数据）。started 用当前时间或过去时间。
func seedManifest(t *testing.T, svc *Service, id, requestID string, started time.Time, result string) {
	t.Helper()
	seedManifestAt(t, svc, id, requestID, started, result)
}

// seedManifestAt 同 seedManifest，但显式传时间。
func seedManifestAt(t *testing.T, svc *Service, id, requestID string, started time.Time, result string) {
	t.Helper()
	m := requestManifest{
		ID:        id,
		RequestID: requestID,
		Model:     "gpt-4o",
		Channel:   "c1",
		StartedAt: started.Format(time.RFC3339Nano),
		Result:    result,
	}
	// 写一个占位消息块：让"种子即有 blob"的断言（清空后 GC 归零）有意义。
	h, err := svc.blobs.putBlob([]byte("msg:" + id))
	if err != nil {
		t.Fatal(err)
	}
	m.Messages = []string{h}
	if err := svc.blobs.writeManifest(id, m); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.reqDB.Exec(`INSERT INTO request_logs(id, request_id, model, channel, stream, started_at, result, created_at) VALUES (?, ?, ?, ?, 0, ?, ?, ?)`,
		id, requestID, m.Model, m.Channel, m.StartedAt, result, m.StartedAt); err != nil {
		t.Fatal(err)
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

	seedFilterRows(t, reqDB,
		metaFilterRow{id: "u1", reqID: "r1", model: "gpt-4o", channel: "c1", status: 200, stream: 0, started: "2026-08-01T00:00:00Z", result: "success"},
		metaFilterRow{id: "u2", reqID: "r2", model: "gpt-4o", channel: "c2", status: 429, stream: 0, started: "2026-08-02T00:00:00Z", result: "failed"},
		metaFilterRow{id: "u3", reqID: "r3", model: "claude-3", channel: "c1", status: 200, stream: 1, started: "2026-08-03T00:00:00Z", result: "success"},
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
	if err := svc.SetBlobDir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	// 卡 running：2 小时前开始（远超 60s 阈值），无 loadout（route_requests 查不到 → stream_interrupted）
	old := time.Now().Add(-2 * time.Hour).UTC()
	seedManifest(t, svc, "stuck1", "req-stuck", old, "running")
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
	if err := svc.SetBlobDir(t.TempDir()); err != nil {
		t.Fatal(err)
	}

	old := time.Now().Add(-2 * time.Hour).UTC()
	seedManifest(t, svc, "stuck2", "req-failed", old, "running")
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
	if err := svc.SetBlobDir(t.TempDir()); err != nil {
		t.Fatal(err)
	}

	old := time.Now().Add(-2 * time.Hour).UTC()
	seedManifest(t, svc, "stuck-l1", "req-s1", old, "running")
	seedManifest(t, svc, "fresh-l2", "req-s2", time.Now().UTC(), "running")
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
	if err := svc.SetBlobDir(t.TempDir()); err != nil {
		t.Fatal(err)
	}

	old := time.Now().Add(-2 * time.Hour).UTC()
	seedManifest(t, svc, "stuck-e1", "req-err", old, "running")
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
	if mhr, herr := svc.blobs.readManifest(uuid); herr == nil && mhr.Resp != "" {
		if data, gerr := svc.blobs.getBlob(mhr.Resp); gerr == nil {
			respJSON = string(data)
		}
	} else if herr != nil {
		t.Fatal(herr)
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
	if mtk, terr := svc.blobs.readManifest(uuid); terr == nil && mtk.Resp != "" {
		if data, gerr := svc.blobs.getBlob(mtk.Resp); gerr == nil {
			respJSON = string(data)
		}
	} else if terr != nil {
		t.Fatal(terr)
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
	if err := svc.SetBlobDir(t.TempDir()); err != nil {
		t.Fatal(err)
	}

	// 种子 running 行（started_at 设为很久以前，超出 60s self-heal 阈值）
	started, _ := time.Parse(time.RFC3339Nano, "2026-08-23T00:00:00Z")
	seedManifestAt(t, svc, "uuid-failed-1", "r-heal", started, "running")

	svc.healStuckBlob("uuid-failed-1", "r-heal", started)

	var result, resp string
	var status int
	m, err := svc.blobs.readManifest("uuid-failed-1")
	if err != nil {
		t.Fatal(err)
	}
	result = m.Result
	status = m.HTTPStatus
	if m.Resp != "" {
		if data, err := svc.blobs.getBlob(m.Resp); err == nil {
			resp = string(data)
		}
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
	if err := svc.SetBlobDir(t.TempDir()); err != nil {
		t.Fatal(err)
	}

	started, _ := time.Parse(time.RFC3339Nano, "2026-08-23T00:00:00Z")
	seedManifestAt(t, svc, "uuid-ok-1", "r-heal-ok", started, "running")

	svc.healStuckBlob("uuid-ok-1", "r-heal-ok", started)

	var result string
	m, err := svc.blobs.readManifest("uuid-ok-1")
	if err != nil {
		t.Fatal(err)
	}
	result = m.Result
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

// seedMetaRow 直接插一行元数据（绕过 handler，便于精确控制 started_at）。
func seedMetaRow(t *testing.T, reqDB *sql.DB, id, startedAt string) {
	t.Helper()
	if _, err := reqDB.Exec(`INSERT INTO request_logs(id, request_id, model, channel, stream, started_at, result, created_at) VALUES (?, ?, 'm', 'c', 0, ?, 'success', ?)`,
		id, id, startedAt, startedAt); err != nil {
		t.Fatal(err)
	}
}

// metaFilterRow TestListFilters 造数用：完整元数据字段。
type metaFilterRow struct {
	id, reqID, model, channel string
	status, stream            int
	started, result           string
}

// seedFilterRows 批量插入列表过滤测试用的元数据行。
func seedFilterRows(t *testing.T, reqDB *sql.DB, rows ...metaFilterRow) {
	t.Helper()
	for _, r := range rows {
		if _, err := reqDB.Exec(`INSERT INTO request_logs(id, request_id, model, channel, http_status, stream, started_at, result, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.id, r.reqID, r.model, r.channel, r.status, r.stream, r.started, r.result, r.started); err != nil {
			t.Fatal(err)
		}
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
	if err := svc.SetBlobDir(filepath.Join(t.TempDir(), "request-logs")); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	seedManifest(t, svc, "old", "old", now.AddDate(0, 0, -30).UTC(), "success")
	seedManifest(t, svc, "recent", "recent", now.AddDate(0, 0, -1).UTC(), "success")

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
	if err := svc.SetBlobDir(filepath.Join(t.TempDir(), "request-logs")); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	for _, id := range []string{"a", "b", "c"} {
		seedManifest(t, svc, id, id, now, "success")
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

// TestClearRemovesRowsAndResetsSize 清空后：清单文件与元数据行都清零，
// GC 把无引用块一并回收（磁盘占用真实下降——块存储下没有 VACUUM）。
func TestClearRemovesRowsAndResetsSize(t *testing.T) {
	path := t.TempDir() + "/request-log.db"
	reqDB, err := openRequestLogDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reqDB.Close()
	svc := NewService(nil, slog.New(slog.DiscardHandler), reqDB, nil)
	svc.SetDBPath(path)
	if err := svc.SetBlobDir(filepath.Join(t.TempDir(), "request-logs")); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	for i := 0; i < 20; i++ {
		seedManifest(t, svc, fmt.Sprintf("row-%d", i), fmt.Sprintf("req-%d", i), now, "success")
	}
	blobsBefore, err := countBlobs(svc.blobs)
	if err != nil {
		t.Fatal(err)
	}
	if blobsBefore == 0 {
		t.Fatal("seeded blobs = 0, want > 0")
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
	blobsAfter, err := countBlobs(svc.blobs)
	if err != nil {
		t.Fatal(err)
	}
	if blobsAfter != 0 {
		t.Fatalf("blobs after clear = %d, want 0 (GC must collect all)", blobsAfter)
	}
}

// countBlobs 统计块文件数量（遍历分片目录）。
func countBlobs(bs *blobStore) (int, error) {
	n := 0
	entries, err := os.ReadDir(bs.blobsDir)
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(bs.blobsDir, e.Name()))
		if err != nil {
			continue
		}
		n += len(files)
	}
	return n, nil
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
	if err := svc.SetBlobDir(filepath.Join(dir, "request-logs")); err != nil {
		t.Fatal(err)
	}
	seedManifest(t, svc, "uuid-1", "uuid-1", time.Now().UTC(), "success")

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
	seedMetaRow(t, reqDB, "alive-1", now)
	seedMetaRow(t, reqDB, "alive-2", now)

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
	seedMetaRow(t, reqDB, "one", now.Add(-time.Hour).Format(time.RFC3339Nano))
	seedMetaRow(t, reqDB, "two", now.Format(time.RFC3339Nano))

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
