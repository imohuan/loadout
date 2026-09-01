package multimodalmcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestInputFileBlockResponses 验证 input_file 块三种形态：file_id / file_url / base64 file_data。
func TestInputFileBlockResponses(t *testing.T) {
	// file_id（Files API 上传，推荐）。
	raw, _ := json.Marshal(inputFileBlockResponses("", "file-123", "a.pdf"))
	if string(raw) != `{"file_id":"file-123","type":"input_file"}` {
		t.Fatalf("inputFileBlock file_id = %s", raw)
	}
	// 公网 URL。
	raw2, _ := json.Marshal(inputFileBlockResponses("https://x.com/demo.pdf", "", "a.pdf"))
	if string(raw2) != `{"file_url":"https://x.com/demo.pdf","type":"input_file"}` {
		t.Fatalf("inputFileBlock url = %s", raw2)
	}
	// base64 data URI → file_data + filename。
	raw3, _ := json.Marshal(inputFileBlockResponses("data:application/pdf;base64,QUJD", "", "demo.pdf"))
	if string(raw3) != `{"file_data":"data:application/pdf;base64,QUJD","filename":"demo.pdf","type":"input_file"}` {
		t.Fatalf("inputFileBlock base64 = %s", raw3)
	}
	// filename 缺省时兜底 document.pdf。
	raw4, _ := json.Marshal(inputFileBlockResponses("data:application/pdf;base64,QUJD", "", ""))
	if !strings.Contains(string(raw4), `"filename":"document.pdf"`) {
		t.Fatalf("inputFileBlock 缺省 filename = %s", raw4)
	}
}

// TestUnderstandDocumentURL 验证：公网 URL 走 responses API，body 里 input_file.file_url。
func TestUnderstandDocumentURL(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Tools[3].Enabled = true
	cfg.Tools[3].Model = "doubao-seed-2-1-pro-260628"
	s := newConfigService(t, cfg)
	fw := s.gw.(*fakeRecogForwarder)
	fw.respBody = []byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"文档内容"}]}]}`)

	res, err := s.understandDocument(context.Background(), map[string]any{"document": "https://x.com/demo.pdf"})
	if err != nil {
		t.Fatalf("understandDocument url: %v", err)
	}
	if res == nil || !strings.Contains(res.Content[0].Text, "文档内容") {
		t.Fatalf("结果异常: %+v", res)
	}
	if fw.gotPath != "responses" {
		t.Fatalf("path = %q, want responses", fw.gotPath)
	}
	var body struct {
		Model  string `json:"model"`
		Input  []any  `json:"input"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(fw.gotBody, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Model != "doubao-seed-2-1-pro-260628" || body.Stream != false {
		t.Fatalf("model/stream = %q/%v", body.Model, body.Stream)
	}
	if len(body.Input) != 1 {
		t.Fatalf("input 消息数 = %d, want 1", len(body.Input))
	}
	rawBody := string(fw.gotBody)
	if !strings.Contains(rawBody, `"file_url":"https://x.com/demo.pdf"`) {
		t.Fatalf("body 应含 file_url，got: %s", rawBody)
	}
}

// TestUnderstandDocumentLocalBase64 验证：本地小文件 → base64 file_data + filename。
func TestUnderstandDocumentLocalBase64(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Tools[3].Enabled = true
	cfg.Tools[3].Model = "doubao-seed-2-1-pro-260628"
	s := newConfigService(t, cfg)
	fw := s.gw.(*fakeRecogForwarder)
	fw.respBody = []byte(`{"output":[{"content":[{"type":"output_text","text":"OK"}]}]}`)

	dir := t.TempDir()
	path := dir + "/report.pdf"
	content := []byte("%PDF-1.4 fake pdf body")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write pdf: %v", err)
	}
	if _, err := s.understandDocument(context.Background(), map[string]any{"document": "file://" + path}); err != nil {
		t.Fatalf("understandDocument local: %v", err)
	}
	wantData := "data:application/pdf;base64," + base64.StdEncoding.EncodeToString(content)
	rawBody := string(fw.gotBody)
	if !strings.Contains(rawBody, `"file_data":"`+wantData+`"`) {
		t.Fatalf("body 应含 base64 file_data，got: %s", rawBody)
	}
	if !strings.Contains(rawBody, `"filename":"report.pdf"`) {
		t.Fatalf("body 应含 filename report.pdf，got: %s", rawBody)
	}
}

// TestUnderstandDocumentMissingModel 验证：未配置文档模型时工具调用报错。
func TestUnderstandDocumentMissingModel(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	s := newConfigService(t, cfg)

	_, err := s.understandDocument(context.Background(), map[string]any{"document": "https://x.com/a.pdf"})
	if err == nil {
		t.Fatal("未配置文档模型时应报错")
	}
	if !strings.Contains(err.Error(), "未配置可用模型") {
		t.Errorf("错误应提及未配置模型，got: %v", err)
	}
}
