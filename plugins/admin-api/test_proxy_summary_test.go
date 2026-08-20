package adminapi

// 模型测试请求的「旁路探针」语义测试：
//  1. 测试请求绝不写入转发日志（route_requests 表保持 0 行）——前端「请求记录」
//     面板依赖响应回带的访问摘要直显，只有上游是 Loadout 自身服务时由 router 写日志。
//  2. 访问摘要经两条通道回带：非流式/错误路径用响应头 X-Test-Log（base64 JSON），
//     流式成功路径在 SSE 末尾追加 route_log 事件（data 为 base64 JSON）。

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loadout/core/config"
	"loadout/core/db"
	"loadout/core/store"
	"loadout/plugins/admin-auth"
	"loadout/plugins/gateway-keys"
	routelog "loadout/plugins/route-log"
	"loadout/plugins/skills"
	unifyai "loadout/plugins/unifyai"

	_ "modernc.org/sqlite"
)

// newRouteLogTestServer 装配带真实 route-log 服务的完整管理后台（core/db.Open
// 建全量 schema，含 route_requests 表），返回服务器、db 句柄与初始密码。
func newRouteLogTestServer(t *testing.T) (*httptest.Server, *sql.DB, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}

	old := config.AdminPasswordFile
	config.AdminPasswordFile = filepath.Join(dir, "admin-password")
	t.Cleanup(func() { config.AdminPasswordFile = old })

	authSvc := adminauth.NewService(st, slog.Default())
	if _, err := authSvc.EnsureFirstRun(); err != nil {
		t.Fatalf("EnsureFirstRun: %v", err)
	}
	pw, err := os.ReadFile(config.AdminPasswordFile)
	if err != nil {
		t.Fatalf("读取初始密码: %v", err)
	}

	keys := gatewaykeys.NewManager(st)
	skillSvc := skills.NewService(st, slog.Default(), t.TempDir(), t.TempDir())

	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "loadout.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	routeLogSvc := routelog.NewService(sqlDB, slog.Default())
	svc := NewService(st, slog.Default(), authSvc, keys, skillSvc, nil, unifyai.NewService(slog.Default()))
	svc.SetRoutingServices(sqlDB, nil, nil, routeLogSvc)

	ts := httptest.NewServer(svc.Handler())
	t.Cleanup(ts.Close)
	return ts, sqlDB, string(pw)
}

// countRouteRequests 返回 route_requests 表中的行数。
func countRouteRequests(t *testing.T, sqlDB *sql.DB) int {
	t.Helper()
	var count int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM route_requests`).Scan(&count); err != nil {
		t.Fatalf("查询 route_requests: %v", err)
	}
	return count
}

// TestModelTestNoRouteLogWritten 验证测试请求不写转发日志：非流式与流式各发一次，
// route_requests 表必须保持 0 行（前端面板数据来自响应回带的访问摘要）。
func TestModelTestNoRouteLogWritten(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"1","choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer upstream.Close()

	ts, sqlDB, pw := newRouteLogTestServer(t)
	cookie := login(t, ts, pw)

	send := func(body map[string]any) *http.Response {
		t.Helper()
		resp, data := apiReq(t, ts, http.MethodPost, "/api/test/chat", body, cookie)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("chat 期望 200，实际 %d: %s", resp.StatusCode, data)
		}
		return resp
	}

	// 非流式。
	resp := send(map[string]any{
		"base_url": upstream.URL + "/v1", "model": "m",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if resp.Header.Get("X-Test-Log") == "" {
		t.Fatal("非流式响应应携带 X-Test-Log 摘要")
	}
	if got := countRouteRequests(t, sqlDB); got != 0 {
		t.Fatalf("非流式测试请求不应写入转发日志，实际 %d 行", got)
	}

	// 流式（上游返回 SSE，测试请求同样不写日志）。
	streamUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+`{"choices":[{"delta":{"content":"hi"}}]}`+"\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer streamUp.Close()
	send(map[string]any{
		"base_url": streamUp.URL + "/v1", "model": "m", "stream": true,
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if got := countRouteRequests(t, sqlDB); got != 0 {
		t.Fatalf("流式测试请求不应写入转发日志，实际 %d 行", got)
	}

	// 错误路径（上游 401）：同样不写日志，且响应头携带 failed 摘要。
	errUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"bad key"}}`)
	}))
	defer errUp.Close()
	errResp, errData := apiReq(t, ts, http.MethodPost, "/api/test/chat", map[string]any{
		"base_url": errUp.URL + "/v1", "model": "m",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	}, cookie)
	if errResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("期望透传 401，实际 %d: %s", errResp.StatusCode, errData)
	}
	if errResp.Header.Get("X-Test-Log") == "" {
		t.Fatal("错误路径响应应携带 X-Test-Log 摘要")
	}
	if got := countRouteRequests(t, sqlDB); got != 0 {
		t.Fatalf("错误路径测试请求不应写入转发日志，实际 %d 行", got)
	}
}

