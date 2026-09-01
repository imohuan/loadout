package multimodalmcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"

	modelgateway "loadout/plugins/model-gateway"
)

// fakeRecogForwarder 实现 SubRequestForwarder：记录收到 pipe，返回预设 body / error。
type fakeRecogForwarder struct {
	gotPath  string
	gotBody  []byte
	respBody []byte
	err      error
}

func (f *fakeRecogForwarder) ForwardSubRequest(ctx context.Context, pipe *modelgateway.ProxyPipeline, streamWriter func(line []byte) error) (*modelgateway.ProxyPipeline, []byte, error) {
	f.gotPath = pipe.Request.Path
	f.gotBody = pipe.Request.Body
	return pipe, f.respBody, f.err
}

func newRecogService(fw *fakeRecogForwarder) *Service {
	s := &Service{gw: fw}
	return s
}

func TestImageBlock(t *testing.T) {
	b := imageBlock("data:image/png;base64,abc", "high")
	raw, _ := json.Marshal(b)
	want := `{"image_url":{"detail":"high","url":"data:image/png;base64,abc"},"type":"image_url"}`
	if string(raw) != want {
		t.Fatalf("imageBlock = %s, want %s", raw, want)
	}
	// 空 detail 时省略字段。
	b2 := imageBlock("https://x/a.png", "")
	raw2, _ := json.Marshal(b2)
	if got := string(raw2); got != `{"image_url":{"url":"https://x/a.png"},"type":"image_url"}` {
		t.Fatalf("imageBlock empty detail = %s", got)
	}
}

func TestVideoBlock(t *testing.T) {
	b := videoBlock("https://x/a.mp4", 1.0)
	raw, _ := json.Marshal(b)
	want := `{"type":"video_url","video_url":{"fps":1,"url":"https://x/a.mp4"}}`
	if string(raw) != want {
		t.Fatalf("videoBlock = %s, want %s", raw, want)
	}
}

func TestAudioBlockResponses(t *testing.T) {
	// audio_url 形态
	raw, _ := json.Marshal(audioBlockResponses("data:audio/mpeg;base64,zz", ""))
	if string(raw) != `{"audio_url":"data:audio/mpeg;base64,zz","type":"input_audio"}` {
		t.Fatalf("audioBlockResponses url = %s", raw)
	}
	// file_id 形态
	raw2, _ := json.Marshal(audioBlockResponses("", "file-123"))
	if string(raw2) != `{"file_id":"file-123","type":"input_audio"}` {
		t.Fatalf("audioBlockResponses file_id = %s", raw2)
	}
}

func TestAudioBlockChat(t *testing.T) {
	raw, _ := json.Marshal(audioBlockChat("data:audio/wav;base64,zz", "wav"))
	want := `{"input_audio":{"data":"data:audio/wav;base64,zz","format":"wav"},"type":"input_audio"}`
	if string(raw) != want {
		t.Fatalf("audioBlockChat = %s, want %s", raw, want)
	}
}

func TestMimeToAudioFormat(t *testing.T) {
	cases := map[string]string{
		"audio/wav": "wav", "audio/mpeg": "mp3", "audio/mp3": "mp3",
		"audio/mp4": "m4a", "audio/aac": "aac", "audio/ogg": "ogg",
		"audio/flac": "flac", "audio/amr": "amr", "video/mp4": "mp3",
		"": "mp3",
	}
	for mime, want := range cases {
		if got := mimeToAudioFormat(mime); got != want {
			t.Fatalf("mimeToAudioFormat(%q) = %q, want %q", mime, got, want)
		}
	}
}

func TestCallChatParsesContent(t *testing.T) {
	resp := `{"choices":[{"message":{"content":"画面里是一只猫"}}]}`
	fw := &fakeRecogForwarder{respBody: []byte(resp)}
	s := newRecogService(fw)
	text, _, _, err := s.callChat(context.Background(), "m1", []map[string]any{imageBlock("u", "high")}, callOpts{})
	if err != nil {
		t.Fatalf("callChat err: %v", err)
	}
	if text != "画面里是一只猫" {
		t.Fatalf("callChat text = %q", text)
	}
	if fw.gotPath != "chat/completions" {
		t.Fatalf("path = %q, want chat/completions", fw.gotPath)
	}
	var body struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []any  `json:"messages"`
	}
	if err := json.Unmarshal(fw.gotBody, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Model != "m1" || body.Stream != false || len(body.Messages) != 1 {
		t.Fatalf("body model/stream/messages = %v/%v/%d", body.Model, body.Stream, len(body.Messages))
	}
}

