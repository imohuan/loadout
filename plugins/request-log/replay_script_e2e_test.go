package requestlog

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// 端到端：写一条带完整请求体的日志 → 调复现脚本接口 → 脚本落盘 → node --check 语法正确。
// 这是详情页「填入测试」按钮的真实调用路径（HTTP handler 全程），不是拼装函数单测。
func TestHandleReplayScriptEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	svc := testService(t, nil)
	body := "{\"model\":\"step-5-preview\",\"stream\":true,\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"tools\":[{\"type\":\"function\",\"function\":{\"name\":\"t\",\"parameters\":{\"type\":\"object\"}}}]}"
	id := "replaye2e000000000000000000000001"
	payload, err := json.Marshal(map[string]any{
		"method": "POST", "path": "chat/completions", "query": "",
		"headers": map[string]any{"Content-Type": "application/json"},
		"body":    body, "model": "step-5-preview", "stream": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeTestSnapshot(svc, id, "req-e2e", payload); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/request-logs/"+id+"/replay-script", nil)
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	svc.handleReplayScript(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK    bool   `json:"ok"`
		Path  string `json:"path"`
		Bytes int    `json:"bytes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Path == "" {
		t.Fatalf("bad response: %s", rec.Body.String())
	}
	if _, err := os.Stat(resp.Path); err != nil {
		t.Fatalf("script not written: %v", err)
	}
	if out, err := exec.Command("node", "--check", resp.Path).CombinedOutput(); err != nil {
		t.Fatalf("node --check failed: %v\n%s", err, out)
	}
	script, err := os.ReadFile(resp.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "step-5-preview") {
		t.Fatal("script should embed original body")
	}
	if len(script) != resp.Bytes {
		t.Fatalf("bytes mismatch: %d vs %d", len(script), resp.Bytes)
	}
}

// writeTestSnapshot 按线上相同的块存储结构写一条日志（envelope + 整块 body）。
func writeTestSnapshot(svc *Service, id, requestID string, payload []byte) error {
	var snap requestSnapshot
	if err := json.Unmarshal(payload, &snap); err != nil {
		return err
	}
	bodyHash, err := svc.blobs.putBlob([]byte(snap.Body))
	if err != nil {
		return err
	}
	envJSON, err := json.Marshal(requestEnvelope{
		Method: snap.Method, Path: snap.Path, Query: snap.Query,
		Headers: snap.Headers, Stream: snap.Stream,
	})
	if err != nil {
		return err
	}
	envHash, err := svc.blobs.putBlob(envJSON)
	if err != nil {
		return err
	}
	m := requestManifest{
		ID: id, RequestID: requestID, Model: snap.Model, Stream: snap.Stream,
		StartedAt: "2026-01-01T00:00:00Z", Result: "failed",
		ReqRaw: bodyHash, Envelope: envHash,
	}
	return svc.blobs.writeManifest(id, m)
}
