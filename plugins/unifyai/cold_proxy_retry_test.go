package unifyai

import (
	"log/slog"
	"strings"
	"testing"

	"loadout/core/procreg"
)

// TestQueryModelsRetriesOnColdProxy 验证「代理冷启动超时」时后端会退避重试一次。
//
// 回归背景（用户实际踩到）：CLI 请求 OpenCodex 代理只等 3 秒（config-loader.mjs 硬编码），
// 而代理冷启动实测要 10 秒以上 → 第一次查询必然超时，返回 degraded + 空模型列表，
// 于是页面「OpenCodex 模型」显示 0 个；紧接着点一下（或刷新元数据触发的重拉）
// 因为代理已热就正常了。后端在这里补一次重试，把这个冷启动窗口吃掉。
func TestQueryModelsRetriesOnColdProxy(t *testing.T) {
	restore := stubQueryModels(t,
		// 第一次：冷启动超时（degraded + 空列表）
		`{"models":{"degraded":true,"degradedReason":"OpenCodex 代理服务不可用","count":0,"models":[]}}`,
		// 第二次：代理已热，正常返回
		`{"models":{"degraded":false,"count":2,"models":[{"provider":"Loadout","modelId":"m1"},{"provider":"Loadout","modelId":"m2"}]}}`,
	)
	defer restore()

	res := NewService(slog.Default()).OpenCodexModels(false)
	if res.Degraded {
		t.Fatalf("重试后仍为 degraded：%+v", res)
	}
	if len(res.Models) != 2 {
		t.Fatalf("重试后模型数 = %d, want 2", len(res.Models))
	}
}

// TestQueryModelsNoRetryWhenProxyHealthy 代理正常时不重试（只调一次）。
func TestQueryModelsNoRetryWhenProxyHealthy(t *testing.T) {
	var calls int
	restore := stubQueryModelsCounting(t, &calls,
		`{"models":{"degraded":false,"count":1,"models":[{"provider":"Loadout","modelId":"m1"}]}}`,
	)
	defer restore()

	NewService(slog.Default()).OpenCodexModels(false)
	if calls != 1 {
		t.Errorf("代理正常时调用次数 = %d, want 1（不应重试）", calls)
	}
}

// TestListAllRetriesOnColdProxy 同款重试逻辑覆盖 --list all（页面初始化走这条）。
func TestListAllRetriesOnColdProxy(t *testing.T) {
	restore := stubQueryModels(t,
		`{"platforms":[],"models":{"degraded":true,"degradedReason":"代理不可用","count":0,"models":[]},"mcp":{"platforms":[]},"metadata":{}}`,
		`{"platforms":[],"models":{"degraded":false,"count":1,"models":[{"provider":"Loadout","modelId":"m1"}]},"mcp":{"platforms":[]},"metadata":{}}`,
	)
	defer restore()

	res, err := NewService(slog.Default()).ListAll(false)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if !strings.Contains(string(res.Models), `"m1"`) {
		t.Fatalf("重试后 --list all 仍未带回模型: %s", string(res.Models))
	}
}

// stubQueryModels 用一次性响应序列替换 CLI 执行（跑完序列后重复用最后一条）。
func stubQueryModels(t *testing.T, responses ...string) func() {
	t.Helper()
	return stubQueryModelsSeq(t, responses, nil)
}

func stubQueryModelsCounting(t *testing.T, calls *int, responses ...string) func() {
	t.Helper()
	return stubQueryModelsSeq(t, responses, calls)
}

func stubQueryModelsSeq(t *testing.T, responses []string, calls *int) func() {
	t.Helper()
	// 重试等待在测试里没必要真等。
	oldDelay := queryModelsRetryDelay
	queryModelsRetryDelay = 0
	t.Cleanup(func() { queryModelsRetryDelay = oldDelay })

	var i int
	restore := procreg.SetRunCollectFn(func(_ *procreg.Registry, name, kind, cmd string, args, env []string) ([]string, error) {
		if calls != nil {
			*calls++
		}
		idx := i
		if idx >= len(responses) {
			idx = len(responses) - 1
		}
		i++
		return []string{responses[idx]}, nil
	})
	t.Cleanup(func() { procreg.SetRunCollectFn(restore) })
	return func() { procreg.SetRunCollectFn(restore) }
}
