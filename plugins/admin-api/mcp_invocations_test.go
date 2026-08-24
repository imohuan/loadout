package adminapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestMCPInvocationsEndpoint 验证 GET /api/mcp-invocations：
// 造数 → 分页/过滤/无会话 401/非法分页 400。
func TestMCPInvocationsEndpoint(t *testing.T) {
	ts, _, sqlDB, pw := newStatsTestServer(t)
	cookie := login(t, ts, pw)

	// 直接向 mcp_invocations 造 3 条不同 kind/auth 的数据。
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rows := [][]any{
		{now, "single", "DEMO", "read_file", "fs", "success", 200, 12, "", `{"p":"/x"}`, `[{"type":"text","text":"ok"}]`, "mcp-key"},
		{now, "group", "search", "web_search", "exa", "success", 200, 34, "", `{"q":"go"}`, `[{"type":"text","text":"hit"}]`, "session"},
		{now, "$smart", nil, "ws_exa", "exa", "error", 500, 56, "boom", `{"q":"x"}`, "", "public"},
	}
	for _, r := range rows {
		if _, err := sqlDB.Exec(`INSERT INTO mcp_invocations
			(started_at, aggregate_kind, aggregate_target, tool_name, server_name, result,
			 http_status, duration_ms, error_message, input_json, output_json, auth_kind)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, r...); err != nil {
			t.Fatalf("造数失败: %v", err)
		}
	}

	// 无会话 → 401
	resp, _ := apiReq(t, ts, http.MethodGet, "/api/mcp-invocations", nil, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无会话期望 401，实际 %d", resp.StatusCode)
	}

	// 全量分页（size=2，total=3）
	resp, data := apiReq(t, ts, http.MethodGet, "/api/mcp-invocations?page=1&page_size=2", nil, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("列表期望 200，实际 %d", resp.StatusCode)
	}
	var page struct {
		Items []struct {
			ToolName     string `json:"tool_name"`
			AuthKind     string `json:"auth_kind"`
			InputJSON    string `json:"input_json"`
			OutputJSON   string `json:"output_json"`
			AggregateKd  string `json:"aggregate_kind"`
			DurationMS   int    `json:"duration_ms"`
			ErrorMessage string `json:"error_message"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal([]byte(data), &page); err != nil {
		t.Fatalf("解析列表: %v (%s)", err, data)
	}
	if page.Total != 3 || len(page.Items) != 2 {
		t.Fatalf("期望 total=3 items=2，实际 total=%d items=%d", page.Total, len(page.Items))
	}

	// kind=single 过滤 → total=1
	_, data = apiReq(t, ts, http.MethodGet, "/api/mcp-invocations?kind=single", nil, cookie)
	if err := json.Unmarshal([]byte(data), &page); err != nil {
		t.Fatalf("解析 kind 过滤: %v", err)
	}
	if page.Total != 1 || page.Items[0].ToolName != "read_file" {
		t.Fatalf("kind=single 期望 read_file，实际 %+v", page.Items)
	}
	if page.Items[0].AuthKind != "mcp-key" || page.Items[0].InputJSON == "" || page.Items[0].OutputJSON == "" {
		t.Fatalf("v2 字段回读异常: %+v", page.Items[0])
	}

	// auth=session 过滤 → total=1（web_search）
	_, data = apiReq(t, ts, http.MethodGet, "/api/mcp-invocations?auth=session", nil, cookie)
	if err := json.Unmarshal([]byte(data), &page); err != nil {
		t.Fatalf("解析 auth 过滤: %v", err)
	}
	if page.Total != 1 || page.Items[0].ToolName != "web_search" {
		t.Fatalf("auth=session 期望 web_search，实际 %+v", page.Items)
	}

	// 失败调用：error_message 回读
	_, data = apiReq(t, ts, http.MethodGet, "/api/mcp-invocations?kind=%24smart", nil, cookie)
	if err := json.Unmarshal([]byte(data), &page); err != nil {
		t.Fatalf("解析 $smart 过滤: %v", err)
	}
	if page.Total != 1 || !strings.Contains(page.Items[0].ErrorMessage, "boom") || page.Items[0].OutputJSON != "" {
		t.Fatalf("失败调用字段异常: %+v", page.Items[0])
	}

	// 非法分页参数 → 400
	resp, _ = apiReq(t, ts, http.MethodGet, "/api/mcp-invocations?page=abc", nil, cookie)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("page=abc 期望 400，实际 %d", resp.StatusCode)
	}
}
