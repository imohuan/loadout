package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"loadout/core/db"
	"loadout/plugins/types"
)

// TestAggregateTargetsOrderPersists 回归：聚合模型的目标数组顺序必须落成
// aggregate_targets.position，且读回顺序与提交顺序一致（路由按该顺序 failover）。
//
// 前端拖拽排序只改数组顺序，不做任何 position 计算；一旦后端按名字/ID 排序，
// 用户在界面上拖出来的「先试 A 再试 B」就会静默失效。
func TestAggregateTargetsOrderPersists(t *testing.T) {
	ts, st, pw := newDBTestServer(t)
	cookie := login(t, ts, pw)

	if resp, data := apiReq(t, ts, http.MethodPost, "/api/channels", map[string]any{
		"name": "wb", "base_url": "http://wb/v1", "enabled": true,
		"models": []string{"m-a", "m-b", "m-c"},
	}, cookie); resp.StatusCode != http.StatusOK {
		t.Fatalf("创建渠道失败: %d %s", resp.StatusCode, data)
	}
	var channels []types.Channel
	_, data := apiReq(t, ts, http.MethodGet, "/api/channels", nil, cookie)
	if err := json.Unmarshal(data, &channels); err != nil {
		t.Fatalf("解析渠道: %v", err)
	}
	if len(channels) != 1 {
		t.Fatalf("渠道数 = %d", len(channels))
	}
	baseURL := channels[0].BaseURL

	order := []string{"m-c", "m-a", "m-b"}
	agg := []map[string]any{{
		"name":    "ordered",
		"enabled": true,
		"targets": []map[string]any{
			{"model": order[0], "channel_base_url": baseURL},
			{"model": order[1], "channel_base_url": baseURL},
			{"model": order[2], "channel_base_url": baseURL},
		},
	}}
	if resp, data := apiReq(t, ts, http.MethodPut, "/api/aggregates", agg, cookie); resp.StatusCode != http.StatusOK {
		t.Fatalf("保存聚合模型失败: %d %s", resp.StatusCode, data)
	}

	var got []types.AggregateModel
	_, data = apiReq(t, ts, http.MethodGet, "/api/aggregates", nil, cookie)
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("解析聚合模型: %v", err)
	}
	if len(got) != 1 || len(got[0].Targets) != 3 {
		t.Fatalf("聚合模型/目标数不符: %+v", got)
	}
	for i, want := range order {
		if got[0].Targets[i].Model != want {
			t.Fatalf("接口读回第 %d 个目标 = %q, 期望 %q", i, got[0].Targets[i].Model, want)
		}
	}

	database, err := db.OpenForStore(st)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	rows, err := database.QueryContext(context.Background(),
		"SELECT position, model FROM aggregate_targets ORDER BY position")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	i := 0
	for rows.Next() {
		var pos int
		var model string
		if err := rows.Scan(&pos, &model); err != nil {
			t.Fatal(err)
		}
		if pos != i || model != order[i] {
			t.Fatalf("第 %d 行: position=%d model=%q, 期望 position=%d model=%q", i, pos, model, i, order[i])
		}
		i++
	}
	if i != 3 {
		t.Fatalf("targets 行数 = %d, 期望 3", i)
	}
}

// TestChannelCreateInsertsIntoGroup 回归：往已有渠道组里加 Key 时，必须插在
// 该组成员之后（组内最后一个），而不是整个列表末尾。
//
// position 是全局顺序、前端按 base_url 分组展示：append 到末尾会让新 Key
// 实际排在其它平台之后，用户看到的组内顺序与真实尝试顺序不一致。
func TestChannelCreateInsertsIntoGroup(t *testing.T) {
	ts, _, pw := newDBTestServer(t)
	cookie := login(t, ts, pw)
	add := func(name, base string) {
		if resp, data := apiReq(t, ts, http.MethodPost, "/api/channels", map[string]any{
			"name": name, "base_url": base, "enabled": true,
		}, cookie); resp.StatusCode != http.StatusOK {
			t.Fatalf("创建 %s 失败: %d %s", name, resp.StatusCode, data)
		}
	}
	add("wb-1", "http://wb/v1")
	add("wb-2", "http://wb/v1")
	add("px-1", "http://px/v1")
	add("wb-3", "http://wb/v1")

	_, data := apiReq(t, ts, http.MethodGet, "/api/channels", nil, cookie)
	var list []types.Channel
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatalf("解析渠道: %v", err)
	}
	got := make([]string, 0, len(list))
	for _, ch := range list {
		got = append(got, ch.Name)
	}
	want := []string{"wb-1", "wb-2", "wb-3", "px-1"}
	if len(got) != len(want) {
		t.Fatalf("渠道数 = %d, 期望 %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("顺序 = %v, 期望 %v", got, want)
		}
	}
}