// TestModelTestStreamSummaryEvent 验证流式成功路径：SSE 末尾追加 route_log 事件，
// data 为 base64 JSON，含 request_id / model / result / duration_ms / stream=true。
func TestModelTestStreamSummaryEvent(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+`{"choices":[{"delta":{"content":"a"}}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: "+`{"choices":[{"delta":{"content":"b"}}]}`+"\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	ts, _, pw := newRouteLogTestServer(t)
	cookie := login(t, ts, pw)

	resp, data := apiReq(t, ts, http.MethodPost, "/api/test/chat", map[string]any{
		"base_url": upstream.URL + "/v1", "model": "gpt-4o", "stream": true,
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("chat 期望 200，实际 %d: %s", resp.StatusCode, data)
	}
	body := string(data)
	if !strings.Contains(body, `"content":"a"`) || !strings.Contains(body, `"content":"b"`) {
		t.Fatalf("流式响应未透传增量: %s", body)
	}
	if !strings.Contains(body, "event: route_log") {
		t.Fatalf("流式响应应追加 route_log 事件，实际: %s", body)
	}

	// 提取最后一个 route_log 事件的 data 并解码校验。
	summary := extractStreamSummary(t, body)
	if summary.RequestID == "" || summary.RequestedModel != "gpt-4o" ||
		summary.Result != "success" || !summary.Stream || summary.DurationMS < 0 {
		t.Fatalf("route_log 摘要字段不符: %+v", summary)
	}
}

// TestModelTestErrorPathSummaryHeader 验证错误路径（上游非 2xx）：透传错误体的同时，
// 响应头 X-Test-Log 仍携带 result=failed 的摘要，前端 throw 前也能拿到。
func TestModelTestErrorPathSummaryHeader(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"bad key"}}`)
	}))
	defer upstream.Close()

	ts, _, pw := newRouteLogTestServer(t)
	cookie := login(t, ts, pw)

	resp, data := apiReq(t, ts, http.MethodPost, "/api/test/chat", map[string]any{
		"base_url": upstream.URL + "/v1", "model": "m",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	}, cookie)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("期望透传 401，实际 %d: %s", resp.StatusCode, data)
	}
	header := resp.Header.Get("X-Test-Log")
	if header == "" {
		t.Fatal("错误路径响应应携带 X-Test-Log 摘要")
	}
	raw, err := base64.StdEncoding.DecodeString(header)
	if err != nil {
		t.Fatalf("X-Test-Log 不是合法 base64: %v", err)
	}
	var summary struct {
		RequestID    string `json:"request_id"`
		Result       string `json:"result"`
		HTTPStatus   int    `json:"http_status"`
		ErrorMessage string `json:"error_message"`
		Attempts     []struct {
			FailureClass string `json:"failure_class"`
		} `json:"attempts"`
	}
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatalf("X-Test-Log 不是合法 JSON: %v", err)
	}
	if summary.RequestID == "" || summary.Result != "failed" || summary.HTTPStatus != http.StatusUnauthorized ||
		summary.ErrorMessage == "" || len(summary.Attempts) == 0 || summary.Attempts[0].FailureClass != "auth" {
		t.Fatalf("错误路径摘要字段不符: %+v", summary)
	}
}

// extractStreamSummary 从 SSE 响应体中提取最后一个 route_log 事件的 data（base64 JSON）。
func extractStreamSummary(t *testing.T, body string) testLogSummary {
	t.Helper()
	lines := strings.Split(body, "\n")
	var lastData string
	for i, line := range lines {
		if strings.TrimSpace(line) != "event: route_log" || i+1 >= len(lines) {
			continue
		}
		if d, ok := strings.CutPrefix(strings.TrimSpace(lines[i+1]), "data:"); ok {
			lastData = strings.TrimSpace(d)
		}
	}
	if lastData == "" {
		t.Fatal("未找到 route_log 事件的 data 载荷")
	}
	raw, err := base64.StdEncoding.DecodeString(lastData)
	if err != nil {
		t.Fatalf("route_log data 不是合法 base64: %v", err)
	}
	var summary testLogSummary
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatalf("route_log data 不是合法 JSON: %v", err)
	}
	return summary
}
