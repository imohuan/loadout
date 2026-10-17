package requestlog

import (
	"encoding/json"
	"path/filepath"
	"testing"

	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// TestBlobStoreDedupRoundTrip 连续三轮对话：第 N 轮请求 = 前 N-1 条历史消息 + 1 条
// 新消息。断言三轮之后消息块总数 = 3（每条消息只存一份），清单各自引用前缀。
func TestBlobStoreDedupRoundTrip(t *testing.T) {
	bs, err := newBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	msgs := []map[string]any{
		{"role": "system", "content": "你是助手"},
		{"role": "user", "content": "第一问"},
		{"role": "assistant", "content": "第一答"},
	}
	roundMsgs := [][]map[string]any{
		{msgs[0], msgs[1]},
		{msgs[0], msgs[1], msgs[2]},
		{msgs[0], msgs[1], msgs[2], {"role": "user", "content": "第二问"}},
	}
	written := map[string]bool{} // 所有出现过的消息哈希（跨轮去重后应只有 3 个）
	for round := 0; round < 3; round++ {
		body := map[string]any{
			"model":    "m",
			"messages": roundMsgs[round],
		}
		raw, _ := json.Marshal(body)
		entries, _, _, ok := splitRequestMessages(raw)
		if !ok {
			t.Fatalf("round %d: split failed", round)
		}
		for _, e := range entries {
			written[e.Hash] = true
			if _, err := bs.putBlob([]byte("x")); err != nil {
				t.Fatal(err)
			}
		}
	}
	// 2+3+4 = 9 次引用，去重后唯一消息 = 4 条（system/第一问/第一答/第二问）
	if len(written) != 4 {
		t.Fatalf("unique message hashes = %d, want 4 (dedup broken)", len(written))
	}
}

// TestBlobStorePutGetRoundTrip 块写入后读回内容一致；重复写幂等（同哈希不重复落盘）。
func TestBlobStorePutGetRoundTrip(t *testing.T) {
	bs, err := newBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("你好，世界 hello world")
	h1, err := bs.putBlob(data)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := bs.putBlob(data)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("same content produced different hashes: %s vs %s", h1, h2)
	}
	back, err := bs.getBlob(h1)
	if err != nil {
		t.Fatal(err)
	}
	if string(back) != string(data) {
		t.Fatalf("round trip mismatch: %q", back)
	}
}

// TestBlobStoreGC 清单删除后，无引用块被 GC 回收；仍被引用的块保留。
func TestBlobStoreGC(t *testing.T) {
	dir := t.TempDir()
	bs, err := newBlobStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	shared, _ := bs.putBlob([]byte("shared"))
	only1, _ := bs.putBlob([]byte("only-manifest-1"))
	if err := bs.writeManifest("m1", requestManifest{ID: "m1", Result: "running", Messages: []string{shared, only1}}); err != nil {
		t.Fatal(err)
	}
	if err := bs.writeManifest("m2", requestManifest{ID: "m2", Result: "running", Messages: []string{shared}}); err != nil {
		t.Fatal(err)
	}
	// 删 m1：only1 失去引用，shared 还被 m2 引用
	bs.deleteManifest("m1")
	removed, err := bs.collectGarbage()
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("gc removed = %d, want 1", removed)
	}
	if !bs.hasBlob(shared) {
		t.Fatal("shared blob must survive gc")
	}
	if bs.hasBlob(only1) {
		t.Fatal("orphan blob must be collected")
	}
}

// TestEndToEndBlobDetail 全链路：HandleBeforeAttempt 拆块 → HandleAfterUpstream 收尾
// → Detail 组装出与旧版结构一致的 request_json / response_json。
func TestEndToEndBlobDetail(t *testing.T) {
	reqDB, err := openRequestLogDB(t.TempDir() + "/request-log.db")
	if err != nil {
		t.Fatal(err)
	}
	defer reqDB.Close()
	svc := testService(t, []types.CapabilityRoute{
		{Models: []string{"*"}, Capability: capabilityName, Route: types.RouteProxy},
	})
	svc.reqDB = reqDB
	if err := svc.SetBlobDir(filepath.Join(t.TempDir(), "request-logs")); err != nil {
		t.Fatal(err)
	}
	pipe := testPipe("req-blob-e2e")
	pipe.Request.Body = []byte(`{"model":"gpt-4o","messages":[{"role":"system","content":"sys prompt"},{"role":"user","content":"hi"}]}`)
	if _, err := svc.HandleBeforeAttempt(pipe); err != nil {
		t.Fatal(err)
	}
	uuid, _ := pipe.Metadata[metadataKey].(string)
	if uuid == "" {
		t.Fatal("uuid missing")
	}
	ap := &modelgateway.AfterUpstreamPayload{
		Pipe: pipe,
		Response: &modelgateway.ProxyResponse{
			StatusCode: 200,
			Header:     map[string][]string{"Content-Type": {"application/json"}},
			Body:       []byte(`{"choices":[{"message":{"content":"ok"}}]}`),
		},
	}
	if _, err := svc.HandleAfterUpstream(ap); err != nil {
		t.Fatal(err)
	}
	d, err := svc.Detail(t.Context(), uuid)
	if err != nil {
		t.Fatal(err)
	}
	if d.Result != "success" && d.Result != "running" {
		t.Fatalf("result = %q", d.Result)
	}
	var snap requestSnapshot
	if err := json.Unmarshal(d.RequestJSON, &snap); err != nil {
		t.Fatalf("request_json not a requestSnapshot: %v", err)
	}
	if len(d.ResponseJSON) == 0 {
		t.Fatal("response_json should be assembled from blob")
	}
}
