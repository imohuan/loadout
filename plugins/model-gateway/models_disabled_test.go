package modelgateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"loadout/plugins/contracts"
	"loadout/plugins/types"
)

// mockHealth 可配置 List 返回的模型状态（其余方法零值）。
type mockHealth struct {
	statuses []contracts.ChannelStatus
}

func (m *mockHealth) Check(ctx context.Context, channelID, model string) (contracts.Availability, error) {
	return contracts.Availability{ManualEnabled: true, EffectiveAvailable: true, HealthStatus: "available"}, nil
}
func (m *mockHealth) RecordSuccess(ctx context.Context, channelID, model string) error { return nil }
func (m *mockHealth) RecordFailure(ctx context.Context, f contracts.RouteFailure) (string, error) {
	return "", nil
}
func (m *mockHealth) SetChannelEnabled(ctx context.Context, channelID string, enabled bool) error {
	return nil
}
func (m *mockHealth) SetModelEnabled(ctx context.Context, channelID, model string, enabled bool) error {
	return nil
}
func (m *mockHealth) SetModelsEnabled(ctx context.Context, channelID string, models []string, enabled bool) error {
	return nil
}
func (m *mockHealth) DeleteModel(ctx context.Context, channelID, model string) error { return nil }
func (m *mockHealth) DeleteModels(ctx context.Context, channelID string, models []string) error {
	return nil
}
func (m *mockHealth) RecoverChannel(ctx context.Context, channelID string) error { return nil }
func (m *mockHealth) RecoverModel(ctx context.Context, channelID, model string) error {
	return nil
}
func (m *mockHealth) RecoverModels(ctx context.Context, channelID string, models []string) error {
	return nil
}
func (m *mockHealth) RecoverAllModels(ctx context.Context) (int64, error) { return 0, nil }
func (m *mockHealth) RecoverAllModelsByChannel(ctx context.Context, channelID string) (int64, error) {
	return 0, nil
}
func (m *mockHealth) RecoverAllChannels(ctx context.Context) (int64, error) { return 0, nil }
func (m *mockHealth) List(ctx context.Context) ([]contracts.ChannelStatus, error) {
	return m.statuses, nil
}
func (m *mockHealth) CheckNow(ctx context.Context, force bool) error { return nil }

// TestHandleModelsDisabledFilter model-status 配置的禁用模型从 /v1/models 剔除。
func TestHandleModelsDisabledFilter(t *testing.T) {
	svc, st := newTestService(t)
	if err := st.Write(types.FileChannels, []types.Channel{
		{ID: "c1", Name: "渠道1", BaseURL: "http://u/v1", Enabled: true, Models: []string{"m1", "m2", "m3"}},
	}); err != nil {
		t.Fatalf("写渠道表失败: %v", err)
	}
	// c1 上 m1 被禁用（model-status 配置），m2/m3 启用。
	svc.SetRoutingServices(nil, &mockHealth{statuses: []contracts.ChannelStatus{
		{ID: "c1", ManualEnabled: true, Models: []contracts.ModelStatus{
			{Model: "m1", ManualEnabled: false, Health: contracts.Availability{ManualEnabled: false, EffectiveAvailable: false}},
			{Model: "m2", ManualEnabled: true, Health: contracts.Availability{ManualEnabled: true, EffectiveAvailable: true}},
		}},
	}}, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	svc.HandleModels(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码应为 200，实际 %d", rec.Code)
	}
	ids := parseModelIDs(t, rec)
	if ids["m1"] {
		t.Fatalf("被禁用的 m1 不应出现在列表: %+v", ids)
	}
	if !ids["m2"] || !ids["m3"] {
		t.Fatalf("启用的 m2/m3 应保留: %+v", ids)
	}
}

// TestHandleModelsDisabledCrossChannel 同名模型跨渠道：一个渠道禁用、另一个启用 → 保留。
func TestHandleModelsDisabledCrossChannel(t *testing.T) {
	svc, st := newTestService(t)
	if err := st.Write(types.FileChannels, []types.Channel{
		{ID: "c1", Name: "渠道1", BaseURL: "http://u/v1", Enabled: true, Models: []string{"shared"}},
		{ID: "c2", Name: "渠道2", BaseURL: "http://u/v1", Enabled: true, Models: []string{"shared"}},
	}); err != nil {
		t.Fatalf("写渠道表失败: %v", err)
	}
	// c1 禁用 shared，c2 启用 shared → shared 应保留（至少一个渠道可用）。
	svc.SetRoutingServices(nil, &mockHealth{statuses: []contracts.ChannelStatus{
		{ID: "c1", ManualEnabled: true, Models: []contracts.ModelStatus{
			{Model: "shared", ManualEnabled: false, Health: contracts.Availability{ManualEnabled: false, EffectiveAvailable: false}},
		}},
		{ID: "c2", ManualEnabled: true, Models: []contracts.ModelStatus{
			{Model: "shared", ManualEnabled: true, Health: contracts.Availability{ManualEnabled: true, EffectiveAvailable: true}},
		}},
	}}, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	svc.HandleModels(rec, req)
	ids := parseModelIDs(t, rec)
	if !ids["shared"] {
		t.Fatalf("跨渠道仍有可用渠道时 shared 应保留: %+v", ids)
	}
}

func parseModelIDs(t *testing.T, rec *httptest.ResponseRecorder) map[string]bool {
	t.Helper()
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	ids := map[string]bool{}
	for _, d := range parsed.Data {
		ids[d.ID] = true
	}
	return ids
}
