package plugins

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"loadout/core/db"
	"loadout/core/plugin"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// TestAggregateFailoverWhenVolcQuotaExhausted 端到端回归（用户报告的场景）：
// 聚合（轮询）模型的第一个目标（火山 doubao）免费额度已耗尽，第二个目标在别的平台。
//
// 期望的正确行为：额度插件只把「火山账号 + doubao」这一次尝试预测为失败，网关落
// 一条 failed attempt 后照常切换下一个目标，第二个模型成功返回 200。
//
// 关键断言是「火山上游一次都没被请求」：这是「预测」与「真打一次再失败」的区别，
// 也是用户说的「我这边的逻辑只是直接预测这个模型会失败，并直接返回它的失败状态」。
// 同时断言第二次尝试（第二个模型）仍然走完整转发，说明切模型没有受影响。
func TestAggregateFailoverWhenVolcQuotaExhausted(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(t.TempDir() + "/loadout.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	var mu sync.Mutex
	var hitVolc, hitSecond bool
	volcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hitVolc = true
		mu.Unlock()
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"quota exhausted"}}`))
	}))
	defer volcSrv.Close()
	secondSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hitSecond = true
		mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		w.Header().Set("Content-Type", "application/json")
		out, _ := json.Marshal(map[string]any{
			"id": "chatcmpl-1", "object": "chat.completion", "model": payload["model"],
			"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
		_, _ = w.Write(out)
	}))
	defer secondSrv.Close()

	if err := seedE2EChannels(database, volcSrv.URL, secondSrv.URL); err != nil {
		t.Fatal(err)
	}
	writeE2EAggregate(t, database, "auto", []types.AggregateTarget{
		{Model: "doubao-1-5-pro-32k-250115", ChannelID: "ch-volc"},
		{Model: "glm-5.2", ChannelID: "ch-other"},
	})
	seedE2EExhaustedVolcQuota(t, database)

	logBuf := &syncBuffer{}
	lg := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	asm, err := plugin.Load(All(), plugin.Options{
		Logger: lg,
		Services: map[string]any{
			"store": st, "logger": lg, "http-client": &http.Client{}, "db": database,
		},
	})
	if err != nil {
		t.Fatalf("assembly failed: %v", err)
	}
	defer asm.Unload()
	gw, ok := asm.Get("model-gateway").(*modelgateway.Service)
	if !ok || gw == nil {
		t.Fatal("model-gateway 服务未装配")
	}

	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	gw.HandleProxy(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("应切换第二个模型并成功，实际 %d: %s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	sawVolc, sawSecond := hitVolc, hitSecond
	mu.Unlock()
	if sawVolc {
		t.Fatal("额度已预测耗尽时不应再真打火山上游（应直接返回失败状态并切下一个模型）")
	}
	if !sawSecond {
		t.Fatal("第二个模型的渠道没有被调用，轮询没有发生")
	}

	// 路由日志要能看出「第一个模型被预测失败、随后切换」。
	rows, err := database.Query(`SELECT model, result, failure_class, error_message FROM route_attempts ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var firstFailed, secondOK bool
	for rows.Next() {
		var model, result, class, msg string
		if err := rows.Scan(&model, &result, &class, &msg); err != nil {
			t.Fatal(err)
		}
		switch {
		case model == "doubao-1-5-pro-32k-250115" && result == "failed" && class == "free_quota_exhausted":
			firstFailed = true
		case model == "glm-5.2" && result == "success":
			secondOK = true
		}
	}
	if !firstFailed {
		t.Fatal("应记录一条「doubao 因免费额度预测失败」的 attempt，日志里能看到切换原因")
	}
	if !secondOK {
		t.Fatal("第二个模型应留下成功的 attempt 记录")
	}
	if !strings.Contains(logBuf.String(), "预测该渠道模型额度已用完") {
		t.Fatalf("应能看到额度插件的预测日志（证明是预测失败在起作用），实际日志:\n%s", logBuf.String())
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func seedE2EChannels(database *sql.DB, volcURL, otherURL string) error {
	if _, err := database.Exec(`INSERT INTO channels(id, name, channel_name, base_url, manual_enabled, created_at, updated_at)
		VALUES ('ch-volc', 'volc', 'ark', ?, 1, 'now', 'now')`, volcURL); err != nil {
		return err
	}
	if _, err := database.Exec(`INSERT INTO channels(id, name, channel_name, base_url, manual_enabled, created_at, updated_at)
		VALUES ('ch-other', 'other', 'wb', ?, 1, 'now', 'now')`, otherURL); err != nil {
		return err
	}
	for _, row := range []struct{ chID, model string }{
		{"ch-volc", "doubao-1-5-pro-32k-250115"},
		{"ch-other", "glm-5.2"},
	} {
		if _, err := database.Exec(`INSERT INTO channel_models(channel_id, model, source, enabled, first_seen_at, last_seen_at)
			VALUES (?, ?, 'probe', 1, 'now', 'now')`, row.chID, row.model); err != nil {
			return err
		}
	}
	return nil
}

func writeE2EAggregate(t *testing.T, database *sql.DB, name string, targets []types.AggregateTarget) {
	t.Helper()
	res, err := database.Exec(`INSERT INTO aggregates(name, enabled, created_at, updated_at) VALUES (?, 1, 'now', 'now')`, name)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	for i, target := range targets {
		idsJSON, _ := json.Marshal(target.ChannelIDs)
		if _, err := database.Exec(`INSERT INTO aggregate_targets(aggregate_id, position, model, channel_id, channel_ids_json, channel_base_url)
			VALUES (?, ?, ?, ?, ?, ?)`, id, i, target.Model, target.ChannelID, string(idsJSON), target.ChannelBaseURL); err != nil {
			t.Fatal(err)
		}
	}
}

// seedE2EExhaustedVolcQuota 造一条火山额度配置与「已耗尽」的资源包。
// 账号指纹必须与插件运行时算的一致（SHA256(access_key)[:16]），否则归属检查会放行。
func seedE2EExhaustedVolcQuota(t *testing.T, database *sql.DB) {
	t.Helper()
	const ak = "AK-e2e"
	sum := sha256.Sum256([]byte(ak))
	aid := hex.EncodeToString(sum[:])[:16]
	if _, err := database.Exec(`INSERT INTO volc_quota_config(channel_id, access_key, account_id, secret_key_cipher, enabled, force_block, updated_at)
		VALUES ('ch-volc', ?, ?, '', 1, 1, 'now')`, ak, aid); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO volc_quota_packages(account_id, instance_no, product, product_name, configuration_code,
	       configuration_name, model, total_amount, available_amount, used_amount, initial_total, local_remaining, unit, status, synced_at)
		VALUES (?, 'inst-e2e', 'ark_bd', 'Doubao-pro-32k', 'Doubao_Pro_32k_data_collaboration', 'pack', 'doubao-pro-32k',
		        2000000, 0, 2000000, 2000000, 0, 'Tokens', 'UsedUp', 'now')`, aid); err != nil {
		t.Fatal(err)
	}
}