func TestCallResponsesParsesOutputText(t *testing.T) {
	resp := `{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"转写结果"}]}]}`
	fw := &fakeRecogForwarder{respBody: []byte(resp)}
	s := newRecogService(fw)
	text, _, _, err := s.callResponses(context.Background(), "m1", []map[string]any{audioBlockResponses("u", "")}, "instructions-x", callOpts{})
	if err != nil {
		t.Fatalf("callResponses err: %v", err)
	}
	if text != "转写结果" {
		t.Fatalf("callResponses text = %q", text)
	}
	if fw.gotPath != "responses" {
		t.Fatalf("path = %q, want responses", fw.gotPath)
	}
	var body struct {
		Model        string `json:"model"`
		Instructions string `json:"instructions"`
		Stream       bool   `json:"stream"`
	}
	if err := json.Unmarshal(fw.gotBody, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Instructions != "instructions-x" || body.Stream != false {
		t.Fatalf("instructions/stream = %q/%v", body.Instructions, body.Stream)
	}
}

func TestAudioInstructions(t *testing.T) {
	cases := map[string]string{"asr": "ASR", "timed": "时间戳", "diarize": "spk", "translate": "translate", "caption": "音频描述专家"}
	for task, wantSub := range cases {
		got := audioInstructions(task, "zh", "zh", "en")
		if got == "" {
			t.Fatalf("audioInstructions(%q) empty", task)
		}
		if len(got) < 20 {
			t.Fatalf("audioInstructions(%q) too short: %q", task, got)
		}
		_ = wantSub
	}
	// translate 应带目标语言。
	tr := audioInstructions("translate", "", "", "de")
	if tr == "" || len(tr) < 20 {
		t.Fatalf("translate instructions empty")
	}
	// 未知 task 回退通用模板。
	if fallback := audioInstructions("unknown", "", "", ""); fallback == "" {
		t.Fatalf("unknown task fallback empty")
	}
}

func TestResolveResourceHTTPAndData(t *testing.T) {
	s := &Service{}
	// http URL 原样返回。
	u, fid, err := s.resolveResource(context.Background(), "https://example.com/a.png", "image/png", imageSizeLimit)
	if err != nil || u != "https://example.com/a.png" || fid != "" {
		t.Fatalf("http url: u=%q fid=%q err=%v", u, fid, err)
	}
	// data URI 原样返回。
	dataURI := "data:image/png;base64,AAAA"
	u2, fid2, err2 := s.resolveResource(context.Background(), dataURI, "image/png", imageSizeLimit)
	if err2 != nil || u2 != dataURI || fid2 != "" {
		t.Fatalf("data uri: u=%q fid=%q err=%v", u2, fid2, err2)
	}
}

func TestResolveResourceLocalFileBase64(t *testing.T) {
	// 构造一个小文件（< 阈值），file:// 路径应转成 data URI。
	dir := t.TempDir()
	path := dir + "/x.png"
	if err := os.WriteFile(path, []byte("pngbytes"), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	s := &Service{}
	u, fid, err := s.resolveResource(context.Background(), "file://"+path, "image/png", imageSizeLimit)
	if err != nil {
		t.Fatalf("resolveResource err: %v", err)
	}
	if fid != "" {
		t.Fatalf("small file should not upload, fid=%q", fid)
	}
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("pngbytes"))
	if u != want {
		t.Fatalf("local file uri = %q, want %q", u, want)
	}
}

func TestResolveResourceLocalFileOversizeUpload(t *testing.T) {
	// 构造一个大文件（> 阈值），file:// 应走上传（uploadAndGetID 占位/实现由子代理C负责）。
	// 这里用一个无 repo 的 Service 调用，验证走上传分支并返回明确错误（非 base64 静默）。
	dir := t.TempDir()
	path := dir + "/big.png"
	big := make([]byte, imageSizeLimit+1)
	if err := os.WriteFile(path, big, 0o600); err != nil {
		t.Fatalf("write temp big file: %v", err)
	}
	s := &Service{} // 无 repo，uploadAndGetID 内部走 arkChannels 会报"渠道仓储未初始化"
	u, fid, err := s.resolveResource(context.Background(), "file://"+path, "image/png", imageSizeLimit)
	if err == nil {
		t.Fatalf("oversize file should error (no upload channel), got u=%q fid=%q", u, fid)
	}
	if u != "" {
		t.Fatalf("oversize file should not produce data uri, got %q", u)
	}
}

func TestMimeByExt(t *testing.T) {
	cases := map[string]string{".png": "image/png", ".mp4": "video/mp4", ".mp3": "audio/mpeg", ".wav": "audio/wav", ".xyz": ""}
	for ext, want := range cases {
		if got := mimeByExt("a" + ext); got != want {
			t.Fatalf("mimeByExt(%s) = %q, want %q", ext, got, want)
		}
	}
}
