package requestlog

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 回归：块被 GC 误删后，Detail 不再静默返回空 body，而是带 body_error 说明。
// 真实事故：manifest 引用 20 个消息块，其中一个被误删，详情页 body 显示为 ""
// （用户以为请求本来就没 body），实际是拼装失败被吞错。
func TestDetailMissingBlobReportsBodyError(t *testing.T) {
	dir := t.TempDir()
	bs, err := newBlobStore(filepath.Join(dir, "request-logs"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bs.putBlob([]byte(`{"role":"user","content":"hi"}`)); err != nil {
		t.Fatal(err)
	}
	m := requestManifest{
		ID:        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RequestID: "req-1",
		Model:     "gpt-x",
		StartedAt: "2026-01-01T00:00:00Z",
		Result:    "failed",
		Messages:  []string{"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"}, // 不存在的块
	}
	if err := bs.writeManifest(m.ID, m); err != nil {
		t.Fatal(err)
	}
	s := &Service{blobs: bs}
	d, err := s.Detail(context.Background(), m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.BodyError == "" {
		t.Fatal("expected body_error to be set when a message blob is missing")
	}
	var snap requestSnapshot
	if err := json.Unmarshal(d.RequestJSON, &snap); err != nil {
		t.Fatal(err)
	}
	// 缺失块用占位符顶上，其余消息照常还原——不再整条报废。
	if snap.Body == "" {
		t.Fatal("body should contain placeholder, not be empty")
	}
	if !strings.Contains(snap.Body, "消息块缺失") {
		t.Fatalf("body should contain missing-block placeholder, got: %s", snap.Body)
	}
	if strings.Count(snap.Body, "\"role\":\"missing\"") != 1 && !strings.Contains(snap.Body, "\"missing\"") {
		t.Fatalf("placeholder message should be present, got: %s", snap.Body)
	}
	_ = os.RemoveAll(dir)
}
