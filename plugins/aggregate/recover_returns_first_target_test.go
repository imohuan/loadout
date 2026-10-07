package aggregate

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"loadout/core/db"
	"loadout/core/store"
	"loadout/plugins/contracts"
	modelhealth "loadout/plugins/model-health"
	"loadout/plugins/types"
)

// TestRecoverReturnsToFirstTarget 复现用户报告的场景：
// 虚拟模型三个目标（前两个挂掉，第三个正常）。用户点「恢复」后，
// 路由必须重新检查前两个目标、并回到第一个可用的目标——
// 而不是继续跳过它们走第三个。
//
// 这里覆盖两种「挂掉」：
//   - 429 限速 → 模型级 cooling；
//   - 401 密钥失效 → Key 级 disabled（账号级）。
func TestRecoverReturnsToFirstTarget(t *testing.T) {
	cases := []struct {
		name    string
		failure contracts.RouteFailure
		// 恢复动作：模拟用户点击。
		recover func(context.Context, *modelhealth.Service) error
	}{
		{
			name:    "429 模型级冷却 + 恢复 Key",
			failure: contracts.RouteFailure{ChannelID: "k1", Model: "m1", StatusCode: 429, Error: "rate limit"},
			recover: func(ctx context.Context, h *modelhealth.Service) error { return h.RecoverChannel(ctx, "k1") },
		},
		{
			name:    "429 模型级冷却 + 恢复本平台",
			failure: contracts.RouteFailure{ChannelID: "k1", Model: "m1", StatusCode: 429, Error: "rate limit"},
			recover: func(ctx context.Context, h *modelhealth.Service) error {
				_, err := h.RecoverPlatformByBaseURL(ctx, "https://plat.example/v1")
				return err
			},
		},
		{
			name:    "401 账号级禁 Key + 恢复 Key",
			failure: contracts.RouteFailure{ChannelID: "k1", Model: "m1", StatusCode: 401, Error: "invalid api key"},
			recover: func(ctx context.Context, h *modelhealth.Service) error { return h.RecoverChannel(ctx, "k1") },
		},
		{
			name:    "401 账号级禁 Key + 恢复本平台",
			failure: contracts.RouteFailure{ChannelID: "k1", Model: "m1", StatusCode: 401, Error: "invalid api key"},
			recover: func(ctx context.Context, h *modelhealth.Service) error {
				_, err := h.RecoverPlatformByBaseURL(ctx, "https://plat.example/v1")
				return err
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, err := store.New(t.TempDir())
			if err != nil {
				t.Fatalf("store: %v", err)
			}
			database, err := db.OpenMemory()
			if err != nil {
				t.Fatalf("db: %v", err)
			}
			defer database.Close()
			repo, err := db.NewRepository(database)
			if err != nil {
				t.Fatalf("repo: %v", err)
			}
			ctx := context.Background()
			if err := repo.ReplaceChannels(ctx, []db.Channel{
				{ID: "k1", Name: "Key1", ChannelName: "plat", BaseURL: "https://plat.example/v1", ManualEnabled: true},
				{ID: "k2", Name: "Key2", ChannelName: "other", BaseURL: "https://other.example/v1", ManualEnabled: true},
			}); err != nil {
				t.Fatalf("channels: %v", err)
			}
			lg := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
			health := modelhealth.NewService(database, lg)
			svc := NewService(st, lg, nil)
			svc.SetRoutingServices(database, health)

			// 三个目标：前两个在 k1（同平台），第三个在 k2。
			targets := []types.AggregateTarget{
				{Model: "m1", ChannelBaseURL: "https://plat.example/v1"},
				{Model: "m2", ChannelBaseURL: "https://plat.example/v1"},
				{Model: "m3", ChannelBaseURL: "https://other.example/v1"},
			}

			// 前两个目标挂掉。
			if _, err := health.RecordFailure(ctx, tc.failure); err != nil {
				t.Fatalf("record failure: %v", err)
			}
			if _, err := health.RecordFailure(ctx, contracts.RouteFailure{ChannelID: "k1", Model: "m2", StatusCode: tc.failure.StatusCode, Error: tc.failure.Error}); err != nil {
				t.Fatalf("record failure m2: %v", err)
			}

			sel, _, err := svc.selectAvailableTarget(targets, nil)
			if err != nil {
				t.Fatal(err)
			}
			if sel == nil || sel.Model != "m3" {
				t.Fatalf("挂掉后应先落到 m3，实际 %+v", sel)
			}

			// 用户点恢复。
			if err := tc.recover(ctx, health); err != nil {
				t.Fatalf("recover: %v", err)
			}

			// 恢复后必须重新检查并回到第一个目标 m1。
			sel, _, err = svc.selectAvailableTarget(targets, nil)
			if err != nil {
				t.Fatal(err)
			}
			if sel == nil || sel.Model != "m1" {
				t.Fatalf("恢复后应重新选中 m1，实际 %+v", sel)
			}
		})
	}
}
