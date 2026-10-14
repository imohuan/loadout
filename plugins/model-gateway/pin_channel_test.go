package modelgateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"loadout/core/db"
	"loadout/plugins/contracts"
)

// pinHealth 假健康服务：指定渠道一律不可用，其余可用。
// 用来验证锁定通道能绕开熔断过滤，而未锁定时过滤照常生效。
type pinHealth struct {
	mockHealth
	unavailable map[string]bool
}

func (h *pinHealth) Check(ctx context.Context, channelID, model string) (contracts.Availability, error) {
	if h.unavailable[channelID] {
		return contracts.Availability{ManualEnabled: true, EffectiveAvailable: false, HealthStatus: "disabled"}, nil
	}
	return contracts.Availability{ManualEnabled: true, EffectiveAvailable: true, HealthStatus: "available"}, nil
}

// usePinRouting 接上真实 SQLite 仓储 + 可配置健康服务，让测试跑在生产路径上。
// 健康过滤只在 SQLite 路径生效（JSON 回退路径不做熔断判定），要测生产行为
// 就必须用真实数据库。
func usePinRouting(t *testing.T, svc *Service, health contracts.ModelHealth) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("db.OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := db.Migrate(context.Background(), database); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	svc.SetRoutingServices(database, health, nil)
}

// seedPinChannels 往 SQLite 里写两条渠道（a / b），都指向同一上游。
func seedPinChannels(t *testing.T, svc *Service, baseURL string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, c := range []struct{ id, name string }{{"a", "KeyA"}, {"b", "KeyB"}} {
		if _, err := svc.database.Exec(
			`INSERT INTO channels(id, name, base_url, manual_enabled, sync_billing, created_at, updated_at)
			 VALUES (?, ?, ?, 1, 1, ?, ?)`, c.id, c.name, baseURL, now, now); err != nil {
			t.Fatalf("插入渠道 %s 失败: %v", c.id, err)
		}
	}
}

// pinRequest 构造带测试锁定头的 chat 请求，来源为回环（内部调用方的真实形态）。
func pinRequest(body, channelID string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set(headerPinChannel, channelID)
	return req
}

// plainRequest 同上但不带锁定头（对照组）。
func plainRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	return req
}

// TestPinChannelRejectsNonLoopback 非回环来源不能借锁定头跳过熔断。
//
// 这个头能绕开熔断并把请求钉到任意渠道，若谁都能用，等于把内部约定变成了
// 对外接口 —— 持 sk- key 的外部调用方就能强制打某个账号。
func TestPinChannelRejectsNonLoopback(t *testing.T) {
	svc, _ := newTestService(t)
	echo, getRecords := newEchoServer(t, `{"choices":[{"message":{"content":"ok"}}]}`, 0, nil)
	defer echo.Close()
	// 两条渠道都熔断：只有锁定生效才可能成功，从而能确定锁定是否被采纳。
	usePinRouting(t, svc, &pinHealth{unavailable: map[string]bool{"a": true, "b": true}})
	seedPinChannels(t, svc, echo.URL)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m1","messages":[{"role":"user","content":"ping"}]}`))
	req.RemoteAddr = "203.0.113.7:54321" // 公网来源
	req.Header.Set(headerPinChannel, "b")
	rr := httptest.NewRecorder()
	svc.HandleProxy(rr, req)

	if rr.Code == http.StatusOK {
		t.Fatalf("非回环来源不应能借锁定头绕过熔断，实际返回 %d", rr.Code)
	}
	if n := len(getRecords()); n != 0 {
		t.Fatalf("非回环来源不应触达上游，实际 %d 次", n)
	}
}

// TestPinChannelLocksToOneChannel 锁定渠道后只打被指定的 Key，不再按顺序试第一个。
func TestPinChannelLocksToOneChannel(t *testing.T) {
	svc, _ := newTestService(t)
	echo, getRecords := newEchoServer(t, `{"choices":[{"message":{"content":"ok"}}]}`, 0, nil)
	defer echo.Close()
	usePinRouting(t, svc, &pinHealth{})
	// 渠道写入必须在 SetRoutingServices 之后：SQLite 仓储此时才生效。
	seedPinChannels(t, svc, echo.URL)

	body := `{"model":"m1","messages":[{"role":"user","content":"ping"}]}`
	req := pinRequest(body, "b")
	rr := httptest.NewRecorder()
	svc.HandleProxy(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d: %s", rr.Code, rr.Body.String())
	}
	// 锁定到 b 时不会先试 a，所以上游只应被调用一次。
	if n := len(getRecords()); n != 1 {
		t.Fatalf("锁定后上游应只被调用 1 次，实际 %d 次", n)
	}
}

// TestPinChannelBypassesHealthFilter 锁定的 Key 即使已被熔断也照常真实请求。
//
// 这是逐 Key 测试的关键：用户点「测试」往往正是想验一验那个「不可用」的 Key
// 到底还行不行，被健康过滤挡掉就得不到答案。
func TestPinChannelBypassesHealthFilter(t *testing.T) {
	svc, _ := newTestService(t)
	echo, getRecords := newEchoServer(t, `{"choices":[{"message":{"content":"ok"}}]}`, 0, nil)
	defer echo.Close()
	// 两条渠道都被熔断：不锁定时无候选可用（对照组），锁定 b 时仍要真实请求。
	usePinRouting(t, svc, &pinHealth{unavailable: map[string]bool{"a": true, "b": true}})
	seedPinChannels(t, svc, echo.URL)

	body := `{"model":"m1","messages":[{"role":"user","content":"ping"}]}`
	req := pinRequest(body, "b")
	rr := httptest.NewRecorder()
	svc.HandleProxy(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("锁定的不可用 Key 应被真实请求，期望 200，实际 %d: %s", rr.Code, rr.Body.String())
	}
	if n := len(getRecords()); n != 1 {
		t.Fatalf("期望上游收到 1 次请求，实际 %d 次", n)
	}

	// 对照：不锁定时熔断过滤照常生效，没有可用候选 → 非 200。
	req2 := plainRequest(body)
	rr2 := httptest.NewRecorder()
	svc.HandleProxy(rr2, req2)
	if rr2.Code == http.StatusOK {
		t.Fatalf("未锁定时不应绕过熔断，实际返回 %d", rr2.Code)
	}
}
