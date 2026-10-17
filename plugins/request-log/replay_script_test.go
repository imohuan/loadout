package requestlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 复现脚本必须把 body 原样带进去：脚本里的字符串字面量反解出来要与输入逐字节一致。
func TestBuildReplayScriptKeepsBodyVerbatim(t *testing.T) {
	body := []byte("{\"model\":\"step-5-preview\",\"stream\":true,\"messages\":[{\"role\":\"user\",\"content\":\"反引号 \x60 与 " + "$" + "{模板} 与 </script>\"}],\"tools\":[{\"type\":\"function\",\"function\":{\"name\":\"canvas_ui_read\",\"parameters\":{\"type\":\"object\"}}}]}")
	script, err := BuildReplayScript(body, "req-abc")
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	const marker = "const REQUEST_BODY = "
	i := strings.Index(text, marker)
	if i < 0 {
		t.Fatal("script missing REQUEST_BODY literal")
	}
	rest := text[i+len(marker):]
	end := strings.Index(rest, ";\n")
	if end < 0 {
		t.Fatal("script REQUEST_BODY literal not terminated")
	}
	var decoded string
	if err := json.Unmarshal([]byte(rest[:end]), &decoded); err != nil {
		t.Fatalf("REQUEST_BODY literal is not a valid JSON string: %v", err)
	}
	if decoded != string(body) {
		t.Fatalf("body changed:\n got %q\nwant %q", decoded, string(body))
	}
	if !strings.Contains(text, "LOADOUT_API_KEY") {
		t.Fatal("script must read LOADOUT_API_KEY")
	}
}

func TestBuildReplayScriptRejectsInvalidBody(t *testing.T) {
	if _, err := BuildReplayScript([]byte("{not json"), "id"); err == nil {
		t.Fatal("expected error for invalid JSON body")
	}
	if _, err := BuildReplayScript(nil, "id"); err == nil {
		t.Fatal("expected error for empty body")
	}
}

// 路径归一：/v1/chat/completions 与 chat/completions 都落到同一个目标地址。
func TestBuildReplayScriptNormalizesPath(t *testing.T) {
	body := []byte("{\"model\":\"m\",\"messages\":[]}")
	for _, p := range []string{"chat/completions", "/chat/completions", "/v1/chat/completions", "v2/chat/completions"} {
		script, err := BuildReplayScriptWithOptions(body, ReplayScriptOptions{RequestID: "x", Path: p})
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if !strings.Contains(string(script), "'/v1/' + REQUEST_PATH") {
			t.Fatalf("%s: script should build /v1/<path>", p)
		}
		if !strings.Contains(string(script), "\"chat/completions\"") {
			t.Fatalf("%s: path not normalized", p)
		}
	}
}

// WriteReplayScript 落盘到 <数据目录>/replay-scripts/<id>.mjs，可覆盖重写。
func TestWriteReplayScriptWritesFile(t *testing.T) {
	svc := testService(t, nil)
	body := []byte("{\"model\":\"m\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}")
	path, size, err := svc.WriteReplayScript("abc123", body, ReplayScriptOptions{Path: "chat/completions"})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(filepath.Dir(path)) != "replay-scripts" {
		t.Fatalf("unexpected dir: %s", path)
	}
	if filepath.Ext(path) != ".mjs" {
		t.Fatalf("unexpected ext: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != size {
		t.Fatalf("reported size %d != file size %d", size, len(data))
	}
	if _, _, err := svc.WriteReplayScript("abc123", body, ReplayScriptOptions{}); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
}

// ID 来自 URL，必须过滤掉路径穿越字符。
func TestWriteReplayScriptSanitizesID(t *testing.T) {
	svc := testService(t, nil)
	path, _, err := svc.WriteReplayScript("../evil", []byte("{\"model\":\"m\"}"), ReplayScriptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(path, "..") {
		t.Fatalf("path traversal not sanitized: %s", path)
	}
}
