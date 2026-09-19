package unifyai

import (
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"loadout/core/procreg"
)

// fastStub 用一段假 CLI 输出来替换真实执行，并记录每次调用的参数。
// 返回调用次数与「最后一次参数」的读取函数（解构用 `_, calls, argsOf :=`）。
type runCollectFn = func(r *procreg.Registry, name, kind, cmd string, args, env []string) ([]string, error)

func fastStub(t *testing.T, output string) (restore func(), calls func() int, argsOf func() []string) {
	t.Helper()
	var callsN int64
	var last []string
	old := procreg.SetRunCollectFn(func(_ *procreg.Registry, name, kind, cmd string, args, env []string) ([]string, error) {
		atomic.AddInt64(&callsN, 1)
		last = append([]string(nil), args...)
		return []string{output}, nil
	})
	restore = func() { procreg.SetRunCollectFn(old) }
	t.Cleanup(restore)
	return restore, func() int { return int(atomic.LoadInt64(&callsN)) }, func() []string { return last }
}

// TestListAllFastSkipsModelsQuery 锁定「首屏查询不碰模型列表」这条性能约束。
//
// 回归背景（用户实际反馈）：每次进入 UnifyAI 页面都要卡约 10 秒。
// 根因是首屏用 `--list all` 一次拉全，其中 models 必须去连 OpenCodex 代理
// （localhost:10100），而该代理冷启动实测 10 秒以上（热态还只维持约 3 秒）；
// 相比之下 platforms + mcp + metadata 合计只要约 1.3 秒。
// 首屏「能不能操作」只取决于平台与 MCP 矩阵，模型列表纯展示，
// 因此快速路径必须完全不查 models，否则每次进页面都要再等一次冷启动。
func TestListAllFastSkipsModelsQuery(t *testing.T) {
	_, _, argsOf := fastStub(t, `{"platforms":[{"id":"opencode","name":"OpenCode"}],`+
		`"mcp":{"platforms":[{"platform":"opencode","readable":true}]},`+
		`"metadata":{"path":"/tmp/c.json","modelCount":3}}`)

	res := NewService(slog.Default()).ListAllFast(false)

	got := argsOf()
	joined := strings.Join(got, " ")
	if strings.Contains(joined, "models") {
		t.Errorf("首屏查询不应包含 models（会连慢代理导致 10 秒）: %v", got)
	}
	for _, want := range []string{"platforms", "mcp", "metadata"} {
		if !strings.Contains(joined, want) {
			t.Errorf("首屏查询应包含 %s: %v", want, got)
		}
	}
	if strings.Contains(joined, "all") {
		t.Errorf("首屏查询不应再用 all（等于又把 models 拉上）: %v", got)
	}

	// 快速路径仍要把首屏需要的数据填齐
	if len(res.Platforms) != 1 || res.Platforms[0].ID != "opencode" {
		t.Errorf("平台未解析: %+v", res.Platforms)
	}
	if len(res.Mcp.Platforms) != 1 {
		t.Errorf("MCP 矩阵未解析: %+v", res.Mcp)
	}
	if !strings.Contains(string(res.Metadata), `"modelCount":3`) {
		t.Errorf("元数据状态未解析: %s", string(res.Metadata))
	}
}

// TestListAllFastDoesNotWarmProxy 首屏路径不应触发「预热代理」——
// 预热本身就要等代理冷启动（实测 8 秒以上），正是要绕开的那一下。
func TestListAllFastDoesNotWarmProxy(t *testing.T) {
	// 把 sync.json 指到临时目录，确保预热逻辑即使被调用也读不到代理地址。
	t.Setenv("OPENCODEX_API_AUTH_TOKEN", "")
	_, calls, _ := fastStub(t, `{"platforms":[],"mcp":{"platforms":[]},"metadata":{}}`)

	start := time.Now()
	NewService(slog.Default()).ListAllFast(false)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("首屏路径耗时 %v，疑似仍在等代理预热", elapsed)
	}
	if n := calls(); n != 1 {
		t.Errorf("CLI 调用次数 = %d, want 1", n)
	}
}

// TestListAllFastCachesWithinTTL 页面来回切换时命中缓存直接返回，不再重跑 CLI。
func TestListAllFastCachesWithinTTL(t *testing.T) {
	oldTTL := listAllFastTTL
	listAllFastTTL = time.Minute
	t.Cleanup(func() { listAllFastTTL = oldTTL })

	_, calls, _ := fastStub(t, `{"platforms":[{"id":"opencode"}],"mcp":{"platforms":[]},"metadata":{}}`)
	svc := NewService(slog.Default())

	svc.ListAllFast(false)
	svc.ListAllFast(false)
	svc.ListAllFast(false)
	if n := calls(); n != 1 {
		t.Errorf("TTL 内 CLI 调用次数 = %d, want 1（应命中缓存）", n)
	}
}

// TestListAllFastFreshBypassesCache 状态变更后（例如导入 MCP）可以强制穿透缓存，
// 否则刚改完的配置会被 15 秒缓存挡住，页面显示旧数据。
func TestListAllFastFreshBypassesCache(t *testing.T) {
	oldTTL := listAllFastTTL
	listAllFastTTL = time.Minute
	t.Cleanup(func() { listAllFastTTL = oldTTL })

	_, calls, _ := fastStub(t, `{"platforms":[],"mcp":{"platforms":[]},"metadata":{}}`)
	svc := NewService(slog.Default())

	svc.ListAllFast(false)
	svc.ListAllFast(true)
	if n := calls(); n != 2 {
		t.Errorf("fresh 穿透后 CLI 调用次数 = %d, want 2", n)
	}
}
