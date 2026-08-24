package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"loadout/core/config"
	"loadout/plugins/types"
	fakemcp "loadout/testkit/fake-mcp"
)

// TestMCPLogsEndpoints 端到端验证日志三端点 + 删除联动：
// 真实 fake-mcp 连接 + invoke 产生日志 → 列表/段列表/增量读 → 删 server → 列表空。
func TestMCPLogsEndpoints(t *testing.T) {
	// 隔离 LogsDir：hub 在 newStatsTestServer 内创建，必须在此之前设置。
	oldLogsDir := config.LogsDir
	config.LogsDir = t.TempDir()
	t.Cleanup(func() { config.LogsDir = oldLogsDir })

	ts, hub, _, pw := newStatsTestServer(t)
	cookie := login(t, ts, pw)

	// fake-mcp 上游（http）
	f1 := fakemcp.New("github", []fakemcp.Tool{
		{Name: "search_code", Description: "搜索代码", InputSchema: map[string]any{"type": "object"}, Result: "github search result"},
	})
	t.Cleanup(f1.Close)

	// 创建 server（走真实 CRUD 端点）
	resp, _ := apiReq(t, ts, http.MethodPost, "/api/mcp-servers", map[string]any{
		"name": "github", "transport": "http", "url": f1.URL(), "enabled": true,
	}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/mcp-servers 期望 200，实际 %d", resp.StatusCode)
	}

	// 触发真实连接 + invoke → 写 CONNECT/FRAME 日志
	if _, err := hub.BuildIndex(context.Background()); err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	out, err := hub.Invoke(context.Background(), "/mcp/github",
		map[string]any{"tool": "search_code", "arguments": map[string]any{}})
	if err != nil || out == "" {
		t.Fatalf("Invoke 期望成功，err=%v out=%q", err, out)
	}

	// GET /api/mcp-servers/logs → 列表含 github + files 非空
	resp, data := apiReq(t, ts, http.MethodGet, "/api/mcp-servers/logs", nil, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/mcp-servers/logs 期望 200，实际 %d", resp.StatusCode)
	}
	var list struct {
		Items []struct {
			Name      string `json:"name"`
			Transport string `json:"transport"`
			Files     []struct {
				Name   string `json:"name"`
				Size   int64  `json:"size"`
				Active bool   `json:"active"`
			} `json:"files"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(data), &list); err != nil {
		t.Fatalf("解析 logs 列表: %v (%s)", err, data)
	}
	if len(list.Items) != 1 || list.Items[0].Name != "github" || list.Items[0].Transport != "http" {
		t.Fatalf("logs 列表异常: %+v", list.Items)
	}
	if len(list.Items[0].Files) != 1 || list.Items[0].Files[0].Name == "" {
		t.Fatalf("github 应有 1 个段文件: %+v", list.Items[0].Files)
	}
	segName := list.Items[0].Files[0].Name

	// GET /api/mcp-servers/github/log/files → 段列表一致
	resp, data = apiReq(t, ts, http.MethodGet, "/api/mcp-servers/github/log/files", nil, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("log/files 期望 200，实际 %d", resp.StatusCode)
	}
	if !strings.Contains(string(data), segName) {
		t.Fatalf("log/files 应含段 %s，实际 %s", segName, data)
	}

	// GET /api/mcp-servers/github/log → 增量读：全量
	resp, data = apiReq(t, ts, http.MethodGet, "/api/mcp-servers/github/log?offset=0", nil, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("log 全量读期望 200，实际 %d", resp.StatusCode)
	}
	var rd struct {
		Name    string `json:"name"`
		File    string `json:"file"`
		Offset  int64  `json:"offset"`
		Size    int64  `json:"size"`
		EOF     bool   `json:"eof"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(data), &rd); err != nil {
		t.Fatalf("解析 log 读取: %v (%s)", err, data)
	}
	if rd.Name != "github" || rd.File != segName || rd.Size == 0 || !rd.EOF {
		t.Fatalf("log 读取响应异常: %+v", rd)
	}
	for _, want := range []string{"[CONNECT]", "[CONNECT_OK]", "[FRAME_OUT]", "[FRAME_IN]", `transport="http"`, `url="`} {
		if !strings.Contains(rd.Content, want) {
			t.Fatalf("日志缺 %q，内容:\n%s", want, rd.Content)
		}
	}
	if strings.Contains(rd.Content, "github search result") == false {
		t.Fatalf("日志应含工具结果，内容:\n%s", rd.Content)
	}

	// 增量语义：从中间 offset 读 → 拼起来等于全量
	half := rd.Size / 2
	_, d2 := apiReq(t, ts, http.MethodGet, "/api/mcp-servers/github/log?offset=0&limit="+itos(half), nil, cookie)
	var p1 struct {
		Content string `json:"content"`
		EOF     bool   `json:"eof"`
	}
	_ = json.Unmarshal([]byte(d2), &p1)
	if p1.EOF {
		t.Fatal("前半读不应 eof")
	}
	_, d3 := apiReq(t, ts, http.MethodGet, "/api/mcp-servers/github/log?offset="+itos(half), nil, cookie)
	var p2 struct {
		Content string `json:"content"`
		EOF     bool   `json:"eof"`
	}
	_ = json.Unmarshal([]byte(d3), &p2)
	if !p2.EOF {
		t.Fatal("后半读应 eof")
	}
	if p1.Content+p2.Content != rd.Content {
		t.Fatal("增量拼接 != 全量")
	}

	// 默认 file 参数（不传 file）= 最新段
	_, dDefault := apiReq(t, ts, http.MethodGet, "/api/mcp-servers/github/log", nil, cookie)
	var def struct {
		File string `json:"file"`
	}
	_ = json.Unmarshal([]byte(dDefault), &def)
	if def.File != segName {
		t.Fatalf("默认 file 应是最新段 %s，实际 %s", segName, def.File)
	}

	// 删除 server → 日志目录联动清理 → 列表空
	_, dList := apiReq(t, ts, http.MethodGet, "/api/mcp-servers", nil, cookie)
	var servers []types.MCPServer
	if err := json.Unmarshal([]byte(dList), &servers); err != nil || len(servers) != 1 {
		t.Fatalf("server 列表解析异常: err=%v len=%d", err, len(servers))
	}
	resp, _ = apiReq(t, ts, http.MethodDelete, "/api/mcp-servers", map[string]string{"id": servers[0].ID}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE 期望 200，实际 %d", resp.StatusCode)
	}
	resp, data = apiReq(t, ts, http.MethodGet, "/api/mcp-servers/logs", nil, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("删除后 logs 列表期望 200，实际 %d", resp.StatusCode)
	}
	if !strings.Contains(string(data), `"items":[]`) {
		t.Fatalf("删除后 logs 列表应为空，实际 %s", data)
	}
}

// TestMCPLogsAuthAndPathTraversal 验证鉴权与路径穿越防护。
func TestMCPLogsAuthAndPathTraversal(t *testing.T) {
	oldLogsDir := config.LogsDir
	config.LogsDir = t.TempDir()
	t.Cleanup(func() { config.LogsDir = oldLogsDir })

	ts, _, _, pw := newStatsTestServer(t)
	cookie := login(t, ts, pw)

	// 无会话 → 401
	for _, path := range []string{"/api/mcp-servers/logs", "/api/mcp-servers/github/log", "/api/mcp-servers/github/log/files"} {
		resp, _ := apiReq(t, ts, http.MethodGet, path, nil, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s 无会话应 401，实际 %d", path, resp.StatusCode)
		}
	}

	// 路径穿越 / 非法名 → 400 或 404（ServeMux 可能先把 ../ 归一化成 404，都算拦截成功）
	for _, path := range []string{
		"/api/mcp-servers/../log",
		"/api/mcp-servers/..%2Fevil/log",
		"/api/mcp-servers/%2e%2e/log",
	} {
		resp, _ := apiReq(t, ts, http.MethodGet, path, nil, cookie)
		if resp.StatusCode < http.StatusBadRequest {
			t.Fatalf("%s 应 ≥400，实际 %d", path, resp.StatusCode)
		}
	}
	// 非法段文件名 → 400
	resp, _ := apiReq(t, ts, http.MethodGet, "/api/mcp-servers/github/log?file=../evil.log", nil, cookie)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("file=../evil.log 应 400，实际 %d", resp.StatusCode)
	}
	resp, _ = apiReq(t, ts, http.MethodGet, "/api/mcp-servers/github/log?file=random.txt", nil, cookie)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("file=random.txt 应 400，实际 %d", resp.StatusCode)
	}
	// 不存在的 server / 段 → 200 空（不报错）
	resp, data := apiReq(t, ts, http.MethodGet, "/api/mcp-servers/ghost/log", nil, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("不存在的 server 应 200，实际 %d", resp.StatusCode)
	}
	if !strings.Contains(string(data), `"eof":true`) {
		t.Fatalf("空日志应 eof=true，实际 %s", data)
	}
}

// itos 快速 int64→string（测试内联用）。
func itos(v int64) string {
	return strconv.FormatInt(v, 10)
}
