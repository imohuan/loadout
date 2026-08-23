package requestlog

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"loadout/core/db"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	routelog "loadout/plugins/route-log"
	"loadout/plugins/contracts"
	"loadout/plugins/types"
)

// TestIntegrationCrossPluginLink 真实联动：route-log.Start 写 running → request-log
// 在 before-attempt 生成 UUID 并 UPDATE route_requests.request_log_id → route-log
// List/Detail 带出该字段 → request-log 收尾 success。
func TestIntegrationCrossPluginLink(t *testing.T) {
	// 真实 loadout.db（自带迁移）+ 真实 request-log.db
	loadout, err := db.Open(t.TempDir() + "/loadout.db")
	if err != nil {
		t.Fatal(err)
	}
	defer loadout.Close()
	reqDB, err := openRequestLogDB(t.TempDir() + "/request-log.db")
	if err != nil {
		t.Fatal(err)
	}
	defer reqDB.Close()

	// route-log 装配
	routeLog := routelog.NewService(loadout, nil)

	// request-log 装配（能力路由：全模型 proxy）
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Write(types.FileCapabilityRoutes, []types.CapabilityRoute{
		{Models: []string{"*"}, Capability: capabilityName, Route: types.RouteProxy},
	}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(st, slog.New(slog.DiscardHandler), reqDB, loadout)

	ctx := context.Background()
	// 1) route-log.Start 写 running 占位
	started := time.Now().Add(-time.Second)
	if err := routeLog.Start(ctx, contracts.RouteRequest{RequestID: "it-1", RequestedModel: "gpt-4o", StartedAt: started}); err != nil {
		t.Fatal(err)
	}

	// 2) request-log 在请求发出前抓取 + 生成 UUID
	pipe := testPipe("it-1")
	if _, err := svc.HandleBeforeAttempt(pipe); err != nil {
		t.Fatal(err)
	}
	uuid, _ := pipe.Metadata[metadataKey].(string)
	if uuid == "" {
		t.Fatal("uuid missing")
	}

	// 3) route-log 列表带出 request_log_id
	page, err := routeLog.List(ctx, contracts.RouteLogFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].RequestLogID != uuid {
		t.Fatalf("route-log List 未带出 request_log_id: %+v", page.Items)
	}

	// 4) request-log 收尾（非流式 2xx）
	pipe.Metadata["__last_tried_channel"] = "c1"
	if _, err := svc.HandleAfterUpstream(&modelgateway.AfterUpstreamPayload{
		Pipe: pipe,
		Response: &modelgateway.ProxyResponse{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       []byte(`{"choices":[{"message":{"content":"ok"}}]}`),
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 5) request-log 详情：success + 完整 request/response
	d, err := svc.Detail(ctx, uuid)
	if err != nil {
		t.Fatal(err)
	}
	if d.Result != "success" {
		t.Fatalf("result = %q, want success", d.Result)
	}
	var snap requestSnapshot
	if err := json.Unmarshal(d.RequestJSON, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Model != "gpt-4o" || snap.Path != "chat/completions" {
		t.Fatalf("request snapshot wrong: %+v", snap)
	}
	if len(d.ResponseJSON) == 0 {
		t.Fatal("response_json should be set")
	}
}
