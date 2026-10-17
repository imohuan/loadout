package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"loadout/core/db"
	"loadout/plugins/types"
)

// TestReorderLevelsAreIndependent 两级顺序必须互不影响：
//   - mode=group 只改渠道组间顺序，组内 Key 顺序保持；
//   - mode=key   只改组内 Key 顺序，组间顺序保持。
//
// 回归背景：旧实现只有一个全局 position，前端为了「拖组内某个 Key」不得不
// 提交全量顺序，结果组间顺序也被一起重排；用户拖一个 Key，整页渠道组顺序
// 跟着变。
func TestReorderLevelsAreIndependent(t *testing.T) {
	ts, st, pw := newDBTestServer(t)
	cookie := login(t, ts, pw)

	// wb 组 3 个 Key，px 组 2 个 Key。
	add := func(name, base string) string {
		resp, data := apiReq(t, ts, http.MethodPost, "/api/channels", map[string]any{
			"name": name, "base_url": base, "enabled": true,
			"models": []string{"m"},
		}, cookie)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("创建 %s 失败: %d %s", name, resp.StatusCode, data)
		}
		var created types.Channel
		if err := json.Unmarshal(data, &created); err != nil {
			t.Fatalf("解析创建响应: %v", err)
		}
		return created.ID
	}
	wb1 := add("wb1", "http://wb/v1")
	wb2 := add("wb2", "http://wb/v1")
	wb3 := add("wb3", "http://wb/v1")
	px1 := add("px1", "http://px/v1")
	px2 := add("px2", "http://px/v1")

	listOrder := func() []string {
		_, data := apiReq(t, ts, http.MethodGet, "/api/channels", nil, cookie)
		var list []types.Channel
		if err := json.Unmarshal(data, &list); err != nil {
			t.Fatalf("解析渠道列表: %v", err)
		}
		out := make([]string, 0, len(list))
		for _, ch := range list {
			out = append(out, ch.Name)
		}
		return out
	}

	// 初始：wb1 wb2 wb3 px1 px2
	if got := listOrder(); len(got) != 5 || got[0] != "wb1" || got[3] != "px1" {
		t.Fatalf("初始顺序异常: %v", got)
	}

	// 1) mode=key：把 wb3 拖到 wb 组最前。组间顺序（wb 在 px 前）必须不变。
	resp, data := apiReq(t, ts, http.MethodPost, "/api/channels/reorder", map[string]any{
		"ids":  []string{wb3, wb1, wb2, px1, px2},
		"mode": "key",
	}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("mode=key 期望 200，实际 %d: %s", resp.StatusCode, data)
	}
	got := listOrder()
	want := []string{"wb3", "wb1", "wb2", "px1", "px2"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("mode=key 后顺序 = %v, 期望 %v", got, want)
		}
	}

	// 2) mode=group：把 px 组挪到 wb 组前面。组内顺序（wb3 wb1 wb2）必须保持。
	resp, data = apiReq(t, ts, http.MethodPost, "/api/channels/reorder", map[string]any{
		"ids":  []string{px1, px2, wb1, wb2, wb3},
		"mode": "group",
	}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("mode=group 期望 200，实际 %d: %s", resp.StatusCode, data)
	}
	got = listOrder()
	// px 组在前；wb 组内部顺序仍是上一步拖出来的 wb3 wb1 wb2。
	want = []string{"px1", "px2", "wb3", "wb1", "wb2"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("mode=group 后顺序 = %v, 期望 %v", got, want)
		}
	}

	// 3) 落库核对：同一组的 channel_position 相同，组内 key_position 连续。
	database, err := db.OpenForStore(st)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	rows, err := database.QueryContext(context.Background(),
		"SELECT name, channel_position, key_position FROM channels ORDER BY channel_position, key_position")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	var chanPos []int
	var keyPos []int
	for rows.Next() {
		var n string
		var cp, kp int
		if err := rows.Scan(&n, &cp, &kp); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
		chanPos = append(chanPos, cp)
		keyPos = append(keyPos, kp)
	}
	wantNames := []string{"px1", "px2", "wb3", "wb1", "wb2"}
	for i := range wantNames {
		if names[i] != wantNames[i] {
			t.Fatalf("库内顺序 = %v, 期望 %v", names, wantNames)
		}
	}
	if chanPos[0] != chanPos[1] || chanPos[2] != chanPos[3] || chanPos[3] != chanPos[4] {
		t.Fatalf("同组 channel_position 应相同: %v", chanPos)
	}
	if chanPos[1] >= chanPos[2] {
		t.Fatalf("px 组应排在 wb 组之前: %v", chanPos)
	}
	if keyPos[3] != 1 || keyPos[4] != 2 {
		t.Fatalf("wb 组 key_position 应为 0,1,2: %v", keyPos)
	}
}

// TestMoveKeyStaysInsideGroup 回归：/api/channels/{id}/move 只能在同渠道组内挪 Key。
//
// 旧实现按扁平数组 index±1 交换：当被移动的 Key 是组内最后一个时，「下移」会把
// 它换到下一个渠道组的第一个位置，等于把 Key 挪进了别的组（base_url 不变，
// 但组内顺序被打乱，且下一个组少了一个 Key 的位置）。
func TestMoveKeyStaysInsideGroup(t *testing.T) {
	ts, _, pw := newDBTestServer(t)
	cookie := login(t, ts, pw)
	add := func(name, base string) string {
		resp, data := apiReq(t, ts, http.MethodPost, "/api/channels", map[string]any{
			"name": name, "base_url": base, "enabled": true,
			"models": []string{"m"},
		}, cookie)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("创建 %s 失败: %d %s", name, resp.StatusCode, data)
		}
		var created types.Channel
		if err := json.Unmarshal(data, &created); err != nil {
			t.Fatalf("解析创建响应: %v", err)
		}
		return created.ID
	}
	wb1 := add("wb1", "http://wb/v1")
	wb2 := add("wb2", "http://wb/v1")
	px1 := add("px1", "http://px/v1")

	order := func() []string {
		_, data := apiReq(t, ts, http.MethodGet, "/api/channels", nil, cookie)
		var list []types.Channel
		if err := json.Unmarshal(data, &list); err != nil {
			t.Fatalf("解析渠道列表: %v", err)
		}
		out := make([]string, 0, len(list))
		for _, ch := range list {
			out = append(out, ch.Name)
		}
		return out
	}
	if got := order(); len(got) != 3 || got[0] != "wb1" || got[2] != "px1" {
		t.Fatalf("初始顺序异常: %v", got)
	}

	// wb2 是 wb 组最后一个：下移应失败（组内已到边界），不能跨到 px1 前面。
	resp, data := apiReq(t, ts, http.MethodPost, "/api/channels/"+wb2+"/move",
		map[string]any{"direction": "down"}, cookie)
	if resp.StatusCode == http.StatusOK {
		if got := order(); got[0] != "wb1" || got[1] != "wb2" || got[2] != "px1" {
			t.Fatalf("wb2 下移不应跨组, 实际 %v", got)
		}
	}
	_ = data

	// wb1 下移：应在组内与 wb2 交换。
	resp, _ = apiReq(t, ts, http.MethodPost, "/api/channels/"+wb1+"/move",
		map[string]any{"direction": "down"}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("wb1 下移期望 200, 实际 %d", resp.StatusCode)
	}
	got := order()
	want := []string{"wb2", "wb1", "px1"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("组内下移后顺序 = %v, 期望 %v", got, want)
		}
	}
	_ = px1
}
