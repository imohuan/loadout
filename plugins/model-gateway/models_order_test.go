package modelgateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"loadout/plugins/types"
)

// TestHandleModelsKeepsChannelOrder 回归：/v1/models 的模型顺序必须稳定，
// 按渠道 position 顺序、同渠道内按模型目录顺序排列，同名模型保留首次出现的位置。
//
// 旧实现先建 map 再 range 输出，Go map 迭代顺序随机 —— 客户端（opencodex 等）
// 每次拉到的列表顺序都不一样，用户根本无法用列表顺序表达偏好。
func TestHandleModelsKeepsChannelOrder(t *testing.T) {
	svc, st := newTestService(t)
	if err := st.Write(types.FileChannels, []types.Channel{
		{ID: "c1", Name: "渠道1", BaseURL: "http://a/v1", Enabled: true, Models: []string{"m3", "m1"}},
		{ID: "c2", Name: "渠道2", BaseURL: "http://b/v1", Enabled: true, Models: []string{"m2", "m1", "m4"}},
	}); err != nil {
		t.Fatalf("写渠道表失败: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	svc.HandleModels(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码应为 200，实际 %d", rec.Code)
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	want := []string{"m3", "m1", "m2", "m4"}
	if len(body.Data) != len(want) {
		t.Fatalf("模型数 = %d，期望 %d: %s", len(body.Data), len(want), rec.Body.String())
	}
	for i, id := range want {
		if body.Data[i].ID != id {
			t.Fatalf("第 %d 个模型 = %q，期望 %q（完整顺序: %s）", i, body.Data[i].ID, id, rec.Body.String())
		}
	}
}

// TestHandleModelsV2KeepsChannelOrder v2 列表同样必须稳定：
// 展示名按渠道 position、渠道内模型目录顺序首次出现排列。
func TestHandleModelsV2KeepsChannelOrder(t *testing.T) {
	svc, st := newTestService(t)
	if err := st.Write(types.FileChannels, []types.Channel{
		{ID: "c1", Name: "渠道1", ChannelName: "甲", BaseURL: "http://a/v1", Enabled: true, Models: []string{"m2", "m1"}},
		{ID: "c2", Name: "渠道2", ChannelName: "乙", BaseURL: "http://b/v1", Enabled: true, Models: []string{"m1"}},
	}); err != nil {
		t.Fatalf("写渠道表失败: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v2/models", nil)
	rec := httptest.NewRecorder()
	svc.HandleModelsV2(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码应为 200，实际 %d", rec.Code)
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	want := []string{"甲/m2", "甲/m1", "乙/m1"}
	if len(body.Data) != len(want) {
		t.Fatalf("模型数 = %d，期望 %d: %s", len(body.Data), len(want), rec.Body.String())
	}
	for i, id := range want {
		if body.Data[i].ID != id {
			t.Fatalf("第 %d 个模型 = %q，期望 %q（完整顺序: %s）", i, body.Data[i].ID, id, rec.Body.String())
		}
	}
}
