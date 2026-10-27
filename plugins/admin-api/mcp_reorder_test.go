package adminapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"loadout/plugins/types"
)

// TestMCPServersReorder 拖拽排序：只改 position，不动配置字段；
// 未列出的条目补在末尾（一次拖拽不能让配置项消失）。
func TestMCPServersReorder(t *testing.T) {
	ts, _, pw := newTestServer(t)
	cookie := login(t, ts, pw)

	created := make([]types.MCPServer, 0, 3)
	for _, name := range []string{"a", "b", "c"} {
		resp, data := apiReq(t, ts, http.MethodPost, "/api/mcp-servers", map[string]any{
			"name": name, "transport": "http", "url": "http://127.0.0.1:39999/mcp", "enabled": false,
		}, cookie)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("创建 %s 失败: %s", name, data)
		}
		var srv types.MCPServer
		if err := json.Unmarshal(data, &srv); err != nil {
			t.Fatalf("解析创建响应: %v", err)
		}
		created = append(created, srv)
	}

	// 倒序保存：c、a（b 故意缺省，应按原位置补在末尾）。
	resp, data := apiReq(t, ts, http.MethodPost, "/api/mcp-servers/reorder",
		[]string{created[2].ID, created[0].ID}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reorder 期望 200，实际 %d: %s", resp.StatusCode, data)
	}

	_, data = apiReq(t, ts, http.MethodGet, "/api/mcp-servers", nil, cookie)
	var list []types.MCPServer
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatalf("解析列表: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("排序后条目数 = %d，期望 3（不能丢配置）", len(list))
	}
	want := []string{"c", "a", "b"}
	for i, name := range want {
		if list[i].Name != name {
			t.Fatalf("第 %d 项 = %s，期望 %s（实际顺序：%v）", i+1, list[i].Name, name, names(list))
		}
	}
	// 配置字段不应被排序改动。
	if list[0].URL != created[2].URL || list[0].Enabled != created[2].Enabled {
		t.Fatalf("排序不应改动配置字段：%+v", list[0])
	}
}

func names(list []types.MCPServer) []string {
	out := make([]string, 0, len(list))
	for _, it := range list {
		out = append(out, it.Name)
	}
	return out
}

// TestMCPSettingsReconnectMaxPersisted 设置页的自动重连次数要能存取（0 = 默认）。
func TestMCPSettingsReconnectMaxPersisted(t *testing.T) {
	ts, _, pw := newTestServer(t)
	cookie := login(t, ts, pw)

	resp, data := apiReq(t, ts, http.MethodPut, "/api/settings", map[string]any{
		"mcp_reconnect_max": 5,
	}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("保存设置失败: %d %s", resp.StatusCode, data)
	}
	_, data = apiReq(t, ts, http.MethodGet, "/api/settings", nil, cookie)
	var settings types.Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("解析设置: %v", err)
	}
	if settings.McpReconnectMax != 5 {
		t.Fatalf("mcp_reconnect_max = %d，期望 5", settings.McpReconnectMax)
	}
}
