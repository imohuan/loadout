package adminapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"loadout/plugins/types"
)

// TestMCPServerQuickToolsPersist 快捷访问必须落库：保存后重新读列表仍在，
// 且不影响 MCP 其它配置（这条覆盖「刷新就消失」的问题）。
func TestMCPServerQuickToolsPersist(t *testing.T) {
	ts, _, pw := newTestServer(t)
	cookie := login(t, ts, pw)

	resp, data := apiReq(t, ts, http.MethodPost, "/api/mcp-servers", map[string]any{
		"name": "quick-srv", "transport": "http", "url": "http://127.0.0.1:39999/mcp", "enabled": false,
	}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("创建失败: %s", data)
	}
	var created types.MCPServer
	if err := json.Unmarshal(data, &created); err != nil {
		t.Fatalf("解析创建响应: %v", err)
	}

	// 保存快捷工具（含重复与空串，后端应去重）。
	resp, data = apiReq(t, ts, http.MethodPut, "/api/mcp-servers/"+created.ID+"/quick-tools",
		[]string{"search", "fetch", "search", ""}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("保存快捷工具失败: %d %s", resp.StatusCode, data)
	}

	// 重新读列表：快捷工具仍在（这就是刷新后的读取路径）。
	_, data = apiReq(t, ts, http.MethodGet, "/api/mcp-servers", nil, cookie)
	var list []types.MCPServer
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatalf("解析列表: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("列表条数 = %d，期望 1", len(list))
	}
	got := list[0].QuickTools
	if len(got) != 2 || got[0] != "search" || got[1] != "fetch" {
		t.Fatalf("快捷工具 = %v，期望 [search fetch]", got)
	}
	// 其它字段不受影响。
	if list[0].Name != "quick-srv" || list[0].Enabled {
		t.Fatalf("保存快捷工具不应改动其它配置: %+v", list[0])
	}

	// 清空快捷工具：变成空列表，不是 nil（前端可稳定渲染）。
	resp, _ = apiReq(t, ts, http.MethodPut, "/api/mcp-servers/"+created.ID+"/quick-tools", []string{}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("清空快捷工具失败: %d", resp.StatusCode)
	}
	_, data = apiReq(t, ts, http.MethodGet, "/api/mcp-servers", nil, cookie)
	// 用新变量解析：Go 的 json.Unmarshal 对缺失字段不会清旧值，复用 list 会读到上一步的快捷工具。
	var cleared []types.MCPServer
	if err := json.Unmarshal(data, &cleared); err != nil {
		t.Fatalf("解析列表: %v", err)
	}
	if len(cleared[0].QuickTools) != 0 {
		t.Fatalf("清空后应为空，实际 %v", cleared[0].QuickTools)
	}
}

// TestMCPServerQuickToolsReorderKeepsData 拖拽排序/编辑不应丢掉快捷工具。
func TestMCPServerQuickToolsReorderKeepsData(t *testing.T) {
	ts, _, pw := newTestServer(t)
	cookie := login(t, ts, pw)

	ids := make([]string, 0, 2)
	for _, name := range []string{"a", "b"} {
		_, data := apiReq(t, ts, http.MethodPost, "/api/mcp-servers", map[string]any{
			"name": name, "transport": "http", "url": "http://127.0.0.1:39999/mcp", "enabled": false,
		}, cookie)
		var srv types.MCPServer
		if err := json.Unmarshal(data, &srv); err != nil {
			t.Fatalf("解析创建响应: %v", err)
		}
		ids = append(ids, srv.ID)
	}
	if resp, data := apiReq(t, ts, http.MethodPut, "/api/mcp-servers/"+ids[0]+"/quick-tools", []string{"keep_me"}, cookie); resp.StatusCode != http.StatusOK {
		t.Fatalf("保存快捷工具失败: %s", data)
	}

	// 倒序拖拽：快捷工具必须跟着服务器一起走。
	if resp, data := apiReq(t, ts, http.MethodPost, "/api/mcp-servers/reorder", []string{ids[1], ids[0]}, cookie); resp.StatusCode != http.StatusOK {
		t.Fatalf("排序失败: %s", data)
	}
	_, data := apiReq(t, ts, http.MethodGet, "/api/mcp-servers", nil, cookie)
	var list []types.MCPServer
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatalf("解析列表: %v", err)
	}
	if list[0].Name != "b" || list[1].Name != "a" {
		t.Fatalf("排序结果不符: %+v", list)
	}
	if len(list[1].QuickTools) != 1 || list[1].QuickTools[0] != "keep_me" {
		t.Fatalf("排序后快捷工具丢失: %+v", list[1].QuickTools)
	}
}
