package modelgateway

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestReadUpstreamErrorBody 覆盖 gzip 修复的三种典型场景。
//
// 历史 bug：上游错误体是 gzip 字节（0x1f 0x8b magic）但 Content-Encoding 缺失，
// 旧实现 io.ReadAll 直接落库，前端看到 16 进制乱码、extractErrorSummary 无法识别。
// 现 readUpstreamErrorBody 双层检测（header + magic）确保一定能解压。
func TestReadUpstreamErrorBody(t *testing.T) {
	const jsonBody = `{"error":{"code":14018,"message":"额度已用尽，请访问以下链接，购买加量包以获取更多额度："}}`

	t.Run("plain text body is passed through", func(t *testing.T) {
		got, err := readUpstreamErrorBody(strings.NewReader(jsonBody), "", 8192)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if string(got) != jsonBody {
			t.Fatalf("body = %q, want %q", got, jsonBody)
		}
	})

	t.Run("gzip with Content-Encoding header is decompressed", func(t *testing.T) {
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		_, _ = gw.Write([]byte(jsonBody))
		_ = gw.Close()
		got, err := readUpstreamErrorBody(&buf, "gzip", 8192)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if string(got) != jsonBody {
			t.Fatalf("body = %q, want %q", got, jsonBody)
		}
	})

	t.Run("gzip magic without Content-Encoding is still decompressed", func(t *testing.T) {
		// 模拟某些代理省略 Content-Encoding 但 body 仍是 gzip 的场景。
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		_, _ = gw.Write([]byte(jsonBody))
		_ = gw.Close()
		got, err := readUpstreamErrorBody(&buf, "", 8192)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if string(got) != jsonBody {
			t.Fatalf("body = %q, want %q", got, jsonBody)
		}
		// 第一字节应该是 '{'，不再是 gzip magic 0x1f
		if len(got) > 0 && got[0] == 0x1f {
			t.Fatal("body 仍以 gzip magic 开头，未解压")
		}
	})

	t.Run("decompressed body is truncated to maxBytes", func(t *testing.T) {
		big := strings.Repeat("A", 20000)
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		_, _ = gw.Write([]byte(big))
		_ = gw.Close()
		got, err := readUpstreamErrorBody(&buf, "gzip", 1024)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if len(got) != 1024 {
			t.Fatalf("len = %d, want 1024", len(got))
		}
	})

	t.Run("non-gzip body with bogus Content-Encoding: gzip falls back to raw", func(t *testing.T) {
		got, err := readUpstreamErrorBody(strings.NewReader(jsonBody), "gzip", 8192)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		// 解压失败应 fallback 到原字节
		if string(got) != jsonBody {
			t.Fatalf("body = %q, want raw fallback %q", got, jsonBody)
		}
	})

	t.Run("empty body returns empty", func(t *testing.T) {
		got, err := readUpstreamErrorBody(strings.NewReader(""), "", 8192)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("body = %q, want empty", got)
		}
	})
}

// TestReadUpstreamErrorBodyFromHTTPResponse 端到端：真实 http.Response.Body 流
// 触发上游 gzip 错误时，readUpstreamErrorBody 返回的是解压后的明文 JSON。
func TestReadUpstreamErrorBodyFromHTTPResponse(t *testing.T) {
	const jsonBody = `{"code":403,"message":"forbidden"}`
	var gzBuf bytes.Buffer
	gw := gzip.NewWriter(&gzBuf)
	_, _ = gw.Write([]byte(jsonBody))
	_ = gw.Close()

	// httptest 模拟一个 403 + 省略 Content-Encoding 但 body 是 gzip 的响应。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write(gzBuf.Bytes())
	}))
	defer srv.Close()

	// 关掉 Go client 的自动 gzip 解压，让响应 body 保持 gzip 字节
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	got, err := readUpstreamErrorBody(resp.Body, resp.Header.Get("Content-Encoding"), 8192)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if string(got) != jsonBody {
		t.Fatalf("body = %q, want %q", got, jsonBody)
	}
}

// 避免 io 引用未被使用（保留扩展点：未来可能加 chunked / 流式测试）
var _ = io.Discard
